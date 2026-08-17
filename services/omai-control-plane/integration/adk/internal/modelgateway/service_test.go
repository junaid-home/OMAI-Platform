package modelgateway

import (
	"math"
	"strings"
	"testing"

	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestToPartRejectsAmbiguousContent(t *testing.T) {
	t.Parallel()

	_, _, err := toPart(&uabv1.ModelPart{
		Text:       "hello",
		InlineData: []byte("also data"),
		MimeType:   "text/plain",
	})
	if err == nil {
		t.Fatal("toPart accepted more than one content variant")
	}
}

func TestToGenerationConfigRejectsNonFiniteAndOversizedValues(t *testing.T) {
	t.Parallel()

	nan := float32(math.NaN())
	if _, err := toGenerationConfig(&uabv1.ModelGenerationConfig{Temperature: &nan}, nil); err == nil {
		t.Fatal("toGenerationConfig accepted NaN temperature")
	}
	infinity := float32(math.Inf(1))
	if _, err := toGenerationConfig(&uabv1.ModelGenerationConfig{TopP: &infinity}, nil); err == nil {
		t.Fatal("toGenerationConfig accepted infinite top_p")
	}
	if _, err := toGenerationConfig(&uabv1.ModelGenerationConfig{MaxOutputTokens: maxOutputTokens + 1}, nil); err == nil {
		t.Fatal("toGenerationConfig accepted excessive max_output_tokens")
	}
	if _, err := toGenerationConfig(&uabv1.ModelGenerationConfig{StopSequences: make([]string, maxStopSequences+1)}, nil); err == nil {
		t.Fatal("toGenerationConfig accepted excessive stop sequences")
	}
	if _, err := toGenerationConfig(&uabv1.ModelGenerationConfig{SystemInstruction: strings.Repeat("x", maxInstruction+1)}, nil); err == nil {
		t.Fatal("toGenerationConfig accepted an oversized system instruction")
	}
}

func TestValidateIdentityRejectsPaddedAndControlValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", " tenant", "tenant\n", strings.Repeat("x", maxIdentityBytes+1)} {
		if err := validateIdentity("tenant_id", value); err == nil {
			t.Fatalf("validateIdentity(%q) accepted an unsafe value", value)
		}
	}
	if err := validateIdentity("tenant_id", "tenant-a"); err != nil {
		t.Fatalf("validateIdentity rejected a safe value: %v", err)
	}
}

func TestToGenerationConfigPreservesPresenceAndTools(t *testing.T) {
	t.Parallel()

	temperature := float32(0)
	topP := float32(0.8)
	config, err := toGenerationConfig(&uabv1.ModelGenerationConfig{
		Temperature:       &temperature,
		TopP:              &topP,
		MaxOutputTokens:   512,
		SystemInstruction: "Be precise.",
		StopSequences:     []string{"STOP"},
	}, []*uabv1.ModelTool{{
		Name:            "workspace.read",
		Description:     "Read a file",
		InputSchemaJson: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}})
	if err != nil {
		t.Fatalf("toGenerationConfig() error = %v", err)
	}
	if config.Temperature == nil || *config.Temperature != 0 {
		t.Fatalf("temperature = %v, want explicit zero", config.Temperature)
	}
	if config.TopP == nil || *config.TopP != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", config.TopP)
	}
	if len(config.Tools) != 1 || len(config.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v, want one declaration", config.Tools)
	}
	if config.Tools[0].FunctionDeclarations[0].Name != "workspace.read" {
		t.Fatalf("tool name = %q", config.Tools[0].FunctionDeclarations[0].Name)
	}
}

func TestToEventMapsUsageAndFunctionCall(t *testing.T) {
	t.Parallel()

	event := toEvent(&model.LLMResponse{
		Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID: "call-1", Name: "workspace.read", Args: map[string]any{"path": "README.md"},
			}}},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 4,
			TotalTokenCount:      14,
		},
		ModelVersion: "test-model",
		TurnComplete: true,
	})
	if event.GetKind() != uabv1.ModelGenerateEventKind_MODEL_GENERATE_EVENT_KIND_CONTENT {
		t.Fatalf("kind = %v", event.GetKind())
	}
	if event.GetContent().GetParts()[0].GetFunctionCall().GetName() != "workspace.read" {
		t.Fatalf("event content = %#v", event.GetContent())
	}
	if event.GetUsage().GetTotalTokens() != 14 {
		t.Fatalf("total tokens = %d", event.GetUsage().GetTotalTokens())
	}
}
