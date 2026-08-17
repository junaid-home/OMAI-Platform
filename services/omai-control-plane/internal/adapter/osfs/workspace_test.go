package osfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

func TestWorkspaceContainsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(context.Background(), principal, workspace.ID, "safe/file.txt", []byte("ok"), domain.WriteFileOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadFile(context.Background(), principal, workspace.ID, "../escape"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("traversal should be forbidden: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.WriteFile(context.Background(), principal, workspace.ID, "outside/file.txt", []byte("bad"), domain.WriteFileOptions{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escaping symlink should be forbidden: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(context.Background(), principal, workspace.ID, "outside/secret.txt", []byte("bad"), domain.WriteFileOptions{
		ExpectedRevision: "sha256:2bb80d537b1da3e38bd30361aa855686bde0ba715d1dc4628fba7e4fefc6cc1", RequireRevisionMatch: true,
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("revision check through escaping symlink should be forbidden: %v", err)
	}
}

func TestWorkspaceRevisionProtectsEditorWrites(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.WriteFile(context.Background(), principal, workspace.ID, "main.go", []byte("package main\n"), domain.WriteFileOptions{
		RequireRevisionMatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision == "" || created.Size != int64(len(created.Data)) {
		t.Fatalf("invalid created file metadata: %#v", created)
	}
	if _, err := store.WriteFile(context.Background(), principal, workspace.ID, "main.go", []byte("stale\n"), domain.WriteFileOptions{
		ExpectedRevision: "sha256:0000000000000000000000000000000000000000000000000000000000000000", RequireRevisionMatch: true,
	}); !errors.Is(err, domain.ErrStaleRevision) {
		t.Fatalf("stale editor write should conflict: %v", err)
	}
	updated, err := store.WriteFile(context.Background(), principal, workspace.ID, "main.go", []byte("package updated\n"), domain.WriteFileOptions{
		ExpectedRevision: created.Revision, RequireRevisionMatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision == created.Revision {
		t.Fatal("updated content kept the previous revision")
	}
	read, err := store.ReadFile(context.Background(), principal, workspace.ID, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != updated.Revision || string(read.Data) != "package updated\n" {
		t.Fatalf("unexpected file snapshot: %#v", read)
	}
}

func TestWorkspaceMoveAndDeleteAreContained(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.WriteFile(context.Background(), principal, workspace.ID, "src/main.go", []byte("package main\n"), domain.WriteFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := store.MovePath(context.Background(), principal, workspace.ID, "src/main.go", "src/app.go", domain.MovePathOptions{
		ExpectedRevision: created.Revision, RequireRevisionMatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Revision != created.Revision {
		t.Fatalf("move changed the content revision: before=%s after=%s", created.Revision, moved.Revision)
	}
	if _, err := store.ReadFile(context.Background(), principal, workspace.ID, "src/main.go"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("move kept source file: %v", err)
	}
	if err := store.DeletePath(context.Background(), principal, workspace.ID, "src/app.go", domain.DeletePathOptions{
		ExpectedRevision: moved.Revision, RequireRevisionMatch: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadFile(context.Background(), principal, workspace.ID, "src/app.go"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete kept destination file: %v", err)
	}
	if _, err := store.MovePath(context.Background(), principal, workspace.ID, "../escape", "safe", domain.MovePathOptions{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escaping move should be forbidden: %v", err)
	}
	if err := store.DeletePath(context.Background(), principal, workspace.ID, "", domain.DeletePathOptions{Recursive: true}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("workspace-root delete should be invalid: %v", err)
	}
}

func TestWorkspaceWatchReportsChangesAndRejectsUnsafeTargets(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WatchFiles(context.Background(), principal, workspace.ID, []string{"../escape"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("traversal watch should be forbidden: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updates, err := store.WatchFiles(ctx, principal, workspace.ID, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	waitForWatchReady(t, ctx, updates)
	if err := os.WriteFile(file, []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for workspace change")
		case change := <-updates:
			if change.Path == "main.go" && change.Kind == domain.FileChangeChange {
				cancel()
				for range updates {
				}
				return
			}
		}
	}
}

func TestWorkspaceWatchCanTrackADeletedOrNotYetCreatedOpenFile(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updates, err := store.WatchFiles(ctx, principal, workspace.ID, []string{"future.go"})
	if err != nil {
		t.Fatal(err)
	}
	waitForWatchReady(t, ctx, updates)
	if err := os.WriteFile(filepath.Join(root, "future.go"), []byte("package future\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for created workspace file")
		case change := <-updates:
			if change.Path == "future.go" && change.Kind == domain.FileChangeAdd {
				cancel()
				for range updates {
				}
				return
			}
		}
	}
}

func waitForWatchReady(t *testing.T, ctx context.Context, updates <-chan domain.FileChange) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for workspace watch readiness")
		case change, ok := <-updates:
			if !ok {
				t.Fatal("workspace watch closed before readiness")
			}
			if change.Kind == domain.FileChangeResync {
				return
			}
		}
	}
}

func TestRepositoryDiscoveryRejectsEscapingGitMetadata(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+outside+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaces([]string{root}, 1024)
	workspace, err := store.Resolve(context.Background(), domain.Principal{TenantID: "tenant", ActorID: "actor"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.RepoRoot != "" {
		t.Fatalf("escaping Git metadata was accepted: %s", workspace.RepoRoot)
	}
}

func TestSearchFilesFiltersFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "components"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "component.go"), []byte("package component\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	directories, err := store.SearchFiles(context.Background(), principal, workspace.ID, "component", domain.FileSearchDirectories, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(directories) != 1 || directories[0] != "src/components" {
		t.Fatalf("unexpected directories: %v", directories)
	}
	files, err := store.SearchFiles(context.Background(), principal, workspace.ID, "component", domain.FileSearchFiles, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "src/component.go" {
		t.Fatalf("unexpected files: %v", files)
	}
}

func TestCreateDirectoryIsContainedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1024)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.CreateDirectory(context.Background(), principal, workspace.ID, "projects/demo"); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "projects", "demo")); err != nil || !info.IsDir() {
		t.Fatalf("directory was not created: info=%v err=%v", info, err)
	}
	if err := store.CreateDirectory(context.Background(), principal, workspace.ID, "../escape"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("directory traversal should be forbidden: %v", err)
	}
}

func FuzzLexicalPathNeverEscapesRoot(f *testing.F) {
	root := filepath.Join(string(filepath.Separator), "omai-fuzz-workspace")
	for _, seed := range []string{"main.go", "safe/file.txt", "../escape", "..\\escape", "/absolute", "a/../../escape", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, relative string) {
		candidate, err := lexicalPath(root, relative)
		if err != nil {
			return
		}
		if !within(root, candidate) {
			t.Fatalf("lexicalPath(%q) escaped root: %q", relative, candidate)
		}
	})
}
