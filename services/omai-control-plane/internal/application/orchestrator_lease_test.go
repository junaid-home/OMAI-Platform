package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omai/backend/internal/adapter/memory"
	"github.com/omai/backend/internal/adapter/osfs"
	runtimeadapter "github.com/omai/backend/internal/adapter/runtime"
	"github.com/omai/backend/internal/domain"
)

type deniedTurnLeases struct{}

func (deniedTurnLeases) Acquire(context.Context, domain.Principal, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (deniedTurnLeases) Renew(context.Context, domain.Principal, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (deniedTurnLeases) Release(context.Context, domain.Principal, string, string) error { return nil }
func (deniedTurnLeases) Active(context.Context, domain.Principal, string) (bool, error) {
	return true, nil
}

func TestOrchestratorRejectsDistributedTurnConflictBeforeAppendingMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	root := t.TempDir()
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	sessions := memory.NewSessions()
	conversations := memory.NewConversations()
	events := memory.NewEvents(8)
	runtimes := runtimeadapter.NewRegistry()
	if err := runtimes.Register(runtimeadapter.NewDemo()); err != nil {
		t.Fatal(err)
	}
	orchestrator := NewOrchestrator(runtimes, workspaces, sessions, conversations, events)
	orchestrator.UseTurnLeases(deniedTurnLeases{})

	_, err := orchestrator.Prompt(ctx, principal, domain.Prompt{
		RuntimeID: "go-adk-demo", ExternalSessionID: "external", Root: root, Text: "hello",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("prompt conflict = %v, want ErrConflict", err)
	}
	session, err := sessions.Find(ctx, principal, "go-adk-demo", "external")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := conversations.List(ctx, principal, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("conflicted turn appended messages: %#v", messages)
	}
}
