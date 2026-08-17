package application

import (
	"errors"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestCatalogResolvesProviderScopedModel(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog(
		[]domain.Provider{
			{ID: "one", Name: "One", RuntimeID: "go-adk", Connected: true},
			{ID: "two", Name: "Two", RuntimeID: "go-adk", Connected: true},
		},
		[]domain.Model{
			{ID: "shared", Name: "Shared One", ProviderID: "one", RuntimeID: "go-adk", Ready: true},
			{ID: "shared", Name: "Shared Two", ProviderID: "two", RuntimeID: "go-adk", Ready: true},
		},
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	selected, err := catalog.Resolve("go-adk", "two", "shared")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if selected.ProviderID != "two" || selected.Name != "Shared Two" {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestCatalogAllowsOneModelRouteAcrossAgentRuntimes(t *testing.T) {
	t.Parallel()

	runtimes := []string{"go-adk", "opencode"}
	catalog, err := NewCatalog(
		[]domain.Provider{{ID: "google", Name: "Google", RuntimeID: "go-adk", RuntimeIDs: runtimes, Connected: true}},
		[]domain.Model{{ID: "gemini", Name: "Gemini", ProviderID: "google", RuntimeID: "go-adk", RuntimeIDs: runtimes, Ready: true}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Resolve("opencode", "google", "gemini"); err != nil {
		t.Fatalf("shared harness route was rejected: %v", err)
	}
	if models := catalog.SearchPage("", "opencode", "google", 0, 10).Models; len(models) != 1 {
		t.Fatalf("shared harness model was not discoverable: %#v", models)
	}
	if providers := catalog.SearchProviders("", "opencode", true, 10); len(providers) != 1 {
		t.Fatalf("shared harness provider was not discoverable: %#v", providers)
	}
	if _, err := catalog.Resolve("foreign", "google", "gemini"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("foreign runtime route was accepted: %v", err)
	}
}

func TestCatalogRejectsNonCanonicalAndUnavailableSelection(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog(
		[]domain.Provider{{ID: "provider", Name: "Provider", RuntimeID: "go-adk", Connected: true}},
		[]domain.Model{{
			ID: "model", Name: "Model", ProviderID: "provider", RuntimeID: "go-adk",
			Ready: false, UnavailableReason: "disabled by policy",
		}},
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	if _, err := catalog.Resolve("go-adk", " provider", "model"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("whitespace error = %v, want ErrInvalid", err)
	}
	if _, err := catalog.Resolve("go-adk", "provider", "model"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("availability error = %v, want ErrUnavailable", err)
	}
}

func TestCatalogPaginatesCompleteCatalog(t *testing.T) {
	t.Parallel()

	catalog, err := NewCatalog(
		[]domain.Provider{{ID: "provider", Name: "Provider", RuntimeID: "go-adk", Connected: true}},
		[]domain.Model{
			{ID: "a", Name: "Alpha", ProviderID: "provider", RuntimeID: "go-adk", Ready: true},
			{ID: "b", Name: "Beta", ProviderID: "provider", RuntimeID: "go-adk", Ready: true},
			{ID: "c", Name: "Gamma", ProviderID: "provider", RuntimeID: "go-adk", Ready: true},
		},
	)
	if err != nil {
		t.Fatalf("NewCatalog() error = %v", err)
	}
	page := catalog.SearchPage("", "go-adk", "provider", 1, 1)
	if page.Total != 3 || page.Offset != 1 || page.NextOffset != 2 || len(page.Models) != 1 || page.Models[0].ID != "b" {
		t.Fatalf("page = %#v", page)
	}
	defaults := catalog.DefaultSnapshot()
	if defaults["provider"] == "" {
		t.Fatalf("default = %#v", defaults)
	}
}
