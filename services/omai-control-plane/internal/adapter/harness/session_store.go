package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxSessionMappings = 100_000

type FileSessionStore struct {
	mu       sync.RWMutex
	path     string
	sessions map[string]string
}

func NewFileSessionStore(path string) (*FileSessionStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("harness session store path must be absolute")
	}
	store := &FileSessionStore{path: filepath.Clean(path), sessions: make(map[string]string)}
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect harness session store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("harness session store must be a bounded regular file")
	}
	// #nosec G304 -- path is absolute operator-owned executor configuration and
	// symlinks were rejected with Lstat immediately above.
	file, err := os.Open(store.path)
	if err != nil {
		return nil, fmt.Errorf("open harness session store: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20))
	if err := decoder.Decode(&store.sessions); err != nil {
		return nil, fmt.Errorf("decode harness session store: %w", err)
	}
	if len(store.sessions) > maxSessionMappings {
		return nil, errors.New("harness session store exceeds mapping limit")
	}
	for external, internal := range store.sessions {
		if !validSessionIdentifier(external) || !validSessionIdentifier(internal) {
			return nil, errors.New("harness session store contains an invalid identifier")
		}
	}
	return store, nil
}

func (s *FileSessionStore) Get(external string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.sessions[external]
	return value, ok
}

func (s *FileSessionStore) Put(external, internal string) error {
	if !validSessionIdentifier(external) || !validSessionIdentifier(internal) {
		return errors.New("invalid harness session identifier")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.sessions[external]; exists {
		if current != internal {
			return ErrSessionMismatch
		}
		return nil
	}
	if len(s.sessions) >= maxSessionMappings {
		return errors.New("harness session mapping limit reached")
	}
	s.sessions[external] = internal
	if err := s.persist(); err != nil {
		delete(s.sessions, external)
		return err
	}
	return nil
}

func (s *FileSessionStore) persist() error {
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create harness state directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".harness-sessions-*.json")
	if err != nil {
		return fmt.Errorf("create harness session snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(s.sessions); err != nil {
		cleanup()
		return fmt.Errorf("encode harness session snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace harness session snapshot: %w", err)
	}
	return nil
}

func validSessionIdentifier(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
