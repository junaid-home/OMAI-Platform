package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

func TestVoiceTicketIsOneTimeAndLeaseLimited(t *testing.T) {
	store := NewVoiceLeases()
	ctx := context.Background()
	admission := domain.VoiceAdmission{TenantID: "tenant", ActorID: "actor", SubjectKey: "subject", WorkspaceID: "workspace", Permissions: []string{"voice:connect"}, ExpiresAt: time.Now().Add(time.Minute)}
	if err := store.Issue(ctx, "ticket-1", admission); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Redeem(ctx, "ticket-1", "session-1", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Token == "" {
		t.Fatal("lease token missing")
	}
	if _, err := store.Redeem(ctx, "ticket-1", "session-replay", 1, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ticket replay was accepted: %v", err)
	}
	if err := store.Issue(ctx, "ticket-2", admission); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Redeem(ctx, "ticket-2", "session-2", 1, time.Minute); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("session limit was not enforced: %v", err)
	}
	if err := store.Release(ctx, lease.Token); err != nil {
		t.Fatal(err)
	}
}
