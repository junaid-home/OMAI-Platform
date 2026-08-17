//go:build !linux

package osfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/omai/backend/internal/domain"
)

const maxFallbackWatchEntries = 8192

type fallbackWatchMeta struct {
	directory bool
	size      int64
	modified  int64
	mode      os.FileMode
}

func watchWorkspace(ctx context.Context, _ string, targets []workspaceWatchTarget) (<-chan domain.FileChange, error) {
	initial, _, err := fallbackSnapshot(targets)
	if err != nil {
		return nil, err
	}
	updates := make(chan domain.FileChange, 512)
	go func() {
		defer close(updates)
		previous := initial
		var sequence uint64
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		emit := func(path string, kind domain.FileChangeKind) {
			if path == "" && kind != domain.FileChangeResync {
				kind = domain.FileChangeResync
			}
			sequence++
			select {
			case updates <- domain.FileChange{Sequence: sequence, Path: path, Kind: kind}:
			case <-ctx.Done():
			default:
			}
		}
		// Match the Linux contract: the initial resync is also the readiness
		// handshake for callers using the private executor stream.
		emit("", domain.FileChangeResync)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, exceeded, err := fallbackSnapshot(targets)
				if err != nil || exceeded {
					emit("", domain.FileChangeResync)
					continue
				}
				for path, value := range current {
					old, exists := previous[path]
					if !exists {
						emit(path, domain.FileChangeAdd)
					} else if old != value {
						emit(path, domain.FileChangeChange)
					}
				}
				for path := range previous {
					if _, exists := current[path]; !exists {
						emit(path, domain.FileChangeUnlink)
					}
				}
				previous = current
			}
		}
	}()
	return updates, nil
}

func fallbackSnapshot(targets []workspaceWatchTarget) (map[string]fallbackWatchMeta, bool, error) {
	result := make(map[string]fallbackWatchMeta)
	add := func(path string, info os.FileInfo) bool {
		result[path] = fallbackWatchMeta{directory: info.IsDir(), size: info.Size(), modified: info.ModTime().UnixNano(), mode: info.Mode()}
		return len(result) <= maxFallbackWatchEntries
	}
	for _, target := range targets {
		info, err := os.Stat(target.absolute)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, false, classifyPathError(err)
		}
		if !add(target.relative, info) {
			return result, true, nil
		}
		if !target.directory {
			continue
		}
		entries, err := os.ReadDir(target.absolute)
		if err != nil {
			return nil, false, classifyPathError(err)
		}
		for _, entry := range entries {
			path := filepath.ToSlash(filepath.Join(target.relative, entry.Name()))
			if ignoredWorkspaceWatchPath(path) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return nil, false, classifyPathError(err)
			}
			if !add(path, info) {
				return result, true, nil
			}
		}
	}
	return result, false, nil
}
