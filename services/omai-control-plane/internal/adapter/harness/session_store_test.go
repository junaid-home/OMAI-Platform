package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileSessionStorePersistsMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "sessions.json")
	store, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put("portal-session", "ses_harness"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewFileSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := reloaded.Get("portal-session"); !ok || value != "ses_harness" {
		t.Fatalf("mapping was not persisted: %q, %v", value, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session mapping permissions are unsafe: %v, %v", info, err)
	}
}

func TestFileSessionStoreRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "sessions.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewFileSessionStore(link); err == nil {
		t.Fatal("symlink session store was accepted")
	}
}
