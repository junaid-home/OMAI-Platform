package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/omai/backend/internal/domain"
)

type Catalog struct {
	mu sync.RWMutex

	SchemaVersion string            `json:"schema_version"`
	Providers     []domain.Provider `json:"providers"`
	Models        []domain.Model    `json:"models"`
	Defaults      map[string]string `json:"default,omitempty"`
	modelIndexes  map[string]int
	modelIDs      map[string][]int
}

type ModelPage struct {
	Models     []domain.Model
	Total      int
	Offset     int
	NextOffset int
}

func LoadCatalog(path string) (*Catalog, error) {
	if path == "" {
		return NewCatalog(nil, nil)
	}
	// #nosec G304 -- path is operator-owned startup configuration, never request data.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	var document struct {
		SchemaVersion string            `json:"schema_version"`
		Providers     []domain.Provider `json:"providers"`
		Models        []domain.Model    `json:"models"`
		Defaults      map[string]string `json:"default,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode model catalog: unexpected trailing JSON value")
	}
	if document.SchemaVersion != "1" {
		return nil, fmt.Errorf("unsupported model catalog schema version %q", document.SchemaVersion)
	}
	return NewCatalogWithDefaults(document.Providers, document.Models, document.Defaults)
}

func NewCatalog(providers []domain.Provider, models []domain.Model) (*Catalog, error) {
	return NewCatalogWithDefaults(providers, models, nil)
}

func NewCatalogWithDefaults(providers []domain.Provider, models []domain.Model, defaults map[string]string) (*Catalog, error) {
	providers = append([]domain.Provider(nil), providers...)
	models = append([]domain.Model(nil), models...)
	defaults = cloneDefaults(defaults)
	seenProviders := make(map[string]struct{}, len(providers))
	providerIndexes := make(map[string]int, len(providers))
	for index, provider := range providers {
		if provider.ID == "" || provider.Name == "" || provider.ID != strings.TrimSpace(provider.ID) || provider.Name != strings.TrimSpace(provider.Name) {
			return nil, fmt.Errorf("provider id and name are required")
		}
		if provider.SourceID != strings.TrimSpace(provider.SourceID) || provider.RuntimeID != strings.TrimSpace(provider.RuntimeID) {
			return nil, fmt.Errorf("provider %q has a non-canonical source_id or runtime_id", provider.ID)
		}
		runtimeIDs, err := canonicalRuntimeIDs(provider.RuntimeID, provider.RuntimeIDs)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", provider.ID, err)
		}
		if _, exists := seenProviders[provider.ID]; exists {
			return nil, fmt.Errorf("duplicate provider %q", provider.ID)
		}
		seenProviders[provider.ID] = struct{}{}
		providerIndexes[provider.ID] = index
		providers[index].ModelCount = 0
		providers[index].RuntimeIDs = runtimeIDs
	}
	seenModels := make(map[string]struct{}, len(models))
	for index, model := range models {
		if model.ID == "" || model.Name == "" || model.ProviderID == "" ||
			model.ID != strings.TrimSpace(model.ID) || model.Name != strings.TrimSpace(model.Name) ||
			model.ProviderID != strings.TrimSpace(model.ProviderID) || model.RuntimeID != strings.TrimSpace(model.RuntimeID) ||
			model.SourceProviderID != strings.TrimSpace(model.SourceProviderID) {
			return nil, fmt.Errorf("canonical model id, name and provider_id are required")
		}
		providerIndex, exists := providerIndexes[model.ProviderID]
		if !exists {
			return nil, fmt.Errorf("model %q references unknown provider %q", model.ID, model.ProviderID)
		}
		runtimeIDs, err := canonicalRuntimeIDs(model.RuntimeID, model.RuntimeIDs)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model.ID, err)
		}
		models[index].RuntimeIDs = runtimeIDs
		if providers[providerIndex].RuntimeID != model.RuntimeID || !slices.Equal(providers[providerIndex].RuntimeIDs, runtimeIDs) {
			return nil, fmt.Errorf("model %q runtime does not match provider %q", model.ID, model.ProviderID)
		}
		if model.Ready && (len(runtimeIDs) == 0 || !providers[providerIndex].Connected) {
			return nil, fmt.Errorf("ready model %q requires a connected runtime provider", model.ID)
		}
		if model.Status == "deprecated" && model.Ready {
			return nil, fmt.Errorf("deprecated model %q cannot be ready", model.ID)
		}
		if !validModelStatus(model.Status) {
			return nil, fmt.Errorf("model %q has unsupported status %q", model.ID, model.Status)
		}
		if model.Limits.Context < 0 || model.Limits.Input < 0 || model.Limits.Output < 0 {
			return nil, fmt.Errorf("model %q has a negative token limit", model.ID)
		}
		if model.Cost != nil && (model.Cost.Input < 0 || model.Cost.Output < 0) {
			return nil, fmt.Errorf("model %q has a negative base cost", model.ID)
		}
		key := model.ProviderID + "/" + model.ID
		if _, exists := seenModels[key]; exists {
			return nil, fmt.Errorf("duplicate model %q", key)
		}
		seenModels[key] = struct{}{}
		providers[providerIndex].ModelCount++
		if model.SourceProviderID == "" {
			models[index].SourceProviderID = providers[providerIndex].SourceID
		}
	}
	for providerID, modelID := range defaults {
		if providerID != strings.TrimSpace(providerID) || modelID != strings.TrimSpace(modelID) || providerID == "" || modelID == "" {
			return nil, fmt.Errorf("default model identifiers must be canonical")
		}
		model, ok := modelByKey(models, providerID, modelID)
		if !ok {
			return nil, fmt.Errorf("default model %s/%s does not exist", providerID, modelID)
		}
		if !model.Ready {
			return nil, fmt.Errorf("default model %s/%s is not ready", providerID, modelID)
		}
	}
	for _, provider := range providers {
		if _, exists := defaults[provider.ID]; exists || !provider.Connected {
			continue
		}
		if model, ok := defaultModel(models, provider.ID); ok {
			defaults[provider.ID] = model.ID
		}
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	sort.Slice(models, func(i, j int) bool {
		left := models[i].ProviderID + "/" + models[i].ID
		right := models[j].ProviderID + "/" + models[j].ID
		return left < right
	})
	modelIndexes, modelIDs := indexModels(models)
	return &Catalog{SchemaVersion: "1", Providers: providers, Models: models, Defaults: defaults, modelIndexes: modelIndexes, modelIDs: modelIDs}, nil
}

func (c *Catalog) Replace(next *Catalog) {
	providers := next.ProviderSnapshot()
	models := next.ModelSnapshot()
	defaults := next.DefaultSnapshot()
	c.mu.Lock()
	c.SchemaVersion = "1"
	c.Providers = providers
	c.Models = models
	c.Defaults = defaults
	c.modelIndexes, c.modelIDs = indexModels(models)
	c.mu.Unlock()
}

func (c *Catalog) ProviderSnapshot() []domain.Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]domain.Provider(nil), c.Providers...)
}

func (c *Catalog) ModelSnapshot() []domain.Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]domain.Model(nil), c.Models...)
}

func (c *Catalog) DefaultSnapshot() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneDefaults(c.Defaults)
}

// Resolve validates an immutable Portal model selection before an agent turn
// crosses the control-plane boundary. Empty provider/model pairs are accepted
// for backwards compatibility and are resolved by the runtime's configured
// default. Supplying only one half of the pair is always invalid.
func (c *Catalog) Resolve(runtimeID, providerID, modelID string) (domain.Model, error) {
	trimmedProviderID := strings.TrimSpace(providerID)
	trimmedModelID := strings.TrimSpace(modelID)
	if providerID != trimmedProviderID || modelID != trimmedModelID {
		return domain.Model{}, fmt.Errorf("%w: provider_id and model_id must not contain surrounding whitespace", domain.ErrInvalid)
	}
	if providerID == "" && modelID == "" {
		return domain.Model{}, nil
	}
	if providerID == "" || modelID == "" {
		return domain.Model{}, fmt.Errorf("%w: provider_id and model_id must be supplied together", domain.ErrInvalid)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	index, exists := c.modelIndexes[providerID+"/"+modelID]
	if !exists {
		return domain.Model{}, fmt.Errorf("%w: model %s/%s", domain.ErrNotFound, providerID, modelID)
	}
	candidate := c.Models[index]
	if !supportsRuntime(candidate.RuntimeID, candidate.RuntimeIDs, runtimeID) {
		return domain.Model{}, fmt.Errorf("%w: model is not assigned to runtime %q", domain.ErrInvalid, runtimeID)
	}
	if !candidate.Ready {
		reason := candidate.UnavailableReason
		if reason == "" {
			reason = "model is not ready"
		}
		return domain.Model{}, fmt.Errorf("%w: %s", domain.ErrUnavailable, reason)
	}
	return candidate, nil
}

func (c *Catalog) Search(query, runtimeID, providerID string, limit int) []domain.Model {
	return c.SearchPage(query, runtimeID, providerID, 0, limit).Models
}

func (c *Catalog) SearchPage(query, runtimeID, providerID string, offset, limit int) ModelPage {
	query = strings.ToLower(strings.TrimSpace(query))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]domain.Model, 0, limit)
	total := 0
	for _, model := range c.Models {
		if runtimeID != "" && !supportsRuntime(model.RuntimeID, model.RuntimeIDs, runtimeID) {
			continue
		}
		if providerID != "" && model.ProviderID != providerID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(model.ID+" "+model.Name+" "+model.Description+" "+model.Family+" "+model.Status), query) {
			continue
		}
		if total >= offset && len(result) < limit {
			result = append(result, model)
		}
		total++
	}
	nextOffset := 0
	if offset+len(result) < total {
		nextOffset = offset + len(result)
	}
	return ModelPage{Models: result, Total: total, Offset: offset, NextOffset: nextOffset}
}

func (c *Catalog) SearchProviders(query, runtimeID string, connectedOnly bool, limit int) []domain.Provider {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]domain.Provider, 0, limit)
	for _, provider := range c.Providers {
		if runtimeID != "" && !supportsRuntime(provider.RuntimeID, provider.RuntimeIDs, runtimeID) {
			continue
		}
		if connectedOnly && !provider.Connected {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(provider.ID+" "+provider.Name), query) {
			continue
		}
		result = append(result, provider)
		if len(result) == limit {
			break
		}
	}
	return result
}

func (c *Catalog) GetModel(providerID, modelID string) (domain.Model, error) {
	if providerID != strings.TrimSpace(providerID) || modelID != strings.TrimSpace(modelID) || modelID == "" {
		return domain.Model{}, fmt.Errorf("%w: canonical model id is required", domain.ErrInvalid)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if providerID != "" {
		index, exists := c.modelIndexes[providerID+"/"+modelID]
		if !exists {
			return domain.Model{}, fmt.Errorf("%w: model %s/%s", domain.ErrNotFound, providerID, modelID)
		}
		return c.Models[index], nil
	}
	indexes := c.modelIDs[modelID]
	if len(indexes) == 0 {
		return domain.Model{}, fmt.Errorf("%w: model %s/%s", domain.ErrNotFound, providerID, modelID)
	}
	if len(indexes) > 1 {
		return domain.Model{}, fmt.Errorf("%w: provider_id is required for ambiguous model %q", domain.ErrInvalid, modelID)
	}
	return c.Models[indexes[0]], nil
}

func validModelStatus(status string) bool {
	switch status {
	case "", "active", "alpha", "beta", "deprecated":
		return true
	default:
		return false
	}
}

func canonicalRuntimeIDs(primary string, additional []string) ([]string, error) {
	for _, runtimeID := range additional {
		if runtimeID == "" {
			return nil, fmt.Errorf("runtime_ids must not contain empty identifiers")
		}
	}
	result := make([]string, 0, 1+len(additional))
	seen := make(map[string]struct{}, 1+len(additional))
	for _, runtimeID := range append([]string{primary}, additional...) {
		if runtimeID == "" {
			continue
		}
		if runtimeID != strings.TrimSpace(runtimeID) || len(runtimeID) > 512 || strings.ContainsAny(runtimeID, "\r\n\x00") {
			return nil, fmt.Errorf("runtime_ids must contain canonical identifiers")
		}
		if _, exists := seen[runtimeID]; exists {
			continue
		}
		seen[runtimeID] = struct{}{}
		result = append(result, runtimeID)
	}
	return result, nil
}

func supportsRuntime(primary string, runtimeIDs []string, selected string) bool {
	if selected == "" {
		return true
	}
	if primary == selected {
		return true
	}
	return slices.Contains(runtimeIDs, selected)
}

func modelByKey(models []domain.Model, providerID, modelID string) (domain.Model, bool) {
	for _, model := range models {
		if model.ProviderID == providerID && model.ID == modelID {
			return model, true
		}
	}
	return domain.Model{}, false
}

func defaultModel(models []domain.Model, providerID string) (domain.Model, bool) {
	var candidates []domain.Model
	for _, model := range models {
		if model.ProviderID == providerID && model.Ready && model.Status != "deprecated" {
			candidates = append(candidates, model)
		}
	}
	if len(candidates) == 0 {
		return domain.Model{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftPriority := modelPriority(candidates[i].ID)
		rightPriority := modelPriority(candidates[j].ID)
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		leftLatest := strings.Contains(candidates[i].ID, "latest")
		rightLatest := strings.Contains(candidates[j].ID, "latest")
		if leftLatest != rightLatest {
			return leftLatest
		}
		return candidates[i].ID > candidates[j].ID
	})
	return candidates[0], true
}

func modelPriority(modelID string) int {
	priorities := []string{"gpt-5", "claude-sonnet-4", "big-pickle", "gemini-3-pro"}
	for index, priority := range priorities {
		if strings.Contains(modelID, priority) {
			return index
		}
	}
	return -1
}

func cloneDefaults(defaults map[string]string) map[string]string {
	result := make(map[string]string, len(defaults))
	for providerID, modelID := range defaults {
		result[providerID] = modelID
	}
	return result
}

func indexModels(models []domain.Model) (map[string]int, map[string][]int) {
	byKey := make(map[string]int, len(models))
	byID := make(map[string][]int)
	for index, model := range models {
		byKey[model.ProviderID+"/"+model.ID] = index
		byID[model.ID] = append(byID[model.ID], index)
	}
	return byKey, byID
}
