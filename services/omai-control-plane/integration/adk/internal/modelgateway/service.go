package modelgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/integration/adk/internal/modelrouter"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

const (
	maxContents      = 256
	maxParts         = 2048
	maxTools         = 512
	maxInlineBytes   = 20 << 20
	maxJSONBytes     = 1 << 20
	maxTotalDataSize = 32 << 20
	maxIdentityBytes = 256
	maxOutputTokens  = 131_072
	maxStopSequences = 64
	maxStopBytes     = 4 << 10
	maxInstruction   = 1 << 20
)

var functionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)

type Service struct {
	router  *modelrouter.Router
	version string
}

func New(router *modelrouter.Router, version string) *Service {
	return &Service{router: router, version: version}
}

func (s *Service) Health(context.Context, *connect.Request[uabv1.ModelGatewayHealthRequest]) (*connect.Response[uabv1.ModelGatewayHealthResponse], error) {
	available, authenticated, reason := s.router.Ready()
	return connect.NewResponse(&uabv1.ModelGatewayHealthResponse{
		Available:     available,
		Authenticated: authenticated,
		Version:       s.version,
		Reason:        reason,
	}), nil
}

func (s *Service) Generate(ctx context.Context, request *connect.Request[uabv1.ModelGenerateRequest], stream *connect.ServerStream[uabv1.ModelGenerateEvent]) error {
	message := request.Msg
	for name, value := range map[string]string{
		"tenant_id":  message.GetTenantId(),
		"actor_id":   message.GetActorId(),
		"session_id": message.GetSessionId(),
	} {
		if err := validateIdentity(name, value); err != nil {
			return invalidArgument(err.Error())
		}
	}
	route, err := s.router.Resolve(message.GetProviderId(), message.GetModelId())
	if err != nil {
		return invalidArgument(err.Error())
	}
	contents, err := toContents(message.GetContents())
	if err != nil {
		return invalidArgument(err.Error())
	}
	config, err := toGenerationConfig(message.GetConfig(), message.GetTools())
	if err != nil {
		return invalidArgument(err.Error())
	}

	ctx = modelrouter.WithRoute(ctx, route.ProviderID, route.ModelID)
	responses := s.router.GenerateContent(ctx, &model.LLMRequest{
		Model:    route.ModelID,
		Contents: contents,
		Config:   config,
	}, true)
	for response, responseErr := range responses {
		if responseErr != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return connect.NewError(connect.CodeCanceled, context.Canceled)
			}
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("model generation failed: %w", responseErr))
		}
		if response == nil {
			continue
		}
		event := toEvent(response)
		event.ProviderId = route.ProviderID
		event.ModelId = route.ModelID
		if err := stream.Send(event); err != nil {
			return err
		}
		if response.ErrorCode != "" {
			return nil
		}
	}
	return stream.Send(&uabv1.ModelGenerateEvent{
		Kind:       uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_DONE,
		ProviderId: route.ProviderID,
		ModelId:    route.ModelID,
		UnixMillis: time.Now().UnixMilli(),
	})
}

func toContents(source []*uabv1.ModelContent) ([]*genai.Content, error) {
	if len(source) == 0 || len(source) > maxContents {
		return nil, fmt.Errorf("contents must contain between 1 and %d messages", maxContents)
	}
	result := make([]*genai.Content, 0, len(source))
	partCount := 0
	totalDataSize := 0
	for contentIndex, content := range source {
		if content == nil {
			return nil, fmt.Errorf("contents[%d] is required", contentIndex)
		}
		role := content.GetRole()
		if role != genai.RoleUser && role != genai.RoleModel {
			return nil, fmt.Errorf("contents[%d].role must be user or model", contentIndex)
		}
		if len(content.GetParts()) == 0 {
			return nil, fmt.Errorf("contents[%d].parts must not be empty", contentIndex)
		}
		parts := make([]*genai.Part, 0, len(content.GetParts()))
		for partIndex, part := range content.GetParts() {
			partCount++
			if partCount > maxParts {
				return nil, fmt.Errorf("request exceeds the %d-part limit", maxParts)
			}
			converted, dataSize, err := toPart(part)
			if err != nil {
				return nil, fmt.Errorf("contents[%d].parts[%d]: %w", contentIndex, partIndex, err)
			}
			totalDataSize += dataSize
			if totalDataSize > maxTotalDataSize {
				return nil, fmt.Errorf("request data exceeds %d bytes", maxTotalDataSize)
			}
			parts = append(parts, converted)
		}
		result = append(result, &genai.Content{Role: role, Parts: parts})
	}
	return result, nil
}

func toPart(source *uabv1.ModelPart) (*genai.Part, int, error) {
	if source == nil {
		return nil, 0, errors.New("part is required")
	}
	variants := 0
	if source.GetText() != "" {
		variants++
	}
	if len(source.GetInlineData()) != 0 {
		variants++
	}
	if source.GetFunctionCall() != nil {
		variants++
	}
	if source.GetFunctionResult() != nil {
		variants++
	}
	if variants != 1 {
		return nil, 0, errors.New("exactly one of text, inline_data, function_call, or function_result is required")
	}
	result := &genai.Part{
		Thought:          source.GetThought(),
		ThoughtSignature: append([]byte(nil), source.GetThoughtSignature()...),
	}
	if source.GetText() != "" {
		result.Text = source.GetText()
		return result, len(source.GetText()) + len(source.GetThoughtSignature()), nil
	}
	if data := source.GetInlineData(); len(data) != 0 {
		if source.GetMimeType() == "" {
			return nil, 0, errors.New("mime_type is required for inline_data")
		}
		if len(data) > maxInlineBytes {
			return nil, 0, fmt.Errorf("inline_data exceeds %d bytes", maxInlineBytes)
		}
		result.InlineData = &genai.Blob{Data: append([]byte(nil), data...), MIMEType: source.GetMimeType()}
		return result, len(data) + len(source.GetThoughtSignature()), nil
	}
	if call := source.GetFunctionCall(); call != nil {
		if !functionNamePattern.MatchString(call.GetName()) {
			return nil, 0, errors.New("function_call.name is invalid")
		}
		arguments, err := parseJSONObject(call.GetArgumentsJson(), "function_call.arguments_json", false)
		if err != nil {
			return nil, 0, err
		}
		result.FunctionCall = &genai.FunctionCall{ID: call.GetId(), Name: call.GetName(), Args: arguments}
		return result, len(call.GetArgumentsJson()) + len(source.GetThoughtSignature()), nil
	}
	response := source.GetFunctionResult()
	if !functionNamePattern.MatchString(response.GetName()) {
		return nil, 0, errors.New("function_result.name is invalid")
	}
	value, err := parseJSONObject(response.GetResultJson(), "function_result.result_json", false)
	if err != nil {
		return nil, 0, err
	}
	result.FunctionResponse = &genai.FunctionResponse{
		ID:       response.GetId(),
		Name:     response.GetName(),
		Response: value,
	}
	return result, len(response.GetResultJson()) + len(source.GetThoughtSignature()), nil
}

func toGenerationConfig(source *uabv1.ModelGenerationConfig, tools []*uabv1.ModelTool) (*genai.GenerateContentConfig, error) {
	config := &genai.GenerateContentConfig{}
	if source != nil {
		if source.Temperature != nil {
			value := source.GetTemperature()
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > 2 {
				return nil, errors.New("config.temperature must be between 0 and 2")
			}
			config.Temperature = &value
		}
		if source.TopP != nil {
			value := source.GetTopP()
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value < 0 || value > 1 {
				return nil, errors.New("config.top_p must be between 0 and 1")
			}
			config.TopP = &value
		}
		if source.GetMaxOutputTokens() < 0 || source.GetMaxOutputTokens() > maxOutputTokens {
			return nil, fmt.Errorf("config.max_output_tokens must be between 0 and %d", maxOutputTokens)
		}
		config.MaxOutputTokens = source.GetMaxOutputTokens()
		if len(source.GetStopSequences()) > maxStopSequences {
			return nil, fmt.Errorf("config.stop_sequences exceeds the %d-sequence limit", maxStopSequences)
		}
		for index, sequence := range source.GetStopSequences() {
			if len(sequence) > maxStopBytes {
				return nil, fmt.Errorf("config.stop_sequences[%d] exceeds %d bytes", index, maxStopBytes)
			}
		}
		config.StopSequences = append([]string(nil), source.GetStopSequences()...)
		if len(source.GetSystemInstruction()) > maxInstruction {
			return nil, fmt.Errorf("config.system_instruction exceeds %d bytes", maxInstruction)
		}
		if instruction := strings.TrimSpace(source.GetSystemInstruction()); instruction != "" {
			config.SystemInstruction = genai.NewContentFromText(instruction, genai.RoleUser)
		}
	}
	declarations, err := toToolDeclarations(tools)
	if err != nil {
		return nil, err
	}
	if len(declarations) != 0 {
		config.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	}
	return config, nil
}

func validateIdentity(name, value string) error {
	if value == "" || len(value) > maxIdentityBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must contain between 1 and %d non-padded bytes", name, maxIdentityBytes)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func toToolDeclarations(source []*uabv1.ModelTool) ([]*genai.FunctionDeclaration, error) {
	if len(source) > maxTools {
		return nil, fmt.Errorf("tools exceeds the %d-tool limit", maxTools)
	}
	seen := make(map[string]struct{}, len(source))
	result := make([]*genai.FunctionDeclaration, 0, len(source))
	for index, tool := range source {
		if tool == nil || !functionNamePattern.MatchString(tool.GetName()) {
			return nil, fmt.Errorf("tools[%d].name is invalid", index)
		}
		if _, exists := seen[tool.GetName()]; exists {
			return nil, fmt.Errorf("tools[%d].name is duplicated", index)
		}
		seen[tool.GetName()] = struct{}{}
		inputSchema, err := parseJSONObject(tool.GetInputSchemaJson(), fmt.Sprintf("tools[%d].input_schema_json", index), true)
		if err != nil {
			return nil, err
		}
		declaration := &genai.FunctionDeclaration{
			Name:                 tool.GetName(),
			Description:          tool.GetDescription(),
			ParametersJsonSchema: inputSchema,
		}
		if len(tool.GetOutputSchemaJson()) != 0 {
			outputSchema, err := parseJSONObject(tool.GetOutputSchemaJson(), fmt.Sprintf("tools[%d].output_schema_json", index), false)
			if err != nil {
				return nil, err
			}
			declaration.ResponseJsonSchema = outputSchema
		}
		result = append(result, declaration)
	}
	return result, nil
}

func parseJSONObject(raw []byte, field string, defaultObject bool) (map[string]any, error) {
	if len(raw) == 0 {
		if defaultObject {
			return map[string]any{"type": "object", "properties": map[string]any{}}, nil
		}
		return map[string]any{}, nil
	}
	if len(raw) > maxJSONBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", field, maxJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", field, err)
	}
	if value == nil {
		return nil, fmt.Errorf("%s must be a JSON object", field)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s must contain one JSON object: %w", field, err)
	}
	return value, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
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

func toEvent(response *model.LLMResponse) *uabv1.ModelGenerateEvent {
	kind := uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT
	if response.ErrorCode != "" {
		kind = uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_ERROR
	}
	event := &uabv1.ModelGenerateEvent{
		Kind:         kind,
		Content:      toProtoContent(response.Content),
		ModelVersion: response.ModelVersion,
		FinishReason: string(response.FinishReason),
		Partial:      response.Partial,
		TurnComplete: response.TurnComplete,
		ErrorCode:    response.ErrorCode,
		ErrorMessage: response.ErrorMessage,
		UnixMillis:   time.Now().UnixMilli(),
	}
	if usage := response.UsageMetadata; usage != nil {
		event.Usage = &uabv1.ModelUsage{
			InputTokens:       usage.PromptTokenCount,
			OutputTokens:      usage.CandidatesTokenCount,
			TotalTokens:       usage.TotalTokenCount,
			CachedInputTokens: usage.CachedContentTokenCount,
			ThoughtTokens:     usage.ThoughtsTokenCount,
		}
	}
	return event
}

func toProtoContent(source *genai.Content) *uabv1.ModelContent {
	if source == nil {
		return nil
	}
	result := &uabv1.ModelContent{Role: source.Role}
	for _, part := range source.Parts {
		if converted := toProtoPart(part); converted != nil {
			result.Parts = append(result.Parts, converted)
		}
	}
	return result
}

func toProtoPart(source *genai.Part) *uabv1.ModelPart {
	if source == nil {
		return nil
	}
	result := &uabv1.ModelPart{
		Thought:          source.Thought,
		ThoughtSignature: append([]byte(nil), source.ThoughtSignature...),
	}
	switch {
	case source.Text != "":
		result.Text = source.Text
	case source.InlineData != nil:
		result.InlineData = append([]byte(nil), source.InlineData.Data...)
		result.MimeType = source.InlineData.MIMEType
	case source.FunctionCall != nil:
		result.FunctionCall = &uabv1.ModelFunctionCall{
			Id:            source.FunctionCall.ID,
			Name:          source.FunctionCall.Name,
			ArgumentsJson: mustJSON(source.FunctionCall.Args),
		}
	case source.FunctionResponse != nil:
		result.FunctionResult = &uabv1.ModelFunctionResult{
			Id:         source.FunctionResponse.ID,
			Name:       source.FunctionResponse.Name,
			ResultJson: mustJSON(source.FunctionResponse.Response),
		}
	default:
		return nil
	}
	return result
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

func invalidArgument(message string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(message))
}
