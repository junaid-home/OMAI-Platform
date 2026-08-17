package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omai/backend/internal/adapter/memory"
	"github.com/omai/backend/internal/domain"
)

func TestInteractionsAreTenantScopedDurableResources(t *testing.T) {
	ctx := context.Background()
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor-a"}
	sessions := memory.NewSessions()
	events := memory.NewEvents(64)
	permissions := memory.NewPermissions()
	questions := memory.NewQuestions()
	interactions := NewInteractions(sessions, permissions, questions, events)
	session, _, err := sessions.Resolve(ctx, principal, "go-adk", "external-1", "project-1", "workspace-1", "/workspace", "Test")
	if err != nil {
		t.Fatal(err)
	}

	createdAt := time.Now().UTC()
	permission, created, err := interactions.AskPermission(ctx, principal, domain.PermissionRequest{
		ID: "permission-1", SessionID: session.ID, Permission: "edit", Patterns: []string{"src/**"},
		MetadataJSON: []byte(`{"reason":"agent edit"}`), Always: []string{"src/**"}, CreatedAt: createdAt,
	})
	if err != nil || !created || permission.ProjectID != session.ProjectID {
		t.Fatalf("ask permission = %#v, %t, %v", permission, created, err)
	}
	if _, duplicate, err := interactions.AskPermission(ctx, principal, domain.PermissionRequest{
		ID: "permission-1", SessionID: session.ID, Permission: "edit", Patterns: []string{"src/**"},
		MetadataJSON: []byte(`{"reason":"agent edit"}`), Always: []string{"src/**"}, CreatedAt: createdAt,
	}); err != nil || duplicate {
		t.Fatalf("idempotent permission create = %t, %v", duplicate, err)
	}
	listed, next, err := interactions.ListPermissions(ctx, principal, session.ProjectID, "", 50, "")
	if err != nil || next != "" || len(listed) != 1 || listed[0].ID != permission.ID {
		t.Fatalf("list permissions = %#v, %q, %v", listed, next, err)
	}
	if foreign, _, err := interactions.ListPermissions(ctx, domain.Principal{TenantID: "tenant-b", ActorID: "actor-b"}, session.ProjectID, "", 50, ""); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign permissions = %#v, %v", foreign, err)
	}
	if _, err := interactions.RespondPermission(ctx, principal, session.ID, permission.ID, domain.PermissionDecisionOnce); err != nil {
		t.Fatal(err)
	}
	if _, err := interactions.RespondPermission(ctx, principal, session.ID, permission.ID, domain.PermissionDecisionOnce); err != nil {
		t.Fatalf("idempotent permission response: %v", err)
	}
	if _, err := interactions.RespondPermission(ctx, principal, session.ID, permission.ID, domain.PermissionDecisionReject); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflicting permission response = %v", err)
	}

	question, created, err := interactions.AskQuestion(ctx, principal, domain.QuestionRequest{
		ID: "question-1", SessionID: session.ID, CreatedAt: createdAt,
		Questions: []domain.Question{{
			Question: "Deploy now?", Header: "Deploy", Custom: false,
			Options: []domain.QuestionOption{{Label: "Yes", Description: "Start deployment"}, {Label: "No"}},
		}},
	})
	if err != nil || !created {
		t.Fatalf("ask question = %#v, %t, %v", question, created, err)
	}
	if _, err := interactions.ReplyQuestion(ctx, principal, session.ID, question.ID, [][]string{{"invalid"}}, false); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid question response = %v", err)
	}
	resolved, err := interactions.ReplyQuestion(ctx, principal, session.ID, question.ID, [][]string{{"Yes"}}, false)
	if err != nil || len(resolved.Answers) != 1 || len(resolved.Answers[0]) != 1 || resolved.Answers[0][0] != "Yes" {
		t.Fatalf("question response = %#v, %v", resolved, err)
	}

	replay, updates, stop, err := events.Subscribe(ctx, principal, session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if updates == nil || len(replay) != 4 {
		t.Fatalf("interaction event replay = %d events", len(replay))
	}
	wantTypes := []string{"permission.asked", "permission.replied", "question.asked", "question.replied"}
	for index, want := range wantTypes {
		if replay[index].Type != want {
			t.Fatalf("event %d type = %q; want %q", index, replay[index].Type, want)
		}
	}
}
