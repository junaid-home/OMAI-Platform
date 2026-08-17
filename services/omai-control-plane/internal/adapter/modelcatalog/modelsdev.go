package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
)

type Route struct {
	SourceProviderID     string   `json:"source_provider_id"`
	ProviderID           string   `json:"provider_id"`
	ProviderName         string   `json:"provider_name,omitempty"`
	RuntimeID            string   `json:"runtime_id"`
	AdditionalRuntimeIDs []string `json:"additional_runtime_ids,omitempty"`
	DefaultModel         string   `json:"default_model,omitempty"`
	ModelPrefixes        []string `json:"model_prefixes,omitempty"`
	AllowAllModels       bool     `json:"allow_all_models,omitempty"`
	AllowAlphaModels     bool     `json:"allow_alpha_models,omitempty"`
	Enabled              bool     `json:"enabled"`
}

type Config struct {
	SchemaVersion   string  `json:"schema_version"`
	SourceURL       string  `json:"source_url"`
	SourceFile      string  `json:"source_file,omitempty"`
	OutputFile      string  `json:"output_file,omitempty"`
	IncludeUnrouted bool    `json:"include_unrouted,omitempty"`
	RefreshInterval string  `json:"refresh_interval,omitempty"`
	RequestTimeout  string  `json:"request_timeout,omitempty"`
	MaxBytes        int64   `json:"max_bytes,omitempty"`
	Routes          []Route `json:"routes"`

	refreshInterval time.Duration
	requestTimeout  time.Duration
}

type Refresher struct {
	cfg    Config
	client *http.Client
}

type sourceProvider struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	API    string                 `json:"api,omitempty"`
	NPM    string                 `json:"npm,omitempty"`
	Doc    string                 `json:"doc,omitempty"`
	Env    []string               `json:"env"`
	Models map[string]sourceModel `json:"models"`
}

type sourceModel struct {
	ID               string                        `json:"id"`
	Name             string                        `json:"name"`
	Description      string                        `json:"description,omitempty"`
	Family           string                        `json:"family,omitempty"`
	Knowledge        string                        `json:"knowledge,omitempty"`
	Status           string                        `json:"status,omitempty"`
	ReleaseDate      string                        `json:"release_date"`
	LastUpdated      string                        `json:"last_updated"`
	Attachment       bool                          `json:"attachment"`
	Reasoning        bool                          `json:"reasoning"`
	Temperature      bool                          `json:"temperature"`
	ToolCall         bool                          `json:"tool_call"`
	StructuredOutput bool                          `json:"structured_output"`
	OpenWeights      bool                          `json:"open_weights"`
	Interleaved      any                           `json:"interleaved,omitempty"`
	ReasoningOptions []domain.ModelReasoningOption `json:"reasoning_options,omitempty"`
	Modalities       domain.ModelModalities        `json:"modalities"`
	Cost             *domain.ModelCost             `json:"cost,omitempty"`
	Limit            sourceLimits                  `json:"limit"`
	Provider         *domain.ModelProviderOverride `json:"provider,omitempty"`
	Experimental     *domain.ModelExperimental     `json:"experimental,omitempty"`
}

type sourceLimits struct {
	Context int64 `json:"context"`
	Input   int64 `json:"input"`
	Output  int64 `json:"output"`
}

func LoadFile(path string) (Config, error) {
	// #nosec G304 -- path is operator-owned startup configuration, never request data.
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read model sync configuration: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode model sync configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("decode model sync configuration: unexpected trailing JSON value")
	}
	base := filepath.Dir(path)
	if cfg.SourceFile != "" && !filepath.IsAbs(cfg.SourceFile) {
		cfg.SourceFile = filepath.Join(base, cfg.SourceFile)
	}
	if cfg.OutputFile != "" && !filepath.IsAbs(cfg.OutputFile) {
		cfg.OutputFile = filepath.Join(base, cfg.OutputFile)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func New(cfg Config) *Refresher {
	source, _ := url.Parse(cfg.SourceURL)
	return &Refresher{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.requestTimeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("too many model catalog redirects")
				}
				if !sameOrigin(source, request.URL) {
					return errors.New("model catalog redirect changed origin")
				}
				return nil
			},
		},
	}
}

// LoadSnapshot loads the complete vendored models.dev database before any
// network request. Disconnected deployments therefore retain the full model
// metadata and a failed refresh never replaces the last-known-good catalog.
func (r *Refresher) LoadSnapshot() (*application.Catalog, error) {
	if r.cfg.OutputFile != "" {
		catalog, err := application.LoadCatalog(r.cfg.OutputFile)
		if err == nil && len(catalog.ModelSnapshot()) > 0 {
			return catalog, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("cached normalized model catalog is invalid; loading vendored source", "error", err)
		}
	}
	if r.cfg.SourceFile == "" {
		return nil, errors.New("model sync source_file is not configured")
	}
	data, err := readFile(r.cfg.SourceFile, r.cfg.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("read models.dev snapshot: %w", err)
	}
	return r.decode(data)
}

func (r *Refresher) Refresh(ctx context.Context) (*application.Catalog, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.SourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create model catalog request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "omai-model-catalog/2")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch model catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch model catalog: unexpected HTTP status %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, r.cfg.MaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	if int64(len(data)) > r.cfg.MaxBytes {
		return nil, errors.New("model catalog exceeds configured size limit")
	}
	return r.decode(data)
}

func (r *Refresher) decode(data []byte) (*application.Catalog, error) {
	var database map[string]sourceProvider
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&database); err != nil {
		return nil, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode models.dev catalog: unexpected trailing JSON value")
	}
	catalog, err := build(database, r.cfg.Routes, r.cfg.IncludeUnrouted)
	if err != nil {
		return nil, err
	}
	if r.cfg.OutputFile != "" {
		if err := writeCatalog(r.cfg.OutputFile, catalog); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

func (r *Refresher) Run(ctx context.Context, target *application.Catalog, refreshImmediately bool) {
	refresh := func() {
		catalog, err := r.Refresh(ctx)
		if err != nil {
			slog.Warn("model catalog refresh failed; retaining previous snapshot", "error", err)
			return
		}
		target.Replace(catalog)
	}
	if refreshImmediately {
		refresh()
	}
	ticker := time.NewTicker(r.cfg.refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (c *Config) validate() error {
	if c.SchemaVersion != "1" {
		return fmt.Errorf("unsupported model sync schema version %q", c.SchemaVersion)
	}
	parsed, err := url.Parse(c.SourceURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("model source_url must be absolute and contain no credentials, query, or fragment")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return errors.New("model source_url must use HTTPS; HTTP is allowed only on loopback")
	}
	if len(c.Routes) == 0 {
		return errors.New("model sync requires at least one route")
	}
	seenProviders := make(map[string]struct{}, len(c.Routes))
	seenSources := make(map[string]struct{}, len(c.Routes))
	enabledRoutes := 0
	for index, route := range c.Routes {
		if !canonical(route.SourceProviderID) || !canonical(route.ProviderID) || !canonical(route.RuntimeID) {
			return fmt.Errorf("model route %d requires canonical source_provider_id, provider_id, and runtime_id", index)
		}
		if _, exists := seenProviders[route.ProviderID]; exists {
			return fmt.Errorf("duplicate model route provider_id %q", route.ProviderID)
		}
		if _, exists := seenSources[route.SourceProviderID]; exists {
			return fmt.Errorf("duplicate model route source_provider_id %q", route.SourceProviderID)
		}
		seenProviders[route.ProviderID] = struct{}{}
		seenSources[route.SourceProviderID] = struct{}{}
		if route.ProviderName != strings.TrimSpace(route.ProviderName) || route.DefaultModel != strings.TrimSpace(route.DefaultModel) {
			return fmt.Errorf("model route %q has non-canonical provider_name or default_model", route.ProviderID)
		}
		seenRuntimes := map[string]struct{}{route.RuntimeID: {}}
		for _, runtimeID := range route.AdditionalRuntimeIDs {
			if !canonical(runtimeID) {
				return fmt.Errorf("model route %q has an invalid additional runtime_id", route.ProviderID)
			}
			if _, exists := seenRuntimes[runtimeID]; exists {
				return fmt.Errorf("model route %q has duplicate runtime_id %q", route.ProviderID, runtimeID)
			}
			seenRuntimes[runtimeID] = struct{}{}
		}
		for _, prefix := range route.ModelPrefixes {
			if !canonical(prefix) || len(prefix) > 128 {
				return fmt.Errorf("model route %q has an invalid model prefix", route.ProviderID)
			}
		}
		if route.Enabled {
			enabledRoutes++
			if route.DefaultModel == "" {
				return fmt.Errorf("enabled model route %q requires default_model", route.ProviderID)
			}
			if !route.AllowAllModels && len(route.ModelPrefixes) == 0 {
				return fmt.Errorf("enabled model route %q requires model_prefixes or allow_all_models", route.ProviderID)
			}
		}
	}
	if enabledRoutes == 0 {
		return errors.New("model sync requires at least one enabled route")
	}
	c.refreshInterval = time.Hour
	if c.RefreshInterval != "" {
		value, err := time.ParseDuration(c.RefreshInterval)
		if err != nil || value < 5*time.Minute || value > 24*time.Hour {
			return errors.New("refresh_interval must be between 5m and 24h")
		}
		c.refreshInterval = value
	}
	c.requestTimeout = 15 * time.Second
	if c.RequestTimeout != "" {
		value, err := time.ParseDuration(c.RequestTimeout)
		if err != nil || value < time.Second || value > time.Minute {
			return errors.New("request_timeout must be between 1s and 1m")
		}
		c.requestTimeout = value
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 64 << 20
	}
	if c.MaxBytes < 1<<20 || c.MaxBytes > 128<<20 {
		return errors.New("max_bytes must be between 1 MiB and 128 MiB")
	}
	return nil
}

func build(database map[string]sourceProvider, routes []Route, includeUnrouted bool) (*application.Catalog, error) {
	if len(database) == 0 {
		return nil, errors.New("models.dev catalog is empty")
	}
	if len(database) > 4096 {
		return nil, errors.New("models.dev catalog exceeds provider limit")
	}
	routesBySource := make(map[string]Route, len(routes))
	for _, route := range routes {
		routesBySource[route.SourceProviderID] = route
		if _, ok := database[route.SourceProviderID]; !ok && route.Enabled {
			return nil, fmt.Errorf("models.dev provider %q was not found", route.SourceProviderID)
		}
	}
	providerKeys := make([]string, 0, len(database))
	for key := range database {
		providerKeys = append(providerKeys, key)
	}
	sort.Strings(providerKeys)

	var providers []domain.Provider
	var models []domain.Model
	defaults := make(map[string]string)
	for _, sourceKey := range providerKeys {
		source := database[sourceKey]
		route, routed := routesBySource[sourceKey]
		if (!routed || !route.Enabled) && !includeUnrouted {
			continue
		}
		sourceID := source.ID
		if sourceID == "" {
			sourceID = sourceKey
		}
		if !canonical(sourceID) || len(sourceID) > 128 || len(source.Name) > 1024 {
			return nil, fmt.Errorf("models.dev provider %q has an invalid id or name", sourceKey)
		}
		providerID := sourceID
		providerName := strings.TrimSpace(source.Name)
		runtimeID := ""
		var runtimeIDs []string
		connected := false
		if routed {
			providerID = route.ProviderID
			if route.ProviderName != "" {
				providerName = route.ProviderName
			}
			if route.Enabled {
				runtimeID = route.RuntimeID
				runtimeIDs = route.runtimeIDs()
				connected = true
			}
		}
		providers = append(providers, domain.Provider{
			ID: providerID, SourceID: sourceID, Name: providerName, API: source.API,
			NPM: source.NPM, Doc: source.Doc, Env: append([]string(nil), source.Env...),
			Connected: connected, RuntimeID: runtimeID, RuntimeIDs: runtimeIDs,
		})
		modelKeys := make([]string, 0, len(source.Models))
		for key := range source.Models {
			modelKeys = append(modelKeys, key)
		}
		sort.Strings(modelKeys)
		for _, key := range modelKeys {
			if len(models) == 100000 {
				return nil, errors.New("models.dev catalog exceeds model limit")
			}
			model, err := normalizeModel(sourceID, providerID, runtimeID, runtimeIDs, source.Models[key], key, route, connected)
			if err != nil {
				return nil, err
			}
			models = append(models, model)
		}
		if connected {
			defaults[providerID] = route.DefaultModel
		}
	}
	if len(models) == 0 {
		return nil, errors.New("models.dev catalog produced no models")
	}
	return application.NewCatalogWithDefaults(providers, models, defaults)
}

func normalizeModel(sourceProviderID, providerID, runtimeID string, runtimeIDs []string, source sourceModel, key string, route Route, connected bool) (domain.Model, error) {
	modelID := source.ID
	if modelID == "" {
		modelID = key
	}
	modelName := source.Name
	if modelName == "" {
		modelName = modelID
	}
	modelName = strings.TrimSpace(modelName)
	ready, reason := modelReadiness(source.Status, modelID, route, connected)
	free := source.Cost != nil && source.Cost.Input == 0 && source.Cost.Output == 0
	model := domain.Model{
		ID: modelID, Name: modelName, Description: source.Description, Family: source.Family,
		Knowledge: source.Knowledge, ProviderID: providerID, SourceProviderID: sourceProviderID,
		RuntimeID: runtimeID, RuntimeIDs: append([]string(nil), runtimeIDs...), Ready: ready, Free: free, UnavailableReason: reason,
		Status: source.Status, ReleaseDate: source.ReleaseDate, LastUpdated: source.LastUpdated,
		Attachment: source.Attachment, Reasoning: source.Reasoning, Temperature: source.Temperature,
		ToolCall: source.ToolCall, StructuredOutput: source.StructuredOutput, OpenWeights: source.OpenWeights,
		Interleaved: source.Interleaved, ReasoningOptions: source.ReasoningOptions,
		Modalities: source.Modalities, Cost: source.Cost, Provider: source.Provider,
		Experimental: source.Experimental,
		Limits:       domain.ModelLimits{Context: source.Limit.Context, Input: source.Limit.Input, Output: source.Limit.Output},
	}
	if err := validateModel(model); err != nil {
		return domain.Model{}, fmt.Errorf("models.dev model %s/%s: %w", sourceProviderID, modelID, err)
	}
	return model, nil
}

func (r Route) runtimeIDs() []string {
	result := make([]string, 0, 1+len(r.AdditionalRuntimeIDs))
	if r.RuntimeID != "" {
		result = append(result, r.RuntimeID)
	}
	return append(result, r.AdditionalRuntimeIDs...)
}

func modelReadiness(status, modelID string, route Route, connected bool) (bool, string) {
	if !connected {
		return false, "provider is catalog-only; configure and enable an OMAI ADK route"
	}
	if status == "deprecated" {
		return false, "model is deprecated in models.dev"
	}
	if status == "alpha" && !route.AllowAlphaModels {
		return false, "alpha model is disabled by OMAI provider policy"
	}
	if !route.allows(modelID) {
		return false, "model is outside the configured OMAI ADK allowlist"
	}
	return true, ""
}

func (r Route) allows(modelID string) bool {
	if r.AllowAllModels || modelID == r.DefaultModel {
		return true
	}
	for _, prefix := range r.ModelPrefixes {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
}

func validateModel(model domain.Model) error {
	if !canonical(model.ID) || !canonical(model.Name) || len(model.ID) > 512 || len(model.Name) > 1024 {
		return errors.New("id and name must be canonical")
	}
	if len(model.Description) > 65536 || len(model.ReasoningOptions) > 32 {
		return errors.New("description or reasoning options exceed catalog limits")
	}
	if model.Limits.Context < 0 || model.Limits.Input < 0 || model.Limits.Output < 0 {
		return errors.New("token limits must be non-negative")
	}
	switch model.Status {
	case "", "active", "alpha", "beta", "deprecated":
	default:
		return fmt.Errorf("unsupported status %q", model.Status)
	}
	for _, option := range model.ReasoningOptions {
		switch option.Type {
		case "effort", "toggle", "budget_tokens":
		default:
			return fmt.Errorf("unsupported reasoning option %q", option.Type)
		}
	}
	for _, modality := range append(append([]string(nil), model.Modalities.Input...), model.Modalities.Output...) {
		switch modality {
		case "text", "audio", "image", "video", "pdf":
		default:
			return fmt.Errorf("unsupported modality %q", modality)
		}
	}
	return nil
}

func writeCatalog(path string, catalog *application.Catalog) error {
	document := struct {
		SchemaVersion string            `json:"schema_version"`
		Providers     []domain.Provider `json:"providers"`
		Models        []domain.Model    `json:"models"`
		Defaults      map[string]string `json:"default,omitempty"`
	}{
		SchemaVersion: "1",
		Providers:     catalog.ProviderSnapshot(),
		Models:        catalog.ModelSnapshot(),
		Defaults:      catalog.DefaultSnapshot(),
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model catalog snapshot: %w", err)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create model catalog directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".omai-models-*.tmp")
	if err != nil {
		return fmt.Errorf("create model catalog snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace model catalog snapshot: %w", err)
	}
	return nil
}

func readFile(path string, maxBytes int64) ([]byte, error) {
	// #nosec G304 -- path comes from the operator-owned sync configuration.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file exceeds configured size limit")
	}
	return data, nil
}

func canonical(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
