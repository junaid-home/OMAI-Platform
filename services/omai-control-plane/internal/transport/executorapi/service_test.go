package executorapi

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestExecutorWorkspaceRootContainsRelativeMapping(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "tenant", "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := executorWorkspaceRoot(root, "tenant/project")
	if err != nil || resolved != project {
		t.Fatalf("resolved workspace = %q, %v", resolved, err)
	}
	if _, err := executorWorkspaceRoot(root, "../escape"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("traversal should be forbidden: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := executorWorkspaceRoot(root, "outside"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escaping symlink should be forbidden: %v", err)
	}
}
