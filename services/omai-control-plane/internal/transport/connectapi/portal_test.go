package connectapi

import "testing"

func TestPortalAllowLists(t *testing.T) {
	if value, ok := portalView("Projekte"); !ok || value != "projects" {
		t.Fatalf("unexpected project view: %q %v", value, ok)
	}
	if _, ok := portalView("https://evil.example"); ok {
		t.Fatal("arbitrary portal route was accepted")
	}
	if value, ok := portalPanel("Vorschau"); !ok || value != "preview" {
		t.Fatalf("unexpected preview panel: %q %v", value, ok)
	}
	if value, ok := portalPanelMode("Öffnen"); !ok || value != "show" {
		t.Fatalf("unexpected panel mode: %q %v", value, ok)
	}
	if safePortalPath("../../secret") {
		t.Fatal("escaping file path was accepted")
	}
	if !safePortalPath("internal/app/main.go") {
		t.Fatal("safe file path was rejected")
	}
}
