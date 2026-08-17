package projectdetector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestAnalyzeNodePlanIsDeterministicAndShellFree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), `{"scripts":{"dev":"vite --open; touch /tmp/never"},"devDependencies":{"vite":"7.1.4","solid-js":"1.9.10"}}`)
	writeTestFile(t, filepath.Join(root, "package-lock.json"), `{"lockfileVersion":3}`)
	detector := New()
	workspace := domain.Workspace{ID: "ws_test", Root: root}
	first, err := detector.Analyze(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	second, err := detector.Analyze(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Analyze() second error = %v", err)
	}
	if first.Fingerprint != second.Fingerprint || first.Primary != second.Primary || len(first.Services) != 1 {
		t.Fatalf("plan not deterministic: first=%+v second=%+v", first, second)
	}
	service := first.Services[0]
	if service.Run.Command != "npm" {
		t.Fatalf("command = %q, want npm", service.Run.Command)
	}
	want := []string{"run", "dev", "--", "--host", "{{host}}", "--port", "{{port}}", "--strictPort"}
	if len(service.Run.Args) != len(want) {
		t.Fatalf("args = %#v", service.Run.Args)
	}
	for index := range want {
		if service.Run.Args[index] != want[index] {
			t.Fatalf("args[%d] = %q, want %q", index, service.Run.Args[index], want[index])
		}
	}
}

func TestAnalyzeExplicitPlanRejectsTraversalAndUnknownFields(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"traversal": `{"version":1,"primary":"web","services":[{"id":"web","workingDir":"../outside","runtime":"node","run":{"command":"npm","args":["run","dev"]},"preview":true}]}`,
		"unknown":   `{"version":1,"primary":"web","surprise":true,"services":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, ".omai", "runtime.json"), body)
			if _, err := New().Analyze(context.Background(), domain.Workspace{ID: "ws_test", Root: root}); err == nil {
				t.Fatal("Analyze() accepted unsafe explicit runtime config")
			}
		})
	}
}

func TestAnalyzeStaticFallbackAndManifestFingerprint(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "index.html"), "<h1>one</h1>")
	workspace := domain.Workspace{ID: "ws_static", Root: root}
	detector := New()
	before, err := detector.Analyze(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if before.Services[0].Runtime != "static" || before.Services[0].Run.Command != "python3" {
		t.Fatalf("static service = %+v", before.Services[0])
	}
	writeTestFile(t, filepath.Join(root, "index.html"), "<h1>two</h1>")
	after, err := detector.Analyze(context.Background(), workspace)
	if err != nil {
		t.Fatalf("Analyze() after change error = %v", err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("runtime-relevant static manifest change did not update fingerprint")
	}
}

func TestAnalyzeIgnoresSymlinkedManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "package.json")
	writeTestFile(t, outside, `{"scripts":{"dev":"vite"}}`)
	if err := os.Symlink(outside, filepath.Join(root, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Analyze(context.Background(), domain.Workspace{ID: "ws_test", Root: root}); err == nil {
		t.Fatal("Analyze() followed a symlinked manifest")
	}
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
