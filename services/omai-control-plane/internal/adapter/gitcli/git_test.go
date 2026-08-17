package gitcli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omai/backend/gen/go/omai/executor/v1/executorv1connect"
	commandadapter "github.com/omai/backend/internal/adapter/command"
	"github.com/omai/backend/internal/adapter/osfs"
	processadapter "github.com/omai/backend/internal/adapter/process"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/transport/executorapi"
)

func TestStageUnstageAndStagedDiff(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(workspaces, commandadapter.NewLocal(workspaces, 1<<20, time.Minute), 1<<20)
	status, err := repository.Stage(context.Background(), principal, workspace.ID, []string{"main.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 1 || status.Files[0].Status[0] == ' ' || status.Files[0].Status[0] == '?' {
		t.Fatalf("file was not staged: %+v", status.Files)
	}
	diff, err := repository.Diff(context.Background(), principal, workspace.ID, "main.go", true)
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("staged diff is empty")
	}
	status, err = repository.Unstage(context.Background(), principal, workspace.ID, []string{"main.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 1 || status.Files[0].Status != "??" {
		t.Fatalf("file was not unstaged: %+v", status.Files)
	}
}

func TestStructuredDiffsIncludeTrackedAndUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "OMAI Test"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "--quiet", "--message", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(workspaces, commandadapter.NewLocal(workspaces, 1<<20, time.Minute), 1<<20)
	status, err := repository.Status(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch != "main" || status.DefaultBranch != "main" {
		t.Fatalf("unexpected branches: %+v", status)
	}
	diffs, err := repository.DiffFiles(context.Background(), principal, workspace.ID, "git", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 {
		t.Fatalf("diffs=%+v", diffs)
	}
	byPath := make(map[string]domain.GitFileDiff, len(diffs))
	for _, diff := range diffs {
		byPath[diff.File] = diff
	}
	if diff := byPath["tracked.txt"]; diff.Status != "modified" || diff.Additions != 2 || diff.Deletions != 1 || !strings.Contains(diff.Patch, "@@ ") {
		t.Fatalf("tracked diff=%+v", diff)
	}
	if diff := byPath["new.txt"]; diff.Status != "added" || diff.Additions != 1 || diff.Deletions != 0 || !strings.Contains(diff.Patch, "@@ ") {
		t.Fatalf("untracked diff=%+v", diff)
	}
}

func TestGitRunsThroughRemoteWorkspaceExecutor(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "remote.go"), []byte("package remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	controlWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := controlWorkspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	executorWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	processes := processadapter.New(executorWorkspaces, 1<<20, 4)
	defer processes.Close()
	service := &executorapi.Service{
		Processes: processes, Commands: commandadapter.NewLocal(executorWorkspaces, 1<<20, time.Minute),
		Workspaces: executorWorkspaces, Root: root, AllowedTenant: principal.TenantID, ExpectedWorkspaceID: workspace.ID,
	}
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer executor-token-0123456789-abcdef" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(response, request)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := processadapter.NewRemote(controlWorkspaces, processadapter.RemoteConfig{
		Endpoint: server.URL, Token: "executor-token-0123456789-abcdef", Transport: "connect",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := New(controlWorkspaces, remote, 1<<20)
	status, err := repository.Stage(context.Background(), principal, workspace.ID, []string{"remote.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 1 || status.Files[0].Status[0] == '?' {
		t.Fatalf("remote Git command did not stage file: %+v", status.Files)
	}
}

func TestManagedWorktreeStaysInsideWorkspaceAndOutOfStatus(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "OMAI Test"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "main.go"}, {"commit", "--quiet", "--message", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(workspaces, commandadapter.NewLocal(workspaces, 1<<20, time.Minute), 1<<20)
	worktree, err := repository.CreateWorktree(context.Background(), principal, workspace.ID, "agent/test", "")
	if err != nil {
		t.Fatal(err)
	}
	expectedPrefix := filepath.Join(root, filepath.FromSlash(managedWorktreeRoot)) + string(filepath.Separator)
	if !strings.HasPrefix(worktree.Path+string(filepath.Separator), expectedPrefix) {
		t.Fatalf("worktree escaped workspace: %s", worktree.Path)
	}
	status, err := repository.Status(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 0 {
		t.Fatalf("managed worktree polluted status: %+v", status.Files)
	}
	worktrees, err := repository.ListWorktrees(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktrees=%+v", worktrees)
	}
	if err := repository.RemoveWorktree(context.Background(), principal, workspace.ID, worktree.Path); err != nil {
		t.Fatal(err)
	}
}
