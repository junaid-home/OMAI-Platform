package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestEventsReplayAndTenantIsolation(t *testing.T) {
	store := NewEvents(2)
	ctx := context.Background()
	owner := domain.Principal{TenantID: "a", ActorID: "one"}
	for index := 0; index < 4; index++ {
		if _, err := store.Publish(ctx, owner, domain.Event{SessionID: "session", Type: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := store.Subscribe(ctx, owner, "session", 1); !errors.Is(err, domain.ErrReplayTooOld) {
		t.Fatalf("expected replay-too-old, got %v", err)
	}
	replay, _, stop, err := store.Subscribe(ctx, owner, "session", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if len(replay) != 1 || replay[0].Sequence != 4 {
		t.Fatalf("unexpected replay: %#v", replay)
	}
	if _, _, _, err := store.Subscribe(ctx, domain.Principal{TenantID: "b"}, "session", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tenant boundary leaked: %v", err)
	}
}
