package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

func TestVoiceDispatchesAreAtomicAndReplayCompletedResults(t *testing.T) {
	store := NewVoiceDispatches()
	ctx := context.Background()
	record, state, err := store.Begin(ctx, "key", "fingerprint", time.Minute)
	if err != nil || state != port.VoiceDispatchStarted || len(record.Result) != 0 {
		t.Fatalf("unexpected first begin: record=%+v state=%v err=%v", record, state, err)
	}
	_, state, err = store.Begin(ctx, "key", "fingerprint", time.Minute)
	if err != nil || state != port.VoiceDispatchRunning {
		t.Fatalf("duplicate running call was not detected: state=%v err=%v", state, err)
	}
	if err := store.Complete(ctx, "key", "fingerprint", []byte(`{"ok":true}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	record, state, err = store.Begin(ctx, "key", "fingerprint", time.Minute)
	if err != nil || state != port.VoiceDispatchCached || string(record.Result) != `{"ok":true}` {
		t.Fatalf("completed result was not replayed: record=%+v state=%v err=%v", record, state, err)
	}
	if _, _, err := store.Begin(ctx, "key", "different", time.Minute); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("fingerprint conflict was accepted: %v", err)
	}
}

func TestVoiceDispatchAbortAllowsRetry(t *testing.T) {
	store := NewVoiceDispatches()
	ctx := context.Background()
	if _, _, err := store.Begin(ctx, "key", "fingerprint", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Abort(ctx, "key", "fingerprint"); err != nil {
		t.Fatal(err)
	}
	_, state, err := store.Begin(ctx, "key", "fingerprint", time.Minute)
	if err != nil || state != port.VoiceDispatchStarted {
		t.Fatalf("aborted dispatch could not retry: state=%v err=%v", state, err)
	}
}
