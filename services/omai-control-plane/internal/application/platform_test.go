package application

import (
	"context"
	"errors"
	"testing"

	"github.com/omai/backend/internal/adapter/memory"
	"github.com/omai/backend/internal/adapter/osfs"
	runtimeadapter "github.com/omai/backend/internal/adapter/runtime"
	"github.com/omai/backend/internal/domain"
)

func TestPlatformProjectAndSessionLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor-a"}
	root := t.TempDir()
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	runtimes := runtimeadapter.NewRegistry()
	if err := runtimes.Register(runtimeadapter.NewDemo()); err != nil {
		t.Fatal(err)
	}
	projects := memory.NewProjects()
	sessions := memory.NewSessions()
	conversations := memory.NewConversations()
	events := memory.NewEvents(32)
	orchestrator := NewOrchestrator(runtimes, workspaces, sessions, conversations, events)
	catalog, err := NewCatalog(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	platform := NewPlatform(projects, sessions, conversations, events, workspaces, runtimes, orchestrator, catalog)

	project, created, err := platform.ResolveProject(ctx, principal, root, "OMAI")
	if err != nil {
		t.Fatal(err)
	}
	if !created || project.ID == "" || project.WorkspaceID == "" {
		t.Fatalf("unexpected project: %#v, created=%v", project, created)
	}
	resolved, createdAgain, err := platform.ResolveProject(ctx, principal, root, "OMAI")
	if err != nil || createdAgain || resolved.ID != project.ID {
		t.Fatalf("project resolution is not idempotent: %#v, created=%v, err=%v", resolved, createdAgain, err)
	}
	name, color := "OMAI Platform", "purple"
	icon, startup := "data:image/png;base64,iVBORw0KGgo=", "go run ./cmd/omai-server"
	updatedProject, err := platform.UpdateProject(ctx, principal, project.ID, domain.ProjectPatch{
		Name: &name, IconColor: &color, IconOverride: &icon, StartupCommand: &startup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updatedProject.Name != name || updatedProject.IconColor != color || updatedProject.IconOverride != icon || updatedProject.StartupCommand != startup {
		t.Fatalf("project presentation update mismatch: %#v", updatedProject)
	}

	session, sessionCreated, err := platform.CreateSession(ctx, principal, project.ID, "go-adk-demo", "web-1", "First turn")
	if err != nil {
		t.Fatal(err)
	}
	if !sessionCreated || session.ProjectID != project.ID || session.WorkspaceID != project.WorkspaceID {
		t.Fatalf("unexpected session: %#v, created=%v", session, sessionCreated)
	}

	archived := true
	updated, err := platform.UpdateSession(ctx, principal, session.ID, domain.SessionPatch{Archived: &archived})
	if err != nil || !updated.Archived {
		t.Fatalf("archive session: %#v, err=%v", updated, err)
	}
	visible, _, err := platform.ListSessions(ctx, principal, project.ID, false, 50, "")
	if err != nil || len(visible) != 0 {
		t.Fatalf("archived session leaked into default listing: %#v, err=%v", visible, err)
	}
	all, _, err := platform.ListSessions(ctx, principal, project.ID, true, 50, "")
	if err != nil || len(all) != 1 || all[0].ID != session.ID {
		t.Fatalf("archived listing mismatch: %#v, err=%v", all, err)
	}

	if err := platform.DeleteSession(ctx, principal, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.GetSession(ctx, principal, session.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted session remains readable: %v", err)
	}
}

func TestPlatformRejectsCrossTenantProjectAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	owner := domain.Principal{TenantID: "owner", ActorID: "one"}
	other := domain.Principal{TenantID: "other", ActorID: "two"}
	root := t.TempDir()
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	projects := memory.NewProjects()
	sessions := memory.NewSessions()
	conversations := memory.NewConversations()
	events := memory.NewEvents(8)
	runtimes := runtimeadapter.NewRegistry()
	orchestrator := NewOrchestrator(runtimes, workspaces, sessions, conversations, events)
	catalog, err := NewCatalog(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	platform := NewPlatform(projects, sessions, conversations, events, workspaces, runtimes, orchestrator, catalog)
	project, _, err := platform.ResolveProject(ctx, owner, root, "Private")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platform.GetProject(ctx, other, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant project access returned %v", err)
	}
}
