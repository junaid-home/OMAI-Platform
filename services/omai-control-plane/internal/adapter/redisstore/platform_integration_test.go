package redisstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

func TestPlatformStoresAgainstRedis(t *testing.T) {
	address := os.Getenv("OMAI_REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("OMAI_REDIS_TEST_ADDR is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := NewClient(address, os.Getenv("OMAI_REDIS_TEST_PASSWORD"), 0)
	defer client.Close()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	id, err := randomPlatformID("test_")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "omai:test:" + id + ":"
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		var cursor uint64
		for {
			keys, next, scanErr := client.Scan(cleanup, cursor, prefix+"*", 100).Result()
			if scanErr != nil {
				return
			}
			if len(keys) > 0 {
				_ = client.Del(cleanup, keys...).Err()
			}
			cursor = next
			if cursor == 0 {
				return
			}
		}
	})

	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace := domain.Workspace{ID: "wsp_test", TenantID: principal.TenantID, Root: "/workspace", RepoRoot: "/workspace"}
	projects := NewProjects(client, prefix)
	project, created, err := projects.Resolve(ctx, principal, workspace, "OMAI")
	if err != nil || !created {
		t.Fatalf("resolve project: %#v, created=%v, err=%v", project, created, err)
	}
	if persisted, err := NewProjects(client, prefix).Get(ctx, principal, project.ID); err != nil || persisted.ID != project.ID {
		t.Fatalf("project did not survive adapter replacement: %#v, err=%v", persisted, err)
	}

	sessions := NewSessions(client, prefix)
	session, created, err := sessions.Resolve(ctx, principal, "runtime", "external", project.ID, workspace.ID, workspace.Root, "First")
	if err != nil || !created {
		t.Fatalf("resolve session: %#v, created=%v, err=%v", session, created, err)
	}
	title := "Renamed"
	updated, err := sessions.Update(ctx, principal, session.ID, domain.SessionPatch{Title: &title})
	if err != nil || updated.Title != title {
		t.Fatalf("update session: %#v, err=%v", updated, err)
	}
	if persisted, err := NewSessions(client, prefix).Get(ctx, principal, session.ID); err != nil || persisted.Title != title {
		t.Fatalf("session did not survive adapter replacement: %#v, err=%v", persisted, err)
	}

	permissions := NewPermissions(client, prefix)
	permission := domain.PermissionRequest{
		ID: "permission", SessionID: session.ID, ProjectID: project.ID, TenantID: principal.TenantID,
		Permission: "edit", Patterns: []string{"src/**"}, MetadataJSON: []byte(`{"reason":"test"}`), CreatedAt: time.Now().UTC(),
	}
	if _, created, err := permissions.Create(ctx, principal, permission); err != nil || !created {
		t.Fatalf("create permission: created=%v, err=%v", created, err)
	}
	listedPermissions, err := NewPermissions(client, prefix).ListPending(ctx, principal, project.ID, "")
	if err != nil || len(listedPermissions) != 1 || listedPermissions[0].ID != permission.ID {
		t.Fatalf("permission persistence mismatch: %#v, err=%v", listedPermissions, err)
	}
	if _, changed, err := permissions.Respond(ctx, principal, session.ID, permission.ID, domain.PermissionDecisionOnce); err != nil || !changed {
		t.Fatalf("resolve permission: changed=%v, err=%v", changed, err)
	}
	if _, changed, err := permissions.Respond(ctx, principal, session.ID, permission.ID, domain.PermissionDecisionOnce); err != nil || changed {
		t.Fatalf("idempotent permission response: changed=%v, err=%v", changed, err)
	}

	questions := NewQuestions(client, prefix)
	question := domain.QuestionRequest{
		ID: "question", SessionID: session.ID, ProjectID: project.ID, TenantID: principal.TenantID, CreatedAt: time.Now().UTC(),
		Questions: []domain.Question{{Question: "Continue?", Header: "Continue", Options: []domain.QuestionOption{{Label: "Yes"}}}},
	}
	if _, created, err := questions.Create(ctx, principal, question); err != nil || !created {
		t.Fatalf("create question: created=%v, err=%v", created, err)
	}
	listedQuestions, err := NewQuestions(client, prefix).ListPending(ctx, principal, project.ID, "")
	if err != nil || len(listedQuestions) != 1 || listedQuestions[0].ID != question.ID {
		t.Fatalf("question persistence mismatch: %#v, err=%v", listedQuestions, err)
	}
	if _, changed, err := questions.Reply(ctx, principal, session.ID, question.ID, [][]string{{"Yes"}}, false); err != nil || !changed {
		t.Fatalf("resolve question: changed=%v, err=%v", changed, err)
	}

	conversations := NewConversations(client, prefix)
	if err := conversations.AppendText(ctx, principal, session.ID, "message", "assistant", "text", "hello "); err != nil {
		t.Fatal(err)
	}
	if err := conversations.AppendText(ctx, principal, session.ID, "message", "assistant", "text", "world"); err != nil {
		t.Fatal(err)
	}
	messages, err := NewConversations(client, prefix).List(ctx, principal, session.ID)
	if err != nil || len(messages) != 1 || messages[0].Text != "hello world" {
		t.Fatalf("conversation persistence mismatch: %#v, err=%v", messages, err)
	}

	events := NewEvents(client, prefix, 2)
	for index := 0; index < 3; index++ {
		if _, err := events.Publish(ctx, principal, domain.Event{SessionID: session.ID, Type: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	replay, _, stop, err := NewEvents(client, prefix, 2).Subscribe(ctx, principal, session.ID, 1)
	if err != nil || len(replay) != 2 || replay[0].Sequence != 2 || replay[1].Sequence != 3 {
		t.Fatalf("event replay mismatch: %#v, err=%v", replay, err)
	}
	if err := events.DeleteSession(ctx, principal, session.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("active event stream deletion = %v, want conflict", err)
	}
	stop()
	if err := events.DeleteSession(ctx, principal, session.ID); err != nil {
		t.Fatal(err)
	}

	leases := NewTurnLeases(client, prefix)
	if acquired, err := leases.Acquire(ctx, principal, session.ID, "owner-one", time.Minute); err != nil || !acquired {
		t.Fatalf("acquire turn lease: acquired=%v, err=%v", acquired, err)
	}
	if acquired, err := leases.Acquire(ctx, principal, session.ID, "owner-two", time.Minute); err != nil || acquired {
		t.Fatalf("duplicate turn lease: acquired=%v, err=%v", acquired, err)
	}
	if renewed, err := leases.Renew(ctx, principal, session.ID, "owner-one", time.Minute); err != nil || !renewed {
		t.Fatalf("renew turn lease: renewed=%v, err=%v", renewed, err)
	}
	if err := leases.Release(ctx, principal, session.ID, "owner-one"); err != nil {
		t.Fatal(err)
	}

	mcp := NewMCP(client, prefix)
	server := domain.MCPServer{ID: "docs", WorkspaceID: workspace.ID, Name: "Docs", Transport: "stdio", Command: "mcp-docs", Enabled: true}
	if _, err := mcp.Upsert(ctx, principal, server); err != nil {
		t.Fatal(err)
	}
	listed, err := NewMCP(client, prefix).List(ctx, principal, workspace.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != server.ID {
		t.Fatalf("MCP persistence mismatch: %#v, err=%v", listed, err)
	}
	deleted, err := NewMCP(client, prefix).Delete(ctx, principal, workspace.ID, server.ID)
	if err != nil || !deleted {
		t.Fatalf("delete MCP server: deleted=%v err=%v", deleted, err)
	}
	deleted, err = NewMCP(client, prefix).Delete(ctx, principal, workspace.ID, server.ID)
	if err != nil || deleted {
		t.Fatalf("idempotent MCP delete: deleted=%v err=%v", deleted, err)
	}
}
