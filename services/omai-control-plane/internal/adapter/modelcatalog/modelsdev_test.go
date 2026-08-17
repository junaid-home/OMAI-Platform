package modelcatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestRefreshBuildsAndPersistsNormalizedCatalog(t *testing.T) {
	t.Parallel()

	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{
			"google":{"id":"google","name":"Google","models":{
				"gemini-test":{"id":"gemini-test","name":"Gemini Test","status":"active","last_updated":"2026-08-10","limit":{"context":1000,"input":900,"output":100}}
			}},
			"unused":{"id":"unused","name":"Unused","models":{}}
		}`))
	}))
	defer source.Close()

	output := filepath.Join(t.TempDir(), "nested", "models.json")
	cfg := Config{
		SchemaVersion: "1",
		SourceURL:     source.URL,
		OutputFile:    output,
		MaxBytes:      1 << 20,
		Routes: []Route{{
			SourceProviderID:     "google",
			ProviderID:           "google",
			RuntimeID:            "go-adk",
			AdditionalRuntimeIDs: []string{"opencode"},
			DefaultModel:         "gemini-test",
			ModelPrefixes:        []string{"gemini-"},
			Enabled:              true,
		}},
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	catalog, err := New(cfg).Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	models := catalog.ModelSnapshot()
	if len(models) != 1 || models[0].ID != "gemini-test" || models[0].RuntimeID != "go-adk" || len(models[0].RuntimeIDs) != 2 || models[0].RuntimeIDs[1] != "opencode" {
		t.Fatalf("models = %#v", models)
	}
	if models[0].Limits.Context != 1000 || models[0].Limits.Output != 100 {
		t.Fatalf("limits = %#v", models[0].Limits)
	}

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var snapshot struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("snapshot JSON error = %v", err)
	}
	if snapshot.SchemaVersion != "1" {
		t.Fatalf("schema_version = %q", snapshot.SchemaVersion)
	}
}

func TestBuildMarksDeprecatedModelsUnavailable(t *testing.T) {
	t.Parallel()

	catalog, err := build(map[string]sourceProvider{
		"openai": {
			Name: "OpenAI",
			Models: map[string]sourceModel{
				"old": {Status: "deprecated"},
				"new": {Status: "active"},
			},
		},
	}, []Route{{
		SourceProviderID: "openai",
		ProviderID:       "openai",
		RuntimeID:        "go-adk",
		DefaultModel:     "new",
		AllowAllModels:   true,
		Enabled:          true,
	}}, false)
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	model, err := catalog.GetModel("openai", "old")
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if model.Ready || model.UnavailableReason == "" {
		t.Fatalf("model = %#v", model)
	}
}

func TestBuildRetainsFullMetadataAndCatalogOnlyProviders(t *testing.T) {
	t.Parallel()

	cacheRead := 0.25
	catalog, err := build(map[string]sourceProvider{
		"google": {
			ID: "google", Name: "Google", NPM: "@ai-sdk/google", Env: []string{"GOOGLE_API_KEY"},
			Models: map[string]sourceModel{
				"gemini-test": {
					ID: "gemini-test", Name: "Gemini Test", Description: "test model", Family: "gemini",
					ReleaseDate: "2026-01-01", LastUpdated: "2026-02-01", Reasoning: true,
					StructuredOutput: true, Modalities: domain.ModelModalities{Input: []string{"text", "image"}, Output: []string{"text"}},
					Cost:  &domain.ModelCost{Input: 1, Output: 2, CacheRead: &cacheRead},
					Limit: sourceLimits{Context: 1000000, Output: 64000},
				},
			},
		},
		"anthropic": {
			ID: "anthropic", Name: "Anthropic", Models: map[string]sourceModel{
				"claude-test": {ID: "claude-test", Name: "Claude Test", Limit: sourceLimits{Context: 200000, Output: 32000}},
			},
		},
	}, []Route{{
		SourceProviderID: "google", ProviderID: "google", RuntimeID: "go-adk",
		DefaultModel: "gemini-test", ModelPrefixes: []string{"gemini-"}, Enabled: true,
	}}, true)
	if err != nil {
		t.Fatalf("build() error = %v", err)
	}
	if got := len(catalog.ProviderSnapshot()); got != 2 {
		t.Fatalf("providers = %d, want 2", got)
	}
	model, err := catalog.GetModel("google", "gemini-test")
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if !model.Ready || !model.Reasoning || !model.StructuredOutput || model.Family != "gemini" || model.Cost == nil || model.Cost.CacheRead == nil {
		t.Fatalf("model metadata = %#v", model)
	}
	unrouted, err := catalog.GetModel("anthropic", "claude-test")
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if unrouted.Ready || unrouted.RuntimeID != "" || unrouted.UnavailableReason == "" {
		t.Fatalf("catalog-only model = %#v", unrouted)
	}
}

func TestVendoredModelsDevSnapshot(t *testing.T) {
	cfg, err := LoadFile(filepath.Join("..", "..", "..", "configs", "model-sync.example.json"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	cfg.OutputFile = ""
	catalog, err := New(cfg).LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if got := len(catalog.ProviderSnapshot()); got != 159 {
		t.Fatalf("providers = %d, want 159", got)
	}
	if got := len(catalog.ModelSnapshot()); got != 5634 {
		t.Fatalf("models = %d, want 5634", got)
	}
	model, err := catalog.GetModel("google", "gemini-flash-latest")
	if err != nil {
		t.Fatalf("GetModel() error = %v", err)
	}
	if !model.Ready || model.Limits.Context == 0 || len(model.Modalities.Input) == 0 || len(model.RuntimeIDs) != 2 || model.RuntimeIDs[1] != "opencode" {
		t.Fatalf("vendored model = %#v", model)
	}
	if _, err := catalog.Resolve("opencode", "google", "gemini-flash-latest"); err != nil {
		t.Fatalf("vendored harness model route was not selectable: %v", err)
	}
}
