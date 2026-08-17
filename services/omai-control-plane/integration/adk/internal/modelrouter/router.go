package modelrouter

import (
	"context"
	"crypto/tls"
	"fmt"
	"iter"
	"net/http"
	"os"
	"strings"
	"sync"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

type routeContextKey struct{}

type Router struct {
	defaultRoute Route
	providers    map[string]Provider
	maxModels    int

	mu      sync.Mutex
	clock   uint64
	models  map[string]cacheEntry
	clients map[string]*http.Client
}

type cacheEntry struct {
	model    model.LLM
	lastUsed uint64
}

func New(cfg Config) *Router {
	maxModels := cfg.MaxCachedModels
	if maxModels <= 0 {
		maxModels = 256
	}
	providers := make(map[string]Provider, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		if provider.Enabled {
			providers[provider.ID] = provider
		}
	}
	return &Router{
		defaultRoute: cfg.Default,
		providers:    providers,
		maxModels:    maxModels,
		models:       make(map[string]cacheEntry),
		clients:      make(map[string]*http.Client),
	}
}

func WithRoute(ctx context.Context, providerID, modelID string) context.Context {
	return context.WithValue(ctx, routeContextKey{}, Route{ProviderID: providerID, ModelID: modelID})
}

func (r *Router) Name() string { return "omai-model-router" }

func (r *Router) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	route, err := r.route(ctx)
	if err != nil {
		return errorSequence(err)
	}
	providerModel, err := r.model(ctx, route)
	if err != nil {
		return errorSequence(err)
	}
	if request == nil {
		return providerModel.GenerateContent(ctx, nil, stream)
	}
	forwarded := *request
	forwarded.Model = route.ModelID
	return providerModel.GenerateContent(ctx, &forwarded, stream)
}

func (r *Router) Ready() (bool, bool, string) {
	provider, ok := r.providers[r.defaultRoute.ProviderID]
	if !ok {
		return false, false, "default provider is unavailable"
	}
	if provider.AllowAnonymous {
		return true, true, ""
	}
	if strings.TrimSpace(os.Getenv(provider.APIKeyEnv)) == "" {
		return false, false, "default provider credential is not configured"
	}
	return true, true, ""
}

func (r *Router) Resolve(providerID, modelID string) (Route, error) {
	if providerID == "" && modelID == "" {
		return r.defaultRoute, nil
	}
	if providerID == "" || modelID == "" {
		return Route{}, fmt.Errorf("provider_id and model_id must be supplied together")
	}
	provider, ok := r.providers[providerID]
	if !ok {
		return Route{}, fmt.Errorf("provider %q is not enabled", providerID)
	}
	if !provider.allows(modelID) {
		return Route{}, fmt.Errorf("model %q is not allowed for provider %q", modelID, providerID)
	}
	return Route{ProviderID: providerID, ModelID: modelID}, nil
}

func (r *Router) route(ctx context.Context) (Route, error) {
	route, _ := ctx.Value(routeContextKey{}).(Route)
	return r.Resolve(route.ProviderID, route.ModelID)
}

func (r *Router) model(ctx context.Context, resolved Route) (model.LLM, error) {
	cacheKey := resolved.ProviderID + "\x00" + resolved.ModelID
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock++
	if cached, ok := r.models[cacheKey]; ok {
		cached.lastUsed = r.clock
		r.models[cacheKey] = cached
		return cached.model, nil
	}
	provider := r.providers[resolved.ProviderID]
	created, err := r.createModel(ctx, provider, resolved.ModelID)
	if err != nil {
		return nil, err
	}
	if len(r.models) >= r.maxModels {
		r.evictLeastRecentlyUsed()
	}
	r.models[cacheKey] = cacheEntry{model: created, lastUsed: r.clock}
	return created, nil
}

func (r *Router) evictLeastRecentlyUsed() {
	var oldestKey string
	var oldestUse uint64
	for key, entry := range r.models {
		if oldestKey == "" || entry.lastUsed < oldestUse {
			oldestKey = key
			oldestUse = entry.lastUsed
		}
	}
	if oldestKey != "" {
		delete(r.models, oldestKey)
	}
}

func (r *Router) createModel(ctx context.Context, provider Provider, modelID string) (model.LLM, error) {
	apiKey := strings.TrimSpace(os.Getenv(provider.APIKeyEnv))
	if !provider.AllowAnonymous && apiKey == "" {
		return nil, fmt.Errorf("provider %q credential is not configured", provider.ID)
	}
	httpClient, err := r.providerHTTPClient(provider)
	if err != nil {
		return nil, err
	}
	switch provider.Driver {
	case DriverGemini:
		timeout := provider.RequestTimeout
		created, err := gemini.NewModel(ctx, modelID, &genai.ClientConfig{
			APIKey:     apiKey,
			Backend:    genai.BackendGeminiAPI,
			HTTPClient: httpClient,
			HTTPOptions: genai.HTTPOptions{
				Timeout: &timeout,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("create gemini model for provider %q: %w", provider.ID, err)
		}
		return created, nil
	case DriverOpenAIResponses:
		created, err := openaimodel.NewModel(ctx, modelID, &openaimodel.ClientConfig{
			APIKey:     apiKey,
			BaseURL:    provider.BaseURL,
			HTTPClient: httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("create OpenAI Responses model for provider %q: %w", provider.ID, err)
		}
		return created, nil
	case DriverOpenAIChatCompletions:
		created, err := newOpenAIChatModel(modelID, provider.BaseURL, apiKey, httpClient)
		if err != nil {
			return nil, fmt.Errorf("create OpenAI Chat Completions model for provider %q: %w", provider.ID, err)
		}
		return created, nil
	default:
		return nil, fmt.Errorf("provider %q uses an unsupported driver", provider.ID)
	}
}

// providerHTTPClient is called while r.mu is held. A provider owns one pooled
// transport regardless of how many model handles are present in the LRU cache.
func (r *Router) providerHTTPClient(provider Provider) (*http.Client, error) {
	if client := r.clients[provider.ID]; client != nil {
		return client, nil
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("create HTTP transport for provider %q: unsupported default transport %T", provider.ID, http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	client := &http.Client{Transport: transport, Timeout: provider.RequestTimeout}
	r.clients[provider.ID] = client
	return client, nil
}

func errorSequence(err error) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, err)
	}
}
