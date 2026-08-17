package osfs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/omai/backend/internal/domain"
)

func TestWorkspaceArchiveImportAndExport(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1<<20)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	archive := testZip(t, []testZipEntry{
		{name: "project/"},
		{name: "project/main.go", data: "package main\n"},
		{name: "project/scripts/run.sh", data: "#!/bin/sh\n", executable: true},
	})
	result, err := store.ImportArchive(context.Background(), principal, workspace.ID, archive, domain.ArchiveImportOptions{StripSingleRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 2 || result.Dirs != 1 || result.Bytes != int64(len("package main\n#!/bin/sh\n")) {
		t.Fatalf("unexpected import result: %#v", result)
	}
	content, err := store.ReadFile(context.Background(), principal, workspace.ID, "main.go")
	if err != nil || string(content.Data) != "package main\n" {
		t.Fatalf("unexpected imported file: %#v, %v", content, err)
	}
	info, err := os.Stat(filepath.Join(root, "scripts", "run.sh"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable mode was not preserved: %#v, %v", info, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "ignored", "index.js"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	exported, err := store.ExportArchive(context.Background(), principal, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	exportedData, err := io.ReadAll(exported)
	closeErr := exported.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read exported archive: read=%v close=%v", err, closeErr)
	}
	reader, err := zip.NewReader(bytes.NewReader(exportedData), int64(len(exportedData)))
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(reader.File))
	for _, file := range reader.File {
		names[file.Name] = true
	}
	if !names["main.go"] || !names["scripts/run.sh"] || names["node_modules/ignored/index.js"] {
		t.Fatalf("unexpected exported paths: %#v", names)
	}
}

func TestWorkspaceArchiveRejectsTraversalAndSymlinks(t *testing.T) {
	for _, test := range []struct {
		name    string
		archive func(*testing.T) []byte
		want    error
	}{
		{
			name: "traversal",
			archive: func(t *testing.T) []byte {
				return testZip(t, []testZipEntry{{name: "../escape.txt", data: "bad"}})
			},
			want: domain.ErrForbidden,
		},
		{
			name: "symlink",
			archive: func(t *testing.T) []byte {
				var buffer bytes.Buffer
				writer := zip.NewWriter(&buffer)
				header := &zip.FileHeader{Name: "link"}
				header.SetMode(os.ModeSymlink | 0o777)
				entry, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("../outside")); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				return buffer.Bytes()
			},
			want: domain.ErrInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewWorkspaces([]string{root}, 1<<20)
			principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
			workspace, err := store.Resolve(context.Background(), principal, root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ImportArchive(context.Background(), principal, workspace.ID, test.archive(t), domain.ArchiveImportOptions{}); !errors.Is(err, test.want) {
				t.Fatalf("archive import error = %v; want %v", err, test.want)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected archive modified workspace: entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestWorkspaceArchiveDoesNotStripASingleFile(t *testing.T) {
	root := t.TempDir()
	store := NewWorkspaces([]string{root}, 1<<20)
	principal := domain.Principal{TenantID: "tenant", ActorID: "actor"}
	workspace, err := store.Resolve(context.Background(), principal, root)
	if err != nil {
		t.Fatal(err)
	}
	archive := testZip(t, []testZipEntry{{name: "README.md", data: "OMAI\n"}})
	if _, err := store.ImportArchive(context.Background(), principal, workspace.ID, archive, domain.ArchiveImportOptions{StripSingleRoot: true}); err != nil {
		t.Fatal(err)
	}
	content, err := store.ReadFile(context.Background(), principal, workspace.ID, "README.md")
	if err != nil || string(content.Data) != "OMAI\n" {
		t.Fatalf("single-file archive was not preserved: %#v, %v", content, err)
	}
}

type testZipEntry struct {
	name       string
	data       string
	executable bool
}

func testZip(t *testing.T, entries []testZipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, value := range entries {
		header := &zip.FileHeader{Name: value.name, Method: zip.Deflate}
		if value.name[len(value.name)-1] == '/' {
			header.SetMode(os.ModeDir | 0o750)
		} else if value.executable {
			header.SetMode(0o700)
		} else {
			header.SetMode(0o600)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(value.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
