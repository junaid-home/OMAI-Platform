package osfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omai/backend/internal/domain"
)

const maxWorkspaceWatchPaths = 256

type workspaceWatchTarget struct {
	absolute  string
	relative  string
	directory bool
}

func (w *Workspaces) WatchFiles(ctx context.Context, principal domain.Principal, workspaceID string, paths []string) (<-chan domain.FileChange, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	targets, err := workspaceWatchTargets(workspace.Root, paths)
	if err != nil {
		return nil, err
	}
	return watchWorkspace(ctx, workspace.Root, targets)
}

func workspaceWatchTargets(root string, paths []string) ([]workspaceWatchTarget, error) {
	if len(paths) == 0 || len(paths) > maxWorkspaceWatchPaths {
		return nil, fmt.Errorf("%w: watch requires between 1 and %d paths", domain.ErrInvalid, maxWorkspaceWatchPaths)
	}
	unique := make(map[string]workspaceWatchTarget, len(paths))
	for _, relative := range paths {
		relative = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relative)))
		if relative == "." {
			relative = ""
		}
		if ignoredWorkspaceWatchPath(relative) {
			continue
		}
		absolute, err := secureExistingPath(root, filepath.FromSlash(relative))
		if err != nil {
			if !errors.Is(err, domain.ErrNotFound) {
				return nil, err
			}
			absolute, err = lexicalPath(root, filepath.FromSlash(relative))
			if err != nil {
				return nil, err
			}
			if err := validateWriteAncestor(root, filepath.Dir(filepath.FromSlash(relative))); err != nil {
				return nil, err
			}
			unique[relative] = workspaceWatchTarget{absolute: absolute, relative: relative}
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, classifyPathError(err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: watched path must be a regular file or directory", domain.ErrInvalid)
		}
		unique[relative] = workspaceWatchTarget{absolute: absolute, relative: relative, directory: info.IsDir()}
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("%w: watch contains no eligible paths", domain.ErrInvalid)
	}
	targets := make([]workspaceWatchTarget, 0, len(unique))
	for _, target := range unique {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left].relative < targets[right].relative })
	return targets, nil
}

func ignoredWorkspaceWatchPath(path string) bool {
	path = filepath.ToSlash(path)
	for _, part := range strings.Split(path, "/") {
		if part == ".git" || strings.HasPrefix(part, ".omai-write-") {
			return true
		}
	}
	return false
}
