package modelrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const maxOpenAIChatPayloadBytes = 32 << 20

var providerErrorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// openAIChatModel is the ADK model port for providers that implement the
// OpenAI Chat Completions contract but not the OpenAI Responses contract.
// Provider credentials never leave this process.
type openAIChatModel struct {
	modelID  string
	endpoint string
	apiKey   string
	client   *http.Client
}

func newOpenAIChatModel(modelID, baseURL, apiKey string, client *http.Client) (*openAIChatModel, error) {
	if !validModelID(modelID) {
		return nil, errors.New("model id is invalid")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("provider credential is not configured")
	}
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/") + "/chat/completions")
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("provider Chat Completions endpoint is invalid")
	}
	return &openAIChatModel{
		modelID:  modelID,
		endpoint: parsed.String(),
		apiKey:   strings.TrimSpace(apiKey),
		client:   client,
	}, nil
}

func (m *openAIChatModel) Name() string { return "openai-chat-completions/" + m.modelID }

func (m *openAIChatModel) GenerateContent(ctx context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		response, err := m.generate(ctx, request)
		yield(response, err)
	}
}

func (m *openAIChatModel) generate(ctx context.Context, request *model.LLMRequest) (*model.LLMResponse, error) {
	input, err := toOpenAIChatRequest(request, m.modelID)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode Chat Completions request: %w", err)
	}
	if len(body) > maxOpenAIChatPayloadBytes {
		return nil, fmt.Errorf("chat completions request exceeds %d bytes", maxOpenAIChatPayloadBytes)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Chat Completions request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	httpResponse, err := m.client.Do(httpRequest)
	if err != nil {
		return nil, errors.New("chat completions provider is unavailable")
	}
	defer httpResponse.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxOpenAIChatPayloadBytes+1))
	if err != nil {
		return nil, errors.New("read Chat Completions response")
	}
	if len(responseBody) > maxOpenAIChatPayloadBytes {
		return nil, fmt.Errorf("chat completions response exceeds %d bytes", maxOpenAIChatPayloadBytes)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return nil, safeOpenAIChatStatusError(httpResponse.StatusCode, responseBody)
	}
	var response openAIChatResponse
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&response); err != nil {
		return nil, errors.New("decode Chat Completions response")
	}
	if err := requireOpenAIChatJSONEOF(decoder); err != nil {
		return nil, errors.New("decode Chat Completions response")
	}
	return fromOpenAIChatResponse(response)
}

type openAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openAIChatMessage `json:"messages"`
	Tools       []openAIChatTool    `json:"tools,omitempty"`
	ToolChoice  string              `json:"tool_choice,omitempty"`
	Stream      bool                `json:"stream"`
	Temperature *float32            `json:"temperature,omitempty"`
	TopP        *float32            `json:"top_p,omitempty"`
	MaxTokens   int32               `json:"max_tokens,omitempty"`
	Stop        []string            `json:"stop,omitempty"`
}

type openAIChatMessage struct {
	Role             string               `json:"role"`
	Content          any                  `json:"content"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCallID       string               `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIChatToolCall `json:"tool_calls,omitempty"`
}

type openAIChatToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIChatFunctionCall `json:"function"`
}

type openAIChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatTool struct {
	Type     string                 `json:"type"`
	Function openAIChatFunctionTool `json:"function"`
}

type openAIChatFunctionTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

func toOpenAIChatRequest(request *model.LLMRequest, fallbackModel string) (openAIChatRequest, error) {
	if request == nil {
		return openAIChatRequest{}, errors.New("chat completions request is required")
	}
	modelID := request.Model
	if modelID == "" {
		modelID = fallbackModel
	}
	if !validModelID(modelID) {
		return openAIChatRequest{}, errors.New("chat completions model id is invalid")
	}
	result := openAIChatRequest{Model: modelID, Stream: false}
	if request.Config != nil {
		instruction, err := openAIChatText(request.Config.SystemInstruction)
		if err != nil {
			return openAIChatRequest{}, fmt.Errorf("system instruction: %w", err)
		}
		if instruction != "" {
			result.Messages = append(result.Messages, openAIChatMessage{Role: "system", Content: instruction})
		}
		result.Temperature = request.Config.Temperature
		result.TopP = request.Config.TopP
		result.MaxTokens = request.Config.MaxOutputTokens
		result.Stop = append([]string(nil), request.Config.StopSequences...)
		tools, err := openAIChatTools(request.Config.Tools)
		if err != nil {
			return openAIChatRequest{}, err
		}
		result.Tools = tools
		if len(tools) != 0 {
			result.ToolChoice = "auto"
		}
	}
	for index, content := range request.Contents {
		messages, err := openAIChatMessages(content)
		if err != nil {
			return openAIChatRequest{}, fmt.Errorf("contents[%d]: %w", index, err)
		}
		result.Messages = append(result.Messages, messages...)
	}
	if len(result.Messages) == 0 || (len(result.Messages) == 1 && result.Messages[0].Role == "system") {
		return openAIChatRequest{}, errors.New("chat completions request has no conversation content")
	}
	return result, nil
}

func openAIChatMessages(content *genai.Content) ([]openAIChatMessage, error) {
	if content == nil || len(content.Parts) == 0 {
		return nil, errors.New("content and parts are required")
	}
	switch content.Role {
	case genai.RoleUser:
		var messages []openAIChatMessage
		var text strings.Builder
		flushText := func() {
			if text.Len() == 0 {
				return
			}
			messages = append(messages, openAIChatMessage{Role: "user", Content: text.String()})
			text.Reset()
		}
		for _, part := range content.Parts {
			if part == nil {
				return nil, errors.New("part is required")
			}
			switch {
			case part.Text != "":
				text.WriteString(part.Text)
			case part.FunctionResponse != nil:
				flushText()
				if part.FunctionResponse.ID == "" {
					return nil, errors.New("function response id is required")
				}
				encoded, err := json.Marshal(part.FunctionResponse.Response)
				if err != nil {
					return nil, fmt.Errorf("encode function response: %w", err)
				}
				messages = append(messages, openAIChatMessage{
					Role: "tool", Content: string(encoded), ToolCallID: part.FunctionResponse.ID,
				})
			default:
				return nil, errors.New("provider supports text and function responses only")
			}
		}
		flushText()
		if len(messages) == 0 {
			return nil, errors.New("user content is empty")
		}
		return messages, nil
	case genai.RoleModel:
		message := openAIChatMessage{Role: "assistant", Content: nil}
		var text strings.Builder
		var thought strings.Builder
		for _, part := range content.Parts {
			if part == nil {
				return nil, errors.New("part is required")
			}
			switch {
			case part.Text != "" && part.Thought:
				thought.WriteString(part.Text)
			case part.Text != "":
				text.WriteString(part.Text)
			case part.FunctionCall != nil:
				call := part.FunctionCall
				if call.ID == "" || call.Name == "" {
					return nil, errors.New("function call id and name are required")
				}
				arguments, err := json.Marshal(call.Args)
				if err != nil {
					return nil, fmt.Errorf("encode function arguments: %w", err)
				}
				message.ToolCalls = append(message.ToolCalls, openAIChatToolCall{
					ID: call.ID, Type: "function",
					Function: openAIChatFunctionCall{Name: call.Name, Arguments: string(arguments)},
				})
			default:
				return nil, errors.New("provider supports text and function calls only")
			}
		}
		if text.Len() != 0 {
			message.Content = text.String()
		}
		message.ReasoningContent = thought.String()
		if message.Content == nil && len(message.ToolCalls) == 0 {
			return nil, errors.New("assistant content is empty")
		}
		return []openAIChatMessage{message}, nil
	default:
		return nil, fmt.Errorf("role %q is not supported", content.Role)
	}
}

func openAIChatText(content *genai.Content) (string, error) {
	if content == nil {
		return "", nil
	}
	var text strings.Builder
	for _, part := range content.Parts {
		if part == nil {
			return "", errors.New("part is required")
		}
		if part.Text == "" || part.FunctionCall != nil || part.FunctionResponse != nil || part.InlineData != nil {
			return "", errors.New("only text parts are supported")
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func openAIChatTools(tools []*genai.Tool) ([]openAIChatTool, error) {
	var result []openAIChatTool
	for toolIndex, tool := range tools {
		if tool == nil {
			return nil, fmt.Errorf("tools[%d] is required", toolIndex)
		}
		for declarationIndex, declaration := range tool.FunctionDeclarations {
			if declaration == nil || declaration.Name == "" {
				return nil, fmt.Errorf("tools[%d].function_declarations[%d] is invalid", toolIndex, declarationIndex)
			}
			parameters := map[string]any{"type": "object", "properties": map[string]any{}}
			if declaration.ParametersJsonSchema != nil {
				var ok bool
				parameters, ok = declaration.ParametersJsonSchema.(map[string]any)
				if !ok || parameters == nil {
					return nil, fmt.Errorf("tools[%d].function_declarations[%d] JSON schema is not an object", toolIndex, declarationIndex)
				}
			}
			result = append(result, openAIChatTool{
				Type: "function",
				Function: openAIChatFunctionTool{
					Name: declaration.Name, Description: declaration.Description, Parameters: parameters,
				},
			})
		}
	}
	return result, nil
}

type openAIChatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          *string              `json:"content"`
			ReasoningContent string               `json:"reasoning_content"`
			ToolCalls        []openAIChatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int32 `json:"prompt_tokens"`
		CompletionTokens    int32 `json:"completion_tokens"`
		TotalTokens         int32 `json:"total_tokens"`
		PromptCacheHit      int32 `json:"prompt_cache_hit_tokens"`
		CompletionBreakdown struct {
			ReasoningTokens int32 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func fromOpenAIChatResponse(response openAIChatResponse) (*model.LLMResponse, error) {
	if len(response.Choices) != 1 {
		return nil, errors.New("chat completions response must contain exactly one choice")
	}
	choice := response.Choices[0]
	parts := make([]*genai.Part, 0, 2+len(choice.Message.ToolCalls))
	if choice.Message.ReasoningContent != "" {
		parts = append(parts, &genai.Part{Text: choice.Message.ReasoningContent, Thought: true})
	}
	if choice.Message.Content != nil && *choice.Message.Content != "" {
		parts = append(parts, &genai.Part{Text: *choice.Message.Content})
	}
	for index, call := range choice.Message.ToolCalls {
		if call.ID == "" || call.Type != "function" || call.Function.Name == "" {
			return nil, fmt.Errorf("chat completions tool_calls[%d] is invalid", index)
		}
		var arguments map[string]any
		decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
		if err := decoder.Decode(&arguments); err != nil || arguments == nil {
			return nil, fmt.Errorf("chat completions tool_calls[%d] arguments are invalid", index)
		}
		if err := requireOpenAIChatJSONEOF(decoder); err != nil {
			return nil, fmt.Errorf("chat completions tool_calls[%d] arguments are invalid", index)
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: call.ID, Name: call.Function.Name, Args: arguments,
		}})
	}
	if len(parts) == 0 {
		return nil, errors.New("chat completions response contains no model content")
	}
	modelVersion := response.Model
	if modelVersion == "" {
		modelVersion = "unknown"
	}
	return &model.LLMResponse{
		Content:      &genai.Content{Role: genai.RoleModel, Parts: parts},
		ModelVersion: modelVersion,
		FinishReason: openAIChatFinishReason(choice.FinishReason),
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        response.Usage.PromptTokens,
			CandidatesTokenCount:    response.Usage.CompletionTokens,
			TotalTokenCount:         response.Usage.TotalTokens,
			CachedContentTokenCount: response.Usage.PromptCacheHit,
			ThoughtsTokenCount:      response.Usage.CompletionBreakdown.ReasoningTokens,
		},
	}, nil
}

func openAIChatFinishReason(value string) genai.FinishReason {
	switch value {
	case "length":
		return genai.FinishReason("MAX_TOKENS")
	case "content_filter":
		return genai.FinishReason("SAFETY")
	default:
		return genai.FinishReason("STOP")
	}
}

func safeOpenAIChatStatusError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code any    `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	code := "provider_error"
	if json.Unmarshal(body, &envelope) == nil {
		candidate := envelope.Error.Type
		if stringCode, ok := envelope.Error.Code.(string); ok && stringCode != "" {
			candidate = stringCode
		}
		if providerErrorCodePattern.MatchString(candidate) {
			code = candidate
		}
	}
	return fmt.Errorf("chat completions provider returned HTTP %d (%s)", status, code)
}

func requireOpenAIChatJSONEOF(decoder *json.Decoder) error {
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
