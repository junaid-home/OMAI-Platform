package modelrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestOpenAIChatModelRoundTrip(t *testing.T) {
	t.Parallel()

	const apiKey = "deepseek-test-key-not-a-real-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Error("provider authorization was not applied")
		}
		var input openAIChatRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if input.Model != "deepseek-v4-flash" || input.Stream || len(input.Messages) != 2 {
			t.Errorf("request = %#v", input)
		}
		if len(input.Tools) != 1 || input.Tools[0].Function.Name != "workspace_read" {
			t.Errorf("tools = %#v", input.Tools)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
  "model":"deepseek-v4-flash-202608",
  "choices":[{"finish_reason":"stop","message":{"content":"OMAI_DEEPSEEK_ACP_OK","reasoning_content":""}}],
  "usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_cache_hit_tokens":3}
}`))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	llm, err := newOpenAIChatModel("deepseek-v4-flash", server.URL, apiKey, client)
	if err != nil {
		t.Fatal(err)
	}
	request := &model.LLMRequest{
		Model:    "deepseek-v4-flash",
		Contents: []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "Return the sentinel."}}}},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("Be exact.", genai.RoleUser),
			MaxOutputTokens:   64,
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: "workspace_read", Description: "Read a workspace file",
				ParametersJsonSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}}}},
		},
	}
	var result *model.LLMResponse
	for response, responseErr := range llm.GenerateContent(context.Background(), request, true) {
		if responseErr != nil {
			t.Fatal(responseErr)
		}
		result = response
	}
	if result == nil || result.Content == nil || len(result.Content.Parts) != 1 {
		t.Fatalf("response = %#v", result)
	}
	if result.Content.Parts[0].Text != "OMAI_DEEPSEEK_ACP_OK" || !result.TurnComplete {
		t.Fatalf("response = %#v", result)
	}
	if result.UsageMetadata == nil || result.UsageMetadata.TotalTokenCount != 17 || result.UsageMetadata.CachedContentTokenCount != 3 {
		t.Fatalf("usage = %#v", result.UsageMetadata)
	}
}

func TestOpenAIChatModelDoesNotExposeProviderErrorsOrCredentials(t *testing.T) {
	t.Parallel()

	const apiKey = "provider-secret-that-must-never-appear"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"error":{"type":"authentication_error","message":"credential ` + apiKey + ` is invalid"}}`))
	}))
	defer server.Close()

	llm, err := newOpenAIChatModel("deepseek-v4-flash", server.URL, apiKey, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := &model.LLMRequest{
		Model:    "deepseek-v4-flash",
		Contents: []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}}},
	}
	var received error
	for _, responseErr := range llm.GenerateContent(context.Background(), request, false) {
		received = responseErr
	}
	if received == nil {
		t.Fatal("provider error was accepted")
	}
	if strings.Contains(received.Error(), apiKey) || strings.Contains(received.Error(), "credential") {
		t.Fatalf("provider error leaked sensitive upstream detail: %v", received)
	}
	if !strings.Contains(received.Error(), "HTTP 401 (authentication_error)") {
		t.Fatalf("provider error lost its safe status: %v", received)
	}
}

func TestConfigAcceptsOpenAIChatCompletionsProvider(t *testing.T) {
	t.Parallel()

	config := Config{
		SchemaVersion: "1",
		Default:       Route{ProviderID: "deepseek", ModelID: "deepseek-v4-flash"},
		Providers: []Provider{{
			ID: "deepseek", Name: "DeepSeek", Driver: DriverOpenAIChatCompletions,
			APIKeyEnv: "DEEPSEEK_API_KEY", BaseURL: "https://api.deepseek.com",
			DefaultModel: "deepseek-v4-flash", ModelPrefixes: []string{"deepseek-"}, Enabled: true,
		}},
	}
	if err := config.validate(); err != nil {
		t.Fatalf("valid Chat Completions provider was rejected: %v", err)
	}
}
