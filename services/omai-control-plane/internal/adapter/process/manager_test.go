package process

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/omai/backend/internal/adapter/osfs"
	"github.com/omai/backend/internal/domain"
)

func TestManagerRunsAndReplaysTerminalOutput(t *testing.T) {
	root := t.TempDir()
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(workspaces, 1<<20, 4)
	defer manager.Close()
	info, err := manager.Start(context.Background(), principal, domain.ProcessSpec{WorkspaceID: workspace.ID, Kind: "terminal", Command: "/bin/sh", Args: []string{"-c", "printf omai-terminal"}})
	if err != nil {
		t.Fatal(err)
	}
	replay, updates, stop, err := manager.Watch(context.Background(), principal, info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	chunks := append([]domain.ProcessChunk(nil), replay...)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case chunk, ok := <-updates:
			if !ok {
				goto completed
			}
			chunks = append(chunks, chunk)
		case <-timer.C:
			t.Fatal("terminal did not exit")
		}
	}
completed:
	var output bytes.Buffer
	exited := false
	for _, chunk := range chunks {
		output.Write(chunk.Data)
		exited = exited || chunk.Exited
	}
	if !bytes.Contains(output.Bytes(), []byte("omai-terminal")) {
		t.Fatalf("terminal output missing: %q", output.String())
	}
	if !exited {
		t.Fatal("terminal exit was not published")
	}
}

func TestManagerIsTenantScoped(t *testing.T) {
	root := t.TempDir()
	owner := domain.Principal{TenantID: "owner", ActorID: "actor"}
	stranger := domain.Principal{TenantID: "stranger", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), owner, root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(workspaces, 1<<20, 4)
	defer manager.Close()
	info, err := manager.Start(context.Background(), owner, domain.ProcessSpec{WorkspaceID: workspace.ID, Kind: "terminal", Command: "/bin/sh", Args: []string{"-c", "sleep 1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(context.Background(), stranger, info.ID, []byte("x")); err == nil {
		t.Fatal("another tenant wrote to the terminal")
	}
}
