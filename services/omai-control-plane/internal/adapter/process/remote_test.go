package process

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/omai/backend/gen/go/omai/executor/v1/executorv1connect"
	commandadapter "github.com/omai/backend/internal/adapter/command"
	"github.com/omai/backend/internal/adapter/osfs"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/transport/executorapi"
)

func TestRemoteExecutorOwnsWorkspaceMutationsAndArchives(t *testing.T) {
	root := t.TempDir()
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor"}
	controlWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := controlWorkspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	executorWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	local := New(executorWorkspaces, 1<<20, 4)
	defer local.Close()
	service := &executorapi.Service{Workspaces: executorWorkspaces, Root: root, AllowedTenant: principal.TenantID, ExpectedWorkspaceID: workspace.ID}
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, requireTestToken(handler, "executor-token-0123456789-abcdef"))
	endpoint := newH2CExecutorServer(t, mux)
	remote, err := NewRemote(controlWorkspaces, RemoteConfig{
		Endpoint: endpoint, Token: "executor-token-0123456789-abcdef", Transport: "grpc",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaces := remote.WorkspaceRepository()
	var archiveBuffer bytes.Buffer
	archiveWriter := zip.NewWriter(&archiveBuffer)
	file, err := archiveWriter.Create("project/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("package main\n")); err != nil {
		t.Fatal(err)
	}
	if err := archiveWriter.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := workspaces.ImportArchive(context.Background(), principal, workspace.ID, archiveBuffer.Bytes(), domain.ArchiveImportOptions{StripSingleRoot: true})
	if err != nil || result.Files != 1 {
		t.Fatalf("remote archive import = %#v, %v", result, err)
	}
	content, err := workspaces.ReadFile(context.Background(), principal, workspace.ID, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.MovePath(context.Background(), principal, workspace.ID, "main.go", "app.go", domain.MovePathOptions{
		ExpectedRevision: content.Revision, RequireRevisionMatch: true,
	}); err != nil {
		t.Fatal(err)
	}
	exported, err := workspaces.ExportArchive(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(exported)
	closeErr := exported.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("remote archive export: read=%v close=%v", err, closeErr)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 || reader.File[0].Name != "app.go" {
		t.Fatalf("remote archive paths = %#v, %v", reader.File, err)
	}
	if err := workspaces.DeletePath(context.Background(), principal, workspace.ID, "app.go", domain.DeletePathOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteExecutorMapsAContainedSubworkspace(t *testing.T) {
	mount := t.TempDir()
	project := filepath.Join(mount, "tenant", "project-a")
	other := filepath.Join(mount, "tenant", "project-b")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor"}
	controlWorkspaces := osfs.NewWorkspaces([]string{mount}, 1<<20)
	workspace, err := controlWorkspaces.Resolve(context.Background(), principal, project)
	if err != nil {
		t.Fatal(err)
	}
	executorWorkspaces := osfs.NewWorkspaces([]string{mount}, 1<<20)
	service := &executorapi.Service{Workspaces: executorWorkspaces, Root: mount, AllowedTenant: principal.TenantID}
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, requireTestToken(handler, "executor-token-0123456789-abcdef"))
	endpoint := newH2CExecutorServer(t, mux)
	remote, err := NewRemote(controlWorkspaces, RemoteConfig{
		Endpoint: endpoint, ControlRoot: mount, Token: "executor-token-0123456789-abcdef", Transport: "grpc",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaces := remote.WorkspaceRepository()
	if _, err := workspaces.WriteFile(context.Background(), principal, workspace.ID, "only-project-a.txt", []byte("owned by A\n"), domain.WriteFileOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, "only-project-a.txt")); err != nil {
		t.Fatalf("subworkspace file was not written below project A: %v", err)
	}
	for _, escaped := range []string{
		filepath.Join(mount, "only-project-a.txt"),
		filepath.Join(other, "only-project-a.txt"),
	} {
		if _, err := os.Stat(escaped); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("subworkspace operation escaped to %s: %v", escaped, err)
		}
	}
}

func TestRemoteExecutorRunsAndStreamsWorkspaceProcess(t *testing.T) {
	root := t.TempDir()
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor"}
	controlWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := controlWorkspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	executorWorkspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	local := New(executorWorkspaces, 1<<20, 4)
	defer local.Close()
	commands := commandadapter.NewLocal(executorWorkspaces, 1<<20, time.Minute)
	service := &executorapi.Service{
		Processes: local, Commands: commands, Workspaces: executorWorkspaces, Root: root,
		AllowedTenant: principal.TenantID, ExpectedWorkspaceID: workspace.ID,
	}
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, requireTestToken(handler, "executor-token-0123456789-abcdef"))
	endpoint := newH2CExecutorServer(t, mux)

	remote, err := NewRemote(controlWorkspaces, RemoteConfig{
		Endpoint: endpoint, Token: "executor-token-0123456789-abcdef", Transport: "grpc",
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteWorkspaces := remote.WorkspaceRepository()
	if _, err := remoteWorkspaces.WriteFile(context.Background(), principal, workspace.ID, "src/main.txt", []byte("omai workspace executor\n"), domain.WriteFileOptions{}); err != nil {
		t.Fatal(err)
	}
	data, err := remoteWorkspaces.ReadFile(context.Background(), principal, workspace.ID, "src/main.txt")
	if err != nil || string(data.Data) != "omai workspace executor\n" || data.Revision == "" {
		t.Fatalf("remote workspace read = %#v, %v", data, err)
	}
	entries, err := remoteWorkspaces.ListFiles(context.Background(), principal, workspace.ID, "src")
	if err != nil || len(entries) != 1 || entries[0].Path != "src/main.txt" {
		t.Fatalf("remote workspace entries = %#v, %v", entries, err)
	}
	paths, err := remoteWorkspaces.SearchFiles(context.Background(), principal, workspace.ID, "main", domain.FileSearchFiles, 10)
	if err != nil || len(paths) != 1 || paths[0] != "src/main.txt" {
		t.Fatalf("remote workspace search = %#v, %v", paths, err)
	}
	matches, err := remoteWorkspaces.SearchText(context.Background(), principal, workspace.ID, "executor", 10)
	if err != nil || len(matches) != 1 || matches[0].Path != "src/main.txt" {
		t.Fatalf("remote workspace text search = %#v, %v", matches, err)
	}
	if _, err := remoteWorkspaces.ReadFile(context.Background(), principal, workspace.ID, "../secret"); !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("escaping remote workspace read error = %v", err)
	}
	fileWatchContext, stopFileWatch := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopFileWatch()
	fileUpdates, err := remoteWorkspaces.WatchFiles(fileWatchContext, principal, workspace.ID, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fileWatchContext.Done():
		t.Fatal("remote workspace watch did not become ready")
	case change := <-fileUpdates:
		if change.Kind != domain.FileChangeResync {
			t.Fatalf("first remote workspace watch update = %#v; want resync readiness", change)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "src", "watched.txt"), []byte("watched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-fileWatchContext.Done():
			t.Fatal("remote workspace watch did not report the created file")
		case change := <-fileUpdates:
			if change.Path == "src/watched.txt" && change.Kind == domain.FileChangeAdd {
				goto fileWatchComplete
			}
		}
	}

fileWatchComplete:
	info, err := remote.Start(context.Background(), principal, domain.ProcessSpec{
		WorkspaceID: workspace.ID, Kind: "terminal", Command: "/bin/sh", Args: []string{"-c", "printf omai-remote-executor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.WorkspaceID != workspace.ID || info.CWD != root {
		t.Fatalf("process mapped to wrong workspace: %#v", info)
	}
	commandResult, err := remote.Run(context.Background(), principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: "/bin/sh", Args: []string{"-c", "printf omai-one-shot"}, MaxOutputBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(commandResult.Output) != "omai-one-shot" || commandResult.WorkspaceRoot != root {
		t.Fatalf("unexpected one-shot result: %#v", commandResult)
	}
	if _, lookErr := exec.LookPath("python3"); lookErr == nil {
		previewPort, allocateErr := remote.AllocatePreviewPort(context.Background(), principal, workspace.ID)
		if allocateErr != nil {
			t.Fatal(allocateErr)
		}
		previewProcess, startErr := remote.Start(context.Background(), principal, domain.ProcessSpec{
			WorkspaceID: workspace.ID, Kind: "preview", Command: "python3",
			Args: []string{"-m", "http.server", strconv.Itoa(int(previewPort)), "--bind", "127.0.0.1"},
		})
		if startErr != nil {
			t.Fatal(startErr)
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		ownedPort, waitErr := remote.WaitPreviewPort(waitCtx, principal, previewProcess.ID, []uint32{previewPort})
		waitCancel()
		if waitErr != nil || ownedPort != previewPort {
			t.Fatalf("remote preview listener = %d, %v; want %d", ownedPort, waitErr, previewPort)
		}
		if stopErr := remote.Stop(context.Background(), principal, previewProcess.ID); stopErr != nil {
			t.Fatal(stopErr)
		}
	}

	watchContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replay, updates, stop, err := remote.Watch(watchContext, principal, info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	chunks := append([]domain.ProcessChunk(nil), replay...)
	for {
		select {
		case chunk, ok := <-updates:
			if !ok {
				goto complete
			}
			chunks = append(chunks, chunk)
		case <-watchContext.Done():
			t.Fatal("remote executor stream did not finish")
		}
	}

complete:
	var output bytes.Buffer
	exited := false
	for _, chunk := range chunks {
		output.Write(chunk.Data)
		exited = exited || chunk.Exited
	}
	if !bytes.Contains(output.Bytes(), []byte("omai-remote-executor")) || !exited {
		t.Fatalf("unexpected remote process stream: output=%q exited=%v", output.String(), exited)
	}
}

func TestRemoteExecutorRejectsWrongTokenAndTenant(t *testing.T) {
	root := t.TempDir()
	principal := domain.Principal{TenantID: "tenant-a", ActorID: "actor"}
	workspaces := osfs.NewWorkspaces([]string{root}, 1<<20)
	workspace, err := workspaces.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	local := New(workspaces, 1<<20, 4)
	defer local.Close()
	service := &executorapi.Service{
		Processes: local, Commands: commandadapter.NewLocal(workspaces, 1<<20, time.Minute), Workspaces: workspaces,
		Root: root, AllowedTenant: principal.TenantID, ExpectedWorkspaceID: workspace.ID,
	}
	path, handler := executorv1connect.NewWorkspaceExecutorServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, requireTestToken(handler, "executor-token-0123456789-abcdef"))
	server := httptest.NewServer(mux)
	defer server.Close()

	wrongToken, err := NewRemote(workspaces, RemoteConfig{Endpoint: server.URL, Token: "wrong-token-012345678901234567890", Transport: "connect"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrongToken.Start(context.Background(), principal, domain.ProcessSpec{WorkspaceID: workspace.ID, Kind: "terminal", Command: "/bin/sh"})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("wrong token error = %v", err)
	}

	remote, err := NewRemote(workspaces, RemoteConfig{Endpoint: server.URL, Token: "executor-token-0123456789-abcdef", Transport: "connect"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := domain.Principal{TenantID: "tenant-b", ActorID: "actor"}
	if err := remote.Write(context.Background(), foreign, "pro_missing", []byte("x")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign tenant error = %v", err)
	}
}

func TestExecutorSearchLimit(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input int
		want  int32
	}{
		{name: "default negative", input: -1, want: 100},
		{name: "default zero", input: 0, want: 100},
		{name: "requested", input: 37, want: 37},
		{name: "maximum", input: 1000, want: 1000},
		{name: "bounded", input: int(^uint(0) >> 1), want: 1000},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := executorSearchLimit(test.input); got != test.want {
				t.Fatalf("executorSearchLimit(%d) = %d, want %d", test.input, got, test.want)
			}
		})
	}
}

func requireTestToken(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func newH2CExecutorServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	// Race-enabled package suites can delay an h2c request while other Go
	// packages compile and start processes. Keep the slow-header defense
	// without expiring a healthy multiplexed connection at one second.
	server := &http.Server{Handler: handler, Protocols: protocols, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})
	return "http://" + listener.Addr().String()
}
