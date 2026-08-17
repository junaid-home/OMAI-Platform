package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

func TestModelLeaseIsRouteBoundRevocableAndExpiring(t *testing.T) {
	store, err := NewLeaseStore("http://127.0.0.1:8793", time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	lease, err := store.Issue(domain.Prompt{
		SessionID: "session", ProviderID: "google", ModelID: "gemini",
		Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.authorize(lease.Token, lease.RouteID); !ok {
		t.Fatal("valid lease was rejected")
	}
	if _, ok := store.authorize(lease.Token, "another-route"); ok {
		t.Fatal("lease authorized another model route")
	}
	second, err := store.Issue(domain.Prompt{
		SessionID: "session-2", ProviderID: "openai", ModelID: "gpt",
		Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	store.Revoke(second.Token)
	if _, ok := store.authorize(second.Token, second.RouteID); ok {
		t.Fatal("revoked lease remained active")
	}
	third, err := store.Issue(domain.Prompt{
		SessionID: "session-3", ProviderID: "google", ModelID: "gemini",
		Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if third.RouteID == lease.RouteID {
		t.Fatal("model route identifiers were deterministic")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := store.authorize(third.Token, third.RouteID); ok {
		t.Fatal("expired lease remained active")
	}
}

func TestModelLeaseRejectsDisguisedNonLoopbackEdge(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:8793@attacker.example",
		"http://attacker.example:8793",
		"https://127.0.0.1:8793",
		"http://127.0.0.1:8793/path",
	} {
		if _, err := NewLeaseStore(endpoint, time.Minute, 1); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("unsafe model edge %q was accepted: %v", endpoint, err)
		}
	}
}
