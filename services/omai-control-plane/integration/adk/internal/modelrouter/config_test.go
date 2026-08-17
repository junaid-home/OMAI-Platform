package modelrouter

import (
	"context"
	"iter"
	"net/http"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
)

func TestResolveUsesDefaultAndRejectsPartialSelection(t *testing.T) {
	cfg := Config{
		SchemaVersion: "1",
		Default:       Route{ProviderID: "openrouter", ModelID: "anthropic/claude-sonnet"},
		Providers: []Provider{{
			ID: "openrouter", Name: "OpenRouter", Driver: DriverOpenAIResponses,
			APIKeyEnv: "OPENROUTER_API_KEY", BaseURL: "https://openrouter.ai/api/v1",
			DefaultModel: "anthropic/claude-sonnet", AllowAllModels: true, Enabled: true,
		}},
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	router := New(cfg)
	resolved, err := router.Resolve("", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != cfg.Default {
		t.Fatalf("resolved route = %#v, want %#v", resolved, cfg.Default)
	}
	if _, err := router.Resolve("openrouter", ""); err == nil {
		t.Fatal("partial route must fail")
	}
	ctx := WithRoute(context.Background(), "openrouter", "openai/gpt-5")
	route, ok := ctx.Value(routeContextKey{}).(Route)
	if !ok || route.ModelID != "openai/gpt-5" {
		t.Fatalf("route context = %#v", route)
	}
}

func TestConfigRejectsInsecureRemoteEndpoint(t *testing.T) {
	cfg := Config{
		SchemaVersion: "1",
		Default:       Route{ProviderID: "remote", ModelID: "model"},
		Providers: []Provider{{
			ID: "remote", Name: "Remote", Driver: DriverOpenAIResponses,
			APIKeyEnv: "REMOTE_API_KEY", BaseURL: "http://example.com/v1",
			DefaultModel: "model", Enabled: true,
		}},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("insecure remote endpoint must fail")
	}
}

func TestRouterEvictsLeastRecentlyUsedModel(t *testing.T) {
	router := &Router{models: map[string]cacheEntry{
		"old":    {lastUsed: 1},
		"recent": {lastUsed: 3},
	}}
	router.evictLeastRecentlyUsed()
	if _, exists := router.models["old"]; exists {
		t.Fatal("least recently used entry was retained")
	}
	if _, exists := router.models["recent"]; !exists {
		t.Fatal("recent entry was evicted")
	}
}

func TestRouterReusesProviderHTTPClient(t *testing.T) {
	router := New(Config{MaxCachedModels: 4})
	provider := Provider{ID: "provider", RequestTimeout: 2 * time.Minute}
	first, err := router.providerHTTPClient(provider)
	if err != nil {
		t.Fatalf("providerHTTPClient() error = %v", err)
	}
	second, err := router.providerHTTPClient(provider)
	if err != nil {
		t.Fatalf("providerHTTPClient() second error = %v", err)
	}
	if first != second {
		t.Fatal("provider HTTP client was not reused")
	}
}

func TestRouterForwardsResolvedModelWithoutMutatingCaller(t *testing.T) {
	provider := &recordingModel{}
	router := &Router{
		defaultRoute: Route{ProviderID: "provider", ModelID: "selected-model"},
		providers:    map[string]Provider{"provider": {ID: "provider", Enabled: true, AllowAllModels: true}},
		maxModels:    4,
		models:       map[string]cacheEntry{"provider\x00selected-model": {model: provider}},
		clients:      make(map[string]*http.Client),
	}
	request := &model.LLMRequest{Model: router.Name()}
	for _, err := range router.GenerateContent(context.Background(), request, true) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if provider.model != "selected-model" {
		t.Fatalf("provider received model %q", provider.model)
	}
	if request.Model != router.Name() {
		t.Fatalf("caller request was mutated to %q", request.Model)
	}
}

type recordingModel struct{ model string }

func (*recordingModel) Name() string { return "recording" }
func (m *recordingModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.model = request.Model
	return func(yield func(*model.LLMResponse, error) bool) { yield(&model.LLMResponse{}, nil) }
}
