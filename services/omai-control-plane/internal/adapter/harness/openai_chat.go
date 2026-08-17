package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
)

const maxChatRequestBytes = 32 << 20

type ModelEdge struct {
	leases  *LeaseStore
	client  uabv1connect.ModelGatewayServiceClient
	version string
}

func NewModelEdge(leases *LeaseStore, config ModelGatewayConfig, version string) (*ModelEdge, error) {
	if leases == nil {
		return nil, errors.New("model lease store is required")
	}
	client, err := newModelGatewayClient(config)
	if err != nil {
		return nil, err
	}
	return &ModelEdge{leases: leases, client: client, version: version}, nil
}

func (e *ModelEdge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/chat/completions", e.chatCompletions)
	return http.MaxBytesHandler(mux, maxChatRequestBytes)
}

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []chatMessage     `json:"messages"`
	Tools               []chatTool        `json:"tools"`
	Stream              bool              `json:"stream"`
	StreamOptions       chatStreamOptions `json:"stream_options"`
	Temperature         *float32          `json:"temperature"`
	TopP                *float32          `json:"top_p"`
	MaxTokens           int32             `json:"max_tokens"`
	MaxCompletionTokens int32             `json:"max_completion_tokens"`
	Stop                json.RawMessage   `json:"stop"`
	User                string            `json:"user"`
	FrequencyPenalty    *float32          `json:"frequency_penalty"`
	PresencePenalty     *float32          `json:"presence_penalty"`
	ResponseFormat      json.RawMessage   `json:"response_format"`
	Seed                *int64            `json:"seed"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	Verbosity           string            `json:"verbosity"`
	ToolChoice          json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Name             string          `json:"name"`
	ID               string          `json:"id"`
	Content          json.RawMessage `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCallID       string          `json:"tool_call_id"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
}

func (e *ModelEdge) chatCompletions(response http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxChatRequestBytes))
	decoder.DisallowUnknownFields()
	var input chatRequest
	if err := decoder.Decode(&input); err != nil {
		writeOpenAIError(response, http.StatusBadRequest, "invalid_request_error", "invalid JSON request")
		return
	}
	if err := requireJSONEOF(decoder); err != nil {
		writeOpenAIError(response, http.StatusBadRequest, "invalid_request_error", "request contains trailing JSON")
		return
	}
	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		writeOpenAIError(response, http.StatusUnauthorized, "invalid_api_key", "invalid model capability")
		return
	}
	authorization, ok := e.leases.authorize(token, input.Model)
	if !ok {
		writeOpenAIError(response, http.StatusUnauthorized, "invalid_api_key", "invalid or expired model capability")
		return
	}
	message, err := toModelRequest(input, authorization)
	if err != nil {
		writeOpenAIError(response, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	stream, err := e.client.Generate(request.Context(), connect.NewRequest(message))
	if err != nil {
		writeOpenAIError(response, http.StatusBadGateway, "model_gateway_error", safeGatewayError(err))
		return
	}
	id, err := completionID()
	if err != nil {
		writeOpenAIError(response, http.StatusInternalServerError, "internal_error", "could not create response identifier")
		return
	}
	if input.Stream {
		e.streamCompletion(request.Context(), response, stream, id, input.Model, input.StreamOptions.IncludeUsage)
		return
	}
	e.complete(request.Context(), response, stream, id, input.Model)
}

func toModelRequest(input chatRequest, authorization modelAuthorization) (*uabv1.ModelGenerateRequest, error) {
	if input.Model == "" || len(input.Messages) == 0 || len(input.Messages) > 256 {
		return nil, errors.New("model and 1-256 messages are required")
	}
	if input.FrequencyPenalty != nil || input.PresencePenalty != nil || rawPresent(input.ResponseFormat) || input.Seed != nil {
		return nil, errors.New("frequency/presence penalties, response_format and seed are not supported by the provider-neutral model gateway")
	}
	if rawPresent(input.ToolChoice) {
		var choice string
		if json.Unmarshal(input.ToolChoice, &choice) != nil || (choice != "auto" && choice != "none") {
			return nil, errors.New("tool_choice currently supports only auto or none")
		}
		if choice == "none" {
			input.Tools = nil
		}
	}
	contents := make([]*uabv1.ModelContent, 0, len(input.Messages))
	system := make([]string, 0, 4)
	callNames := make(map[string]string)
	for index, message := range input.Messages {
		switch message.Role {
		case "system", "developer":
			text, err := textContent(message.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", index, err)
			}
			if text != "" {
				system = append(system, text)
			}
		case "user":
			parts, err := contentParts(message.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", index, err)
			}
			contents = append(contents, &uabv1.ModelContent{Role: "user", Parts: parts})
		case "assistant":
			parts, err := contentPartsOptional(message.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", index, err)
			}
			if message.ReasoningContent != "" {
				parts = append(parts, &uabv1.ModelPart{Text: message.ReasoningContent, Thought: true})
			}
			for callIndex, call := range message.ToolCalls {
				if call.ID == "" || call.Function.Name == "" || (call.Type != "" && call.Type != "function") {
					return nil, fmt.Errorf("messages[%d].tool_calls[%d] is invalid", index, callIndex)
				}
				arguments, err := objectBytes([]byte(call.Function.Arguments), false)
				if err != nil {
					return nil, fmt.Errorf("messages[%d].tool_calls[%d].arguments: %w", index, callIndex, err)
				}
				callNames[call.ID] = call.Function.Name
				parts = append(parts, &uabv1.ModelPart{FunctionCall: &uabv1.ModelFunctionCall{Id: call.ID, Name: call.Function.Name, ArgumentsJson: arguments}})
			}
			if len(parts) == 0 {
				return nil, fmt.Errorf("messages[%d] has no content", index)
			}
			contents = append(contents, &uabv1.ModelContent{Role: "model", Parts: parts})
		case "tool":
			name := callNames[message.ToolCallID]
			if name == "" {
				name = message.Name
			}
			if message.ToolCallID == "" || name == "" {
				return nil, fmt.Errorf("messages[%d] tool_call_id and function name are required", index)
			}
			text, err := textContent(message.Content)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", index, err)
			}
			result, err := resultObject(text)
			if err != nil {
				return nil, fmt.Errorf("messages[%d]: %w", index, err)
			}
			contents = append(contents, &uabv1.ModelContent{Role: "user", Parts: []*uabv1.ModelPart{{FunctionResult: &uabv1.ModelFunctionResult{Id: message.ToolCallID, Name: name, ResultJson: result}}}})
		default:
			return nil, fmt.Errorf("messages[%d].role is unsupported", index)
		}
	}
	if len(contents) == 0 {
		return nil, errors.New("messages contain no model input")
	}
	tools := make([]*uabv1.ModelTool, 0, len(input.Tools))
	if len(input.Tools) > 512 {
		return nil, errors.New("tools exceed the 512-item limit")
	}
	for index, tool := range input.Tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			return nil, fmt.Errorf("tools[%d] must be a named function", index)
		}
		schema, err := objectBytes(tool.Function.Parameters, true)
		if err != nil {
			return nil, fmt.Errorf("tools[%d].function.parameters: %w", index, err)
		}
		tools = append(tools, &uabv1.ModelTool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchemaJson: schema})
	}
	config := &uabv1.ModelGenerationConfig{SystemInstruction: strings.Join(system, "\n\n")}
	if input.Temperature != nil {
		value := *input.Temperature
		config.Temperature = &value
	}
	if input.TopP != nil {
		value := *input.TopP
		config.TopP = &value
	}
	maximum := input.MaxCompletionTokens
	if maximum == 0 {
		maximum = input.MaxTokens
	}
	if maximum < 0 || maximum > 131_072 {
		return nil, errors.New("max completion tokens must be between 0 and 131072")
	}
	config.MaxOutputTokens = maximum
	stop, err := stopSequences(input.Stop)
	if err != nil {
		return nil, err
	}
	config.StopSequences = stop
	return &uabv1.ModelGenerateRequest{
		TenantId: authorization.TenantID, ActorId: authorization.ActorID, SessionId: authorization.SessionID,
		ProviderId: authorization.Provider, ModelId: authorization.Model,
		Contents: contents, Tools: tools, Config: config,
	}, nil
}

func rawPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

func contentParts(raw json.RawMessage) ([]*uabv1.ModelPart, error) {
	parts, err := contentPartsOptional(raw)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("message content is required")
	}
	return parts, nil
}

func contentPartsOptional(raw json.RawMessage) ([]*uabv1.ModelPart, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []*uabv1.ModelPart{{Text: text}}, nil
	}
	var source []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
		InputAudio struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		} `json:"input_audio"`
		File struct {
			Filename string `json:"filename"`
			FileData string `json:"file_data"`
		} `json:"file"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, errors.New("content must be a string or content-part array")
	}
	parts := make([]*uabv1.ModelPart, 0, len(source))
	for index, part := range source {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				parts = append(parts, &uabv1.ModelPart{Text: part.Text})
			}
		case "image_url":
			mime, data, err := dataURL(part.ImageURL.URL)
			if err != nil {
				return nil, fmt.Errorf("content part %d: %w", index, err)
			}
			parts = append(parts, &uabv1.ModelPart{InlineData: data, MimeType: mime})
		case "input_audio":
			data, err := decodeInlineBase64(part.InputAudio.Data)
			if err != nil || part.InputAudio.Format == "" {
				return nil, fmt.Errorf("content part %d contains invalid audio", index)
			}
			parts = append(parts, &uabv1.ModelPart{InlineData: data, MimeType: "audio/" + part.InputAudio.Format})
		case "file":
			mime, data, err := dataURL(part.File.FileData)
			if err != nil || mime != "application/pdf" {
				return nil, fmt.Errorf("content part %d contains an invalid PDF", index)
			}
			parts = append(parts, &uabv1.ModelPart{InlineData: data, MimeType: mime})
		default:
			return nil, fmt.Errorf("content part %d has unsupported type", index)
		}
	}
	return parts, nil
}

func textContent(raw json.RawMessage) (string, error) {
	parts, err := contentParts(raw)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	for _, part := range parts {
		if part.GetText() == "" {
			return "", errors.New("only text content is allowed for this role")
		}
		result.WriteString(part.GetText())
	}
	return result.String(), nil
}

func dataURL(value string) (string, []byte, error) {
	if !strings.HasPrefix(value, "data:") {
		return "", nil, errors.New("remote image URLs are forbidden; use a base64 data URL")
	}
	header, payload, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
	if !ok || !strings.HasSuffix(header, ";base64") {
		return "", nil, errors.New("image must be a base64 data URL")
	}
	mime := strings.TrimSuffix(header, ";base64")
	if !strings.HasPrefix(mime, "image/") && mime != "application/pdf" {
		return "", nil, errors.New("data URL must contain an image or PDF MIME type")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 || len(data) > 20<<20 {
		return "", nil, errors.New("image data is invalid or exceeds 20 MiB")
	}
	return mime, data, nil
}

func decodeInlineBase64(value string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(data) == 0 || len(data) > 20<<20 {
		return nil, errors.New("inline data is invalid or exceeds 20 MiB")
	}
	return data, nil
}

func objectBytes(raw []byte, defaultObject bool) ([]byte, error) {
	if len(raw) == 0 {
		if defaultObject {
			return []byte(`{"type":"object","properties":{}}`), nil
		}
		return []byte(`{}`), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("must be a JSON object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, errors.New("must contain one JSON object")
	}
	return json.Marshal(value)
}

func resultObject(text string) ([]byte, error) {
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		value = text
	}
	if object, ok := value.(map[string]any); ok {
		return json.Marshal(object)
	}
	return json.Marshal(map[string]any{"output": value})
}

func stopSequences(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil || len(multiple) > 64 {
		return nil, errors.New("stop must be a string or an array of at most 64 strings")
	}
	return multiple, nil
}

func (e *ModelEdge) streamCompletion(ctx context.Context, response http.ResponseWriter, stream *connect.ServerStreamForClient[uabv1.ModelGenerateEvent], id, model string, includeUsage bool) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := response.(http.Flusher)
	created := time.Now().Unix()
	_ = writeSSE(response, map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}},
	})
	if flusher != nil {
		flusher.Flush()
	}
	state := newCompletionState()
	for stream.Receive() {
		if ctx.Err() != nil {
			return
		}
		event := stream.Msg()
		state.observe(event)
		for _, delta := range state.deltas(event) {
			_ = writeSSE(response, map[string]any{
				"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
			})
			if flusher != nil {
				flusher.Flush()
			}
		}
		if event.GetKind() == uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_ERROR {
			code, message := safeModelEventError(event)
			_ = writeSSE(response, map[string]any{"error": map[string]any{"message": message, "type": "model_gateway_error", "code": code}})
			_, _ = io.WriteString(response, "data: [DONE]\n\n")
			return
		}
	}
	if err := stream.Err(); err != nil {
		_ = writeSSE(response, map[string]any{"error": map[string]any{"message": safeGatewayError(err), "type": "model_gateway_error"}})
		_, _ = io.WriteString(response, "data: [DONE]\n\n")
		return
	}
	final := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": state.finishReason()}},
	}
	if includeUsage {
		final["usage"] = state.usageMap()
	}
	_ = writeSSE(response, final)
	_, _ = io.WriteString(response, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (e *ModelEdge) complete(ctx context.Context, response http.ResponseWriter, stream *connect.ServerStreamForClient[uabv1.ModelGenerateEvent], id, model string) {
	state := newCompletionState()
	for stream.Receive() {
		if ctx.Err() != nil {
			return
		}
		event := stream.Msg()
		if event.GetKind() == uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_ERROR {
			code, message := safeModelEventError(event)
			writeOpenAIError(response, http.StatusBadGateway, code, message)
			return
		}
		state.observe(event)
		state.deltas(event)
	}
	if err := stream.Err(); err != nil {
		writeOpenAIError(response, http.StatusBadGateway, "model_gateway_error", safeGatewayError(err))
		return
	}
	message := map[string]any{"role": "assistant", "content": state.text}
	if len(state.toolCalls) != 0 {
		message["tool_calls"] = state.toolCalls
		if state.text == "" {
			message["content"] = nil
		}
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": state.finishReason()}},
		"usage":   state.usageMap(),
	})
}

type completionState struct {
	text           string
	finish         string
	usage          *uabv1.ModelUsage
	toolCalls      []map[string]any
	toolIndexes    map[string]int
	toolSignatures map[string]string
}

func newCompletionState() *completionState {
	return &completionState{toolIndexes: make(map[string]int), toolSignatures: make(map[string]string)}
}

func (s *completionState) observe(event *uabv1.ModelGenerateEvent) {
	if event.GetUsage() != nil {
		s.usage = event.GetUsage()
	}
	if event.GetFinishReason() != "" {
		s.finish = event.GetFinishReason()
	}
}

func (s *completionState) deltas(event *uabv1.ModelGenerateEvent) []map[string]any {
	content := event.GetContent()
	if content == nil {
		return nil
	}
	result := make([]map[string]any, 0, len(content.GetParts()))
	for _, part := range content.GetParts() {
		if part.GetText() != "" {
			delta := s.textDelta(part.GetText(), event.GetPartial())
			if delta == "" {
				continue
			}
			if part.GetThought() {
				result = append(result, map[string]any{"reasoning_content": delta})
			} else {
				result = append(result, map[string]any{"content": delta})
			}
			continue
		}
		call := part.GetFunctionCall()
		if call == nil {
			continue
		}
		arguments := string(call.GetArgumentsJson())
		signature := call.GetName() + "\x00" + arguments
		if s.toolSignatures[call.GetId()] == signature {
			continue
		}
		s.toolSignatures[call.GetId()] = signature
		index, exists := s.toolIndexes[call.GetId()]
		if !exists {
			index = len(s.toolCalls)
			s.toolIndexes[call.GetId()] = index
			s.toolCalls = append(s.toolCalls, map[string]any{
				"id": call.GetId(), "type": "function",
				"function": map[string]any{"name": call.GetName(), "arguments": arguments},
			})
		}
		result = append(result, map[string]any{"tool_calls": []any{map[string]any{
			"index": index, "id": call.GetId(), "type": "function",
			"function": map[string]any{"name": call.GetName(), "arguments": arguments},
		}}})
	}
	return result
}

func (s *completionState) textDelta(text string, partial bool) string {
	if !partial && strings.HasPrefix(text, s.text) {
		text = strings.TrimPrefix(text, s.text)
	}
	s.text += text
	return text
}

func (s *completionState) finishReason() string {
	if len(s.toolCalls) != 0 {
		return "tool_calls"
	}
	switch strings.ToUpper(s.finish) {
	case "MAX_TOKENS", "MAX_TOKEN", "LENGTH":
		return "length"
	case "SAFETY", "CONTENT_FILTER":
		return "content_filter"
	default:
		return "stop"
	}
}

func (s *completionState) usageMap() map[string]int32 {
	if s.usage == nil {
		return map[string]int32{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	return map[string]int32{
		"prompt_tokens": s.usage.GetInputTokens(), "completion_tokens": s.usage.GetOutputTokens(),
		"total_tokens": s.usage.GetTotalTokens(),
	}
}

func writeSSE(writer io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", data)
	return err
}

func writeOpenAIError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func bearerToken(value string) (string, bool) {
	if len(value) > 512 || !strings.HasPrefix(value, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func completionID() (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return "chatcmpl_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func safeGatewayError(err error) string {
	if err == nil {
		return "model gateway unavailable"
	}
	if code := connect.CodeOf(err); code != connect.CodeUnknown {
		return "model gateway " + code.String()
	}
	return "model gateway unavailable"
}

func safeModelEventError(event *uabv1.ModelGenerateEvent) (string, string) {
	code := strings.ToLower(strings.TrimSpace(event.GetErrorCode()))
	if code == "" || len(code) > 64 {
		code = "model_gateway_error"
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			code = "model_gateway_error"
			break
		}
	}
	return code, "model gateway request failed (" + code + ")"
}
