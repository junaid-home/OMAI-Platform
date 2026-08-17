//go:build linux

package process

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/omai/backend/internal/adapter/osfs"
	"github.com/omai/backend/internal/domain"
)

func TestWaitPreviewPortReturnsOnlyProcessOwnedListener(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
	root := t.TempDir()
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	manager := New(workspaces, 1<<20, 4)
	t.Cleanup(func() { _ = manager.Close() })
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := manager.AllocatePreviewPort(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unrelated.Close() })
	unrelatedPort := uint32(unrelated.Addr().(*net.TCPAddr).Port)
	processInfo, err := manager.Start(context.Background(), principal, domain.ProcessSpec{
		WorkspaceID: workspace.ID, Kind: "preview", Command: "python3",
		Args: []string{"-m", "http.server", strconv.Itoa(int(owned)), "--bind", "127.0.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selected, err := manager.WaitPreviewPort(waitCtx, principal, processInfo.ID, []uint32{unrelatedPort, owned})
	if err != nil {
		t.Fatal(err)
	}
	if selected != owned {
		t.Fatalf("selected port = %d, want process-owned %d (unrelated %d)", selected, owned, unrelatedPort)
	}
}
