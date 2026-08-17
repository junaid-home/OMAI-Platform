package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omai/backend/internal/adapter/osfs"
	"github.com/omai/backend/internal/domain"
)

func TestLocalRunnerBoundsOutputAndEnvironment(t *testing.T) {
	root := t.TempDir()
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewLocal(workspaces, 1024, time.Minute)
	result, err := runner.Run(context.Background(), principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "/bin/sh", Args: []string{"-c", "printf %s \"$SAFE_VALUE\""},
		Env: map[string]string{"SAFE_VALUE": "omai"}, MaxOutputBytes: 1024,
	})
	if err != nil || string(result.Output) != "omai" || result.WorkspaceRoot != root {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	_, err = runner.Run(context.Background(), principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "/bin/sh", Args: []string{"-c", "printf 12345"}, MaxOutputBytes: 4,
	})
	if !errors.Is(err, domain.ErrOutputTruncated) {
		t.Fatalf("output limit error=%v", err)
	}
	_, err = runner.Run(context.Background(), principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "/bin/sh", Env: map[string]string{"OMAI_SERVICE_TOKEN": "secret"}, MaxOutputBytes: 1024,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("reserved environment error=%v", err)
	}
}

func TestLocalRunnerRejectsEscapingWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewLocal(workspaces, 1024, time.Minute)
	_, err = runner.Run(context.Background(), principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "/bin/sh", CWD: "escape", MaxOutputBytes: 1024,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escaping CWD error=%v", err)
	}
}
