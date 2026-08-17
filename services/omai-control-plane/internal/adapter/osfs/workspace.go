package osfs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type Workspaces struct {
	mu           sync.RWMutex
	opsMu        sync.Mutex
	roots        []string
	maxFileBytes int64
	items        map[string]domain.Workspace
}

func NewWorkspaces(roots []string, maxFileBytes int64) *Workspaces {
	return &Workspaces{
		roots: append([]string(nil), roots...), maxFileBytes: maxFileBytes,
		items: make(map[string]domain.Workspace),
	}
}

func (w *Workspaces) Resolve(ctx context.Context, principal domain.Principal, root string) (domain.Workspace, error) {
	if principal.TenantID == "" || strings.TrimSpace(root) == "" {
		return domain.Workspace{}, fmt.Errorf("%w: tenant and root are required", domain.ErrInvalid)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("%w: resolve workspace root", domain.ErrInvalid)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("%w: workspace root does not exist", domain.ErrNotFound)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return domain.Workspace{}, fmt.Errorf("%w: workspace root is not a directory", domain.ErrInvalid)
	}
	if !w.allowed(resolved) {
		return domain.Workspace{}, fmt.Errorf("%w: workspace root is outside configured roots", domain.ErrForbidden)
	}
	id := workspaceID(principal.TenantID, resolved)
	key := principal.TenantID + "\x00" + id
	w.mu.Lock()
	defer w.mu.Unlock()
	if workspace, ok := w.items[key]; ok {
		workspace.UpdatedAt = time.Now().UTC()
		workspace.RepoRoot = repositoryRoot(resolved)
		w.items[key] = workspace
		return workspace, nil
	}
	now := time.Now().UTC()
	workspace := domain.Workspace{
		ID: id, TenantID: principal.TenantID, NodeID: "local", Root: resolved,
		RepoRoot: repositoryRoot(resolved), CreatedAt: now, UpdatedAt: now,
	}
	w.items[key] = workspace
	return workspace, nil
}

func (w *Workspaces) Get(_ context.Context, principal domain.Principal, id string) (domain.Workspace, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	workspace, ok := w.items[principal.TenantID+"\x00"+id]
	if !ok {
		return domain.Workspace{}, domain.ErrNotFound
	}
	return workspace, nil
}

func (w *Workspaces) List(_ context.Context, principal domain.Principal) ([]domain.Workspace, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	result := make([]domain.Workspace, 0)
	for _, workspace := range w.items {
		if workspace.TenantID == principal.TenantID {
			result = append(result, workspace)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Root < result[right].Root })
	return result, nil
}

func (w *Workspaces) ListFiles(ctx context.Context, principal domain.Principal, workspaceID, relative string) ([]domain.FileEntry, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	path, err := secureExistingPath(workspace.Root, relative)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, classifyPathError(err)
	}
	result := make([]domain.FileEntry, 0, len(entries))
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		info, err := entry.Info()
		if err != nil {
			return nil, classifyPathError(err)
		}
		entryPath := filepath.Join(path, entry.Name())
		rel, err := filepath.Rel(workspace.Root, entryPath)
		if err != nil {
			return nil, fmt.Errorf("relative file path: %w", err)
		}
		result = append(result, domain.FileEntry{
			Name: entry.Name(), Path: filepath.ToSlash(rel), Directory: entry.IsDir(),
			Size: info.Size(), ModifiedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Directory != result[right].Directory {
			return result[left].Directory
		}
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	return result, nil
}

func (w *Workspaces) ReadFile(ctx context.Context, principal domain.Principal, workspaceID, relative string) (domain.FileContent, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	path, err := secureExistingPath(workspace.Root, relative)
	if err != nil {
		return domain.FileContent{}, err
	}
	return readRegularFile(path, w.maxFileBytes)
}

func readRegularFile(path string, maxFileBytes int64) (domain.FileContent, error) {
	// #nosec G304 -- secureExistingPath proves containment and resolves symlinks.
	file, err := os.Open(path)
	if err != nil {
		return domain.FileContent{}, classifyPathError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.FileContent{}, classifyPathError(err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return domain.FileContent{}, fmt.Errorf("%w: file is not regular or exceeds %d bytes", domain.ErrInvalid, maxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return domain.FileContent{}, fmt.Errorf("read file: %w", err)
	}
	if int64(len(data)) > maxFileBytes {
		return domain.FileContent{}, fmt.Errorf("%w: file exceeds %d bytes", domain.ErrInvalid, maxFileBytes)
	}
	info, err = file.Stat()
	if err != nil {
		return domain.FileContent{}, classifyPathError(err)
	}
	return domain.FileContent{
		Data: data, Revision: contentRevision(data), Size: int64(len(data)), ModifiedAt: info.ModTime().UTC(),
	}, nil
}

func (w *Workspaces) WriteFile(ctx context.Context, principal domain.Principal, workspaceID, relative string, data []byte, options domain.WriteFileOptions) (domain.FileContent, error) {
	if int64(len(data)) > w.maxFileBytes {
		return domain.FileContent{}, fmt.Errorf("%w: file exceeds %d bytes", domain.ErrInvalid, w.maxFileBytes)
	}
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	path, err := secureWritePath(workspace.Root, relative)
	if err != nil {
		return domain.FileContent{}, err
	}
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.FileContent{}, err
	}
	parent := filepath.Dir(path)
	if err := validateWriteAncestor(workspace.Root, filepath.Dir(relative)); err != nil {
		return domain.FileContent{}, err
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return domain.FileContent{}, fmt.Errorf("create parent directory: %w", err)
	}
	if _, err := secureExistingPath(workspace.Root, filepath.Dir(relative)); err != nil {
		return domain.FileContent{}, err
	}
	if err := checkExpectedRevision(path, w.maxFileBytes, options.ExpectedRevision, options.RequireRevisionMatch); err != nil {
		return domain.FileContent{}, err
	}
	mode := fs.FileMode(0o600)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return domain.FileContent{}, fmt.Errorf("%w: refusing to replace a symlink", domain.ErrForbidden)
		}
		if !info.Mode().IsRegular() {
			return domain.FileContent{}, fmt.Errorf("%w: write target is not a regular file", domain.ErrInvalid)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.FileContent{}, classifyPathError(err)
	}
	temporary, err := os.CreateTemp(parent, ".omai-write-*")
	if err != nil {
		return domain.FileContent{}, fmt.Errorf("create temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return domain.FileContent{}, fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return domain.FileContent{}, fmt.Errorf("write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return domain.FileContent{}, fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return domain.FileContent{}, fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return domain.FileContent{}, fmt.Errorf("publish file: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return domain.FileContent{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return domain.FileContent{}, classifyPathError(err)
	}
	return domain.FileContent{
		Data: append([]byte(nil), data...), Revision: contentRevision(data), Size: int64(len(data)), ModifiedAt: info.ModTime().UTC(),
	}, nil
}

func (w *Workspaces) MovePath(ctx context.Context, principal domain.Principal, workspaceID, sourceRelative, destinationRelative string, options domain.MovePathOptions) (domain.FileContent, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.FileContent{}, err
	}
	source, sourceInfo, err := secureMutableExistingPath(workspace.Root, sourceRelative)
	if err != nil {
		return domain.FileContent{}, err
	}
	destination, err := secureWritePath(workspace.Root, destinationRelative)
	if err != nil {
		return domain.FileContent{}, err
	}
	if filepath.Clean(source) == filepath.Clean(destination) {
		return pathContent(source, sourceInfo, w.maxFileBytes)
	}
	if err := checkExpectedRevision(source, w.maxFileBytes, options.ExpectedRevision, options.RequireRevisionMatch); err != nil {
		return domain.FileContent{}, err
	}
	if err := validateWriteAncestor(workspace.Root, filepath.Dir(destinationRelative)); err != nil {
		return domain.FileContent{}, err
	}
	if _, err := secureExistingPath(workspace.Root, filepath.Dir(destinationRelative)); err != nil {
		return domain.FileContent{}, err
	}
	if destinationInfo, statErr := os.Lstat(destination); statErr == nil {
		if destinationInfo.Mode()&os.ModeSymlink != 0 {
			return domain.FileContent{}, fmt.Errorf("%w: refusing to replace a symlink", domain.ErrForbidden)
		}
		if !options.Overwrite {
			return domain.FileContent{}, fmt.Errorf("%w: destination already exists", domain.ErrConflict)
		}
		if !sourceInfo.Mode().IsRegular() || !destinationInfo.Mode().IsRegular() {
			return domain.FileContent{}, fmt.Errorf("%w: overwrite is supported only for regular files", domain.ErrInvalid)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return domain.FileContent{}, classifyPathError(statErr)
	}
	if err := os.Rename(source, destination); err != nil {
		return domain.FileContent{}, fmt.Errorf("move workspace path: %w", err)
	}
	if err := syncDirectory(filepath.Dir(source)); err != nil {
		return domain.FileContent{}, err
	}
	if filepath.Clean(filepath.Dir(source)) != filepath.Clean(filepath.Dir(destination)) {
		if err := syncDirectory(filepath.Dir(destination)); err != nil {
			return domain.FileContent{}, err
		}
	}
	info, err := os.Stat(destination)
	if err != nil {
		return domain.FileContent{}, classifyPathError(err)
	}
	return pathContent(destination, info, w.maxFileBytes)
}

func (w *Workspaces) DeletePath(ctx context.Context, principal domain.Principal, workspaceID, relative string, options domain.DeletePathOptions) error {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, info, err := secureMutableExistingPath(workspace.Root, relative)
	if err != nil {
		return err
	}
	if err := checkExpectedRevision(path, w.maxFileBytes, options.ExpectedRevision, options.RequireRevisionMatch); err != nil {
		return err
	}
	if info.IsDir() && options.RequireRevisionMatch {
		return fmt.Errorf("%w: directory revisions are not supported", domain.ErrInvalid)
	}
	if info.IsDir() && options.Recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		if info.IsDir() {
			return fmt.Errorf("%w: directory is not empty", domain.ErrConflict)
		}
		return classifyPathError(err)
	}
	return syncDirectory(filepath.Dir(path))
}

func (w *Workspaces) CreateDirectory(ctx context.Context, principal domain.Principal, workspaceID, relative string) error {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	path, err := secureWritePath(workspace.Root, relative)
	if err != nil {
		return err
	}
	if err := validateWriteAncestor(workspace.Root, relative); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: refusing a symlink directory", domain.ErrForbidden)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: directory path is not a directory", domain.ErrInvalid)
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return classifyPathError(statErr)
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	if _, err := secureExistingPath(workspace.Root, relative); err != nil {
		return err
	}
	return nil
}

func (w *Workspaces) SearchFiles(ctx context.Context, principal domain.Principal, workspaceID, query string, kind domain.FileSearchKind, limit int) ([]string, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("%w: search query is required", domain.ErrInvalid)
	}
	limit = boundedLimit(limit)
	result := make([]string, 0, limit)
	err = filepath.WalkDir(workspace.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && path != workspace.Root && skippedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		relative, _ := filepath.Rel(workspace.Root, path)
		if entry.IsDir() {
			if kind == domain.FileSearchFiles {
				return nil
			}
		} else if kind == domain.FileSearchDirectories {
			return nil
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(relative)), query) {
			result = append(result, filepath.ToSlash(relative))
		}
		if len(result) >= limit {
			return errLimitReached
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return nil, fmt.Errorf("search paths: %w", err)
	}
	return result, nil
}

func (w *Workspaces) SearchText(ctx context.Context, principal domain.Principal, workspaceID, query string, limit int) ([]domain.SearchMatch, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: search query is required", domain.ErrInvalid)
	}
	limit = boundedLimit(limit)
	result := make([]domain.SearchMatch, 0, limit)
	err = filepath.WalkDir(workspace.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && path != workspace.Root && skippedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > w.maxFileBytes {
			return nil
		}
		matches, err := searchTextFile(path, query, limit-len(result))
		if err != nil {
			return nil
		}
		relative, _ := filepath.Rel(workspace.Root, path)
		for index := range matches {
			matches[index].Path = filepath.ToSlash(relative)
		}
		result = append(result, matches...)
		if len(result) >= limit {
			return errLimitReached
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return nil, fmt.Errorf("search text: %w", err)
	}
	return result, nil
}

var errLimitReached = errors.New("search limit reached")

func searchTextFile(path, query string, remaining int) ([]domain.SearchMatch, error) {
	if remaining <= 0 {
		return nil, nil
	}
	// #nosec G304 -- path originates from WalkDir beneath the tenant workspace;
	// symlink entries are rejected by the caller before this helper is invoked.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	prefix := make([]byte, 8192)
	read, _ := file.Read(prefix)
	if bytes.IndexByte(prefix[:read], 0) >= 0 {
		return nil, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []domain.SearchMatch
	for line := int32(1); scanner.Scan(); line++ {
		if strings.Contains(scanner.Text(), query) {
			result = append(result, domain.SearchMatch{Line: line, Text: scanner.Text()})
			if len(result) >= remaining {
				break
			}
		}
	}
	return result, scanner.Err()
}

func (w *Workspaces) allowed(path string) bool {
	for _, root := range w.roots {
		if within(root, path) {
			return true
		}
	}
	return false
}

func secureExistingPath(root, relative string) (string, error) {
	candidate, err := lexicalPath(root, relative)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", classifyPathError(err)
	}
	if !within(root, resolved) {
		return "", fmt.Errorf("%w: symlink escapes workspace", domain.ErrForbidden)
	}
	return resolved, nil
}

func secureMutableExistingPath(root, relative string) (string, fs.FileInfo, error) {
	candidate, err := secureWritePath(root, relative)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", nil, classifyPathError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%w: refusing to mutate a symlink", domain.ErrForbidden)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, classifyPathError(err)
	}
	if !within(root, resolved) {
		return "", nil, fmt.Errorf("%w: path escapes workspace", domain.ErrForbidden)
	}
	info, err = os.Stat(resolved)
	if err != nil {
		return "", nil, classifyPathError(err)
	}
	return resolved, info, nil
}

func secureWritePath(root, relative string) (string, error) {
	candidate, err := lexicalPath(root, relative)
	if err != nil {
		return "", err
	}
	if filepath.Clean(candidate) == filepath.Clean(root) {
		return "", fmt.Errorf("%w: file path is required", domain.ErrInvalid)
	}
	return candidate, nil
}

func validateWriteAncestor(root, relativeParent string) error {
	current := filepath.Clean(relativeParent)
	for {
		candidate, err := lexicalPath(root, current)
		if err != nil {
			return err
		}
		info, err := os.Lstat(candidate)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, resolveErr := filepath.EvalSymlinks(candidate)
				if resolveErr != nil || !within(root, resolved) {
					return fmt.Errorf("%w: parent symlink escapes workspace", domain.ErrForbidden)
				}
			}
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("%w: parent path is not a directory", domain.ErrInvalid)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return classifyPathError(err)
		}
		if current == "." || current == string(filepath.Separator) {
			return nil
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
		current = next
	}
}

func lexicalPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("%w: absolute or invalid path", domain.ErrInvalid)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path traversal", domain.ErrForbidden)
	}
	candidate := filepath.Join(root, cleaned)
	if !within(root, candidate) {
		return "", fmt.Errorf("%w: path escapes workspace", domain.ErrForbidden)
	}
	return candidate, nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func checkExpectedRevision(path string, maxFileBytes int64, expected string, required bool) error {
	if !required {
		if expected != "" {
			return fmt.Errorf("%w: expected revision requires revision matching", domain.ErrInvalid)
		}
		return nil
	}
	if expected != "" && !validRevision(expected) {
		return fmt.Errorf("%w: expected revision is invalid", domain.ErrInvalid)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if expected == "" {
			return nil
		}
		return fmt.Errorf("%w: file no longer exists", domain.ErrStaleRevision)
	}
	if err != nil {
		return classifyPathError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: refusing to compare a symlink revision", domain.ErrForbidden)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: revisions are supported only for regular files", domain.ErrInvalid)
	}
	content, err := readRegularFile(path, maxFileBytes)
	if err != nil {
		return err
	}
	if expected == "" {
		return fmt.Errorf("%w: file already exists", domain.ErrConflict)
	}
	if content.Revision != expected {
		return fmt.Errorf("%w: file changed", domain.ErrStaleRevision)
	}
	return nil
}

func pathContent(path string, info fs.FileInfo, maxFileBytes int64) (domain.FileContent, error) {
	if info.Mode().IsRegular() {
		return readRegularFile(path, maxFileBytes)
	}
	return domain.FileContent{Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

func contentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validRevision(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size
}

func syncDirectory(path string) error {
	// #nosec G304 -- callers pass a validated workspace-contained directory.
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open parent directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}

func workspaceID(tenant, root string) string {
	sum := sha256.Sum256([]byte(tenant + "\x00" + filepath.Clean(root)))
	return "wsp_" + hex.EncodeToString(sum[:16])
}

func repositoryRoot(root string) string {
	metadata := filepath.Join(root, ".git")
	info, err := os.Lstat(metadata)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if info.IsDir() {
		return root
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return ""
	}
	// #nosec G304 -- metadata is the fixed .git child of a validated workspace root.
	file, err := os.Open(metadata)
	if err != nil {
		return ""
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > 4096 {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || !within(root, resolved) {
		return ""
	}
	return root
}

func classifyPathError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return domain.ErrNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return domain.ErrForbidden
	}
	return err
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".next", "dist", "build", ".cache":
		return true
	default:
		return false
	}
}

func boundedLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}
