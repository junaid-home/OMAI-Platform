package application_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandadapter "github.com/omai/backend/internal/adapter/command"
	"github.com/omai/backend/internal/adapter/memory"
	"github.com/omai/backend/internal/adapter/osfs"
	previewadapter "github.com/omai/backend/internal/adapter/preview"
	processadapter "github.com/omai/backend/internal/adapter/process"
	"github.com/omai/backend/internal/adapter/projectdetector"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
)

func TestPreviewLifecycleStartsProxiesRestartsAndStopsRealServer(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the static preview integration test")
	}
	root := t.TempDir()
	index := filepath.Join(root, "index.html")
	writePreviewFile(t, index, "<h1>first</h1>")

	var route http.Handler = http.NotFoundHandler()
	edge := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		route.ServeHTTP(writer, request)
	}))
	t.Cleanup(edge.Close)
	publisher, err := previewadapter.NewPublisher(previewadapter.PublisherConfig{PublicBaseURL: edge.URL})
	if err != nil {
		t.Fatal(err)
	}
	route = publisher.Wrap(http.NotFoundHandler())

	workspaces := osfs.NewWorkspaces([]string{root}, 4<<20)
	processes := processadapter.New(workspaces, 1<<20, 8)
	t.Cleanup(func() { _ = processes.Close() })
	commands := commandadapter.NewLocal(workspaces, 1<<20, 30*time.Second)
	events := memory.NewEvents(128)
	manager, err := application.NewPreviewManager(workspaces, processes, commands, projectdetector.New(), publisher, events, application.PreviewConfig{
		BindHost: "127.0.0.1", RuntimeHost: "127.0.0.1", Preparation: application.PreviewPreparationNever,
		StartupTimeout: 10 * time.Second, IdleTimeout: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{TenantID: "tenant-preview", ActorID: "actor-preview", Permissions: []string{"*"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	first, err := manager.Start(ctx, principal, root, false)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	assertPreviewBody(t, first.PublicURL, "first")
	if first.Status != "ready" || first.Port == 0 || first.ProcessID == "" || first.WorkspaceID == "" {
		t.Fatalf("incomplete first preview = %+v", first)
	}

	writePreviewFile(t, index, "<h1>second</h1>")
	second, err := manager.Start(ctx, principal, root, true)
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if second.ID == first.ID || second.ProcessID == first.ProcessID || second.PublicURL == first.PublicURL {
		t.Fatalf("restart did not replace preview: first=%+v second=%+v", first, second)
	}
	assertPreviewBody(t, second.PublicURL, "second")
	response, err := http.Get(first.PublicURL) // #nosec G107 -- httptest capability URL.
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("old capability route status = %d", response.StatusCode)
	}

	if _, err := manager.Get(ctx, principal, second.WorkspaceID); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := manager.Stop(ctx, principal, second.WorkspaceID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	response, err = http.Get(second.PublicURL) // #nosec G107 -- httptest capability URL.
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("stopped capability route status = %d", response.StatusCode)
	}
}

func assertPreviewBody(t *testing.T, url, expected string) {
	t.Helper()
	response, err := http.Get(url) // #nosec G107 -- httptest capability URL.
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), expected) {
		t.Fatalf("preview response = %d %q", response.StatusCode, body)
	}
}

func writePreviewFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
