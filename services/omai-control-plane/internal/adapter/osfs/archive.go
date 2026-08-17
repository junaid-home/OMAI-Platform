package osfs

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omai/backend/internal/domain"
)

const (
	maxArchiveEntries         = 100_000
	maxArchiveExpandedBytes   = int64(1 << 30)
	maxArchiveSingleFileBytes = int64(256 << 20)
)

type archiveEntry struct {
	file *zip.File
	path string
}

func (w *Workspaces) ImportArchive(ctx context.Context, principal domain.Principal, workspaceID string, data []byte, options domain.ArchiveImportOptions) (domain.ArchiveImportResult, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.ArchiveImportResult{}, err
	}
	entries, result, err := validatedArchive(data, options)
	if err != nil {
		return domain.ArchiveImportResult{}, err
	}
	w.opsMu.Lock()
	defer w.opsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.ArchiveImportResult{}, err
	}
	existing, err := os.ReadDir(workspace.Root)
	if err != nil {
		return domain.ArchiveImportResult{}, classifyPathError(err)
	}
	if len(existing) != 0 {
		return domain.ArchiveImportResult{}, fmt.Errorf("%w: archive import requires an empty workspace", domain.ErrConflict)
	}
	staging, err := os.MkdirTemp(workspace.Root, ".omai-import-")
	if err != nil {
		return domain.ArchiveImportResult{}, fmt.Errorf("create archive staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := extractArchive(ctx, staging, entries); err != nil {
		return domain.ArchiveImportResult{}, err
	}
	rootEntries, err := os.ReadDir(workspace.Root)
	if err != nil {
		return domain.ArchiveImportResult{}, classifyPathError(err)
	}
	if len(rootEntries) != 1 || filepath.Join(workspace.Root, rootEntries[0].Name()) != staging {
		return domain.ArchiveImportResult{}, fmt.Errorf("%w: workspace changed while archive was importing", domain.ErrConflict)
	}
	stagedEntries, err := os.ReadDir(staging)
	if err != nil {
		return domain.ArchiveImportResult{}, classifyPathError(err)
	}
	moved := make([]string, 0, len(stagedEntries))
	for _, entry := range stagedEntries {
		if err := ctx.Err(); err != nil {
			rollbackArchiveMoves(workspace.Root, staging, moved)
			return domain.ArchiveImportResult{}, err
		}
		name := entry.Name()
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(workspace.Root, name)); err != nil {
			rollbackArchiveMoves(workspace.Root, staging, moved)
			return domain.ArchiveImportResult{}, fmt.Errorf("publish archive entry %q: %w", name, err)
		}
		moved = append(moved, name)
	}
	if err := os.Remove(staging); err != nil {
		return domain.ArchiveImportResult{}, fmt.Errorf("remove archive staging directory: %w", err)
	}
	if err := syncDirectory(workspace.Root); err != nil {
		return domain.ArchiveImportResult{}, err
	}
	return result, nil
}

func (w *Workspaces) ExportArchive(ctx context.Context, principal domain.Principal, workspaceID string) (io.ReadCloser, error) {
	workspace, err := w.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	w.opsMu.Lock()
	reader, writer := io.Pipe()
	go func() {
		defer w.opsMu.Unlock()
		archive := zip.NewWriter(writer)
		writeErr := writeWorkspaceArchive(ctx, archive, workspace.Root)
		if closeErr := archive.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
	}()
	return reader, nil
}

func validatedArchive(data []byte, options domain.ArchiveImportOptions) ([]archiveEntry, domain.ArchiveImportResult, error) {
	if len(data) == 0 {
		return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive is empty", domain.ErrInvalid)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: invalid ZIP archive: %v", domain.ErrInvalid, err)
	}
	if len(archive.File) == 0 || len(archive.File) > maxArchiveEntries {
		return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive entry count is outside the allowed range", domain.ErrInvalid)
	}
	entries := make([]archiveEntry, 0, len(archive.File))
	for _, file := range archive.File {
		cleaned, err := cleanArchivePath(file.Name)
		if err != nil {
			return nil, domain.ArchiveImportResult{}, err
		}
		if file.Mode()&os.ModeSymlink != 0 || file.Mode()&os.ModeType != 0 && !file.FileInfo().IsDir() {
			return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive contains an unsupported special file", domain.ErrInvalid)
		}
		entries = append(entries, archiveEntry{file: file, path: cleaned})
	}
	if options.StripSingleRoot {
		entries = stripArchiveRoot(entries)
	}
	seen := make(map[string]struct{}, len(entries))
	files := make(map[string]struct{}, len(entries))
	directories := make(map[string]struct{})
	result := domain.ArchiveImportResult{}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.path == "." {
			continue
		}
		if _, ok := seen[entry.path]; ok {
			return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive contains duplicate path %q", domain.ErrInvalid, entry.path)
		}
		seen[entry.path] = struct{}{}
		if entry.file.FileInfo().IsDir() {
			directories[entry.path] = struct{}{}
		} else {
			size := int64(entry.file.UncompressedSize64)
			if size < 0 || size > maxArchiveSingleFileBytes || result.Bytes > maxArchiveExpandedBytes-size {
				return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive expands beyond its allowed size", domain.ErrInvalid)
			}
			result.Files++
			result.Bytes += size
			files[entry.path] = struct{}{}
			for parent := path.Dir(entry.path); parent != "."; parent = path.Dir(parent) {
				directories[parent] = struct{}{}
			}
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) == 0 {
		return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive has no importable entries", domain.ErrInvalid)
	}
	for _, entry := range filtered {
		for parent := path.Dir(entry.path); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return nil, domain.ArchiveImportResult{}, fmt.Errorf("%w: ZIP archive uses file %q as a directory", domain.ErrInvalid, parent)
			}
		}
	}
	result.Dirs = int64(len(directories))
	return filtered, result, nil
}

func cleanArchivePath(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: ZIP archive contains an absolute or invalid path", domain.ErrInvalid)
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: ZIP archive path escapes the workspace", domain.ErrForbidden)
	}
	return cleaned, nil
}

func stripArchiveRoot(entries []archiveEntry) []archiveEntry {
	if len(entries) == 0 {
		return entries
	}
	root := strings.SplitN(entries[0].path, "/", 2)[0]
	if root == "." || root == "" {
		return entries
	}
	hasNested := false
	for _, entry := range entries {
		if entry.path != root && !strings.HasPrefix(entry.path, root+"/") {
			return entries
		}
		if strings.HasPrefix(entry.path, root+"/") {
			hasNested = true
		}
		if entry.path == root && !entry.file.FileInfo().IsDir() {
			return entries
		}
	}
	if !hasNested {
		return entries
	}
	result := make([]archiveEntry, 0, len(entries))
	for _, entry := range entries {
		entry.path = strings.TrimPrefix(strings.TrimPrefix(entry.path, root), "/")
		if entry.path == "" {
			entry.path = "."
		}
		result = append(result, entry)
	}
	return result
}

func extractArchive(ctx context.Context, root string, entries []archiveEntry) error {
	sort.SliceStable(entries, func(left, right int) bool {
		return strings.Count(entries[left].path, "/") < strings.Count(entries[right].path, "/")
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(entry.path))
		if entry.file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("create archive directory %q: %w", entry.path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("create archive parent %q: %w", entry.path, err)
		}
		mode := fs.FileMode(0o600)
		if entry.file.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return fmt.Errorf("create archive file %q: %w", entry.path, err)
		}
		source, err := entry.file.Open()
		if err != nil {
			_ = destination.Close()
			return fmt.Errorf("open archive file %q: %w", entry.path, err)
		}
		_, copyErr := copyWithContext(ctx, destination, source)
		closeSourceErr := source.Close()
		syncErr := destination.Sync()
		closeDestinationErr := destination.Close()
		if err := errors.Join(copyErr, closeSourceErr, syncErr, closeDestinationErr); err != nil {
			return fmt.Errorf("extract archive file %q: %w", entry.path, err)
		}
		if modified := entry.file.Modified; !modified.IsZero() {
			if err := os.Chtimes(target, modified, modified); err != nil {
				return fmt.Errorf("set archive file time %q: %w", entry.path, err)
			}
		}
	}
	return nil
}

func rollbackArchiveMoves(root, staging string, moved []string) {
	for index := len(moved) - 1; index >= 0; index-- {
		name := moved[index]
		_ = os.Rename(filepath.Join(root, name), filepath.Join(staging, name))
	}
}

func writeWorkspaceArchive(ctx context.Context, archive *zip.Writer, root string) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && excludedArchiveDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// #nosec G304 -- filePath originates from WalkDir beneath the validated workspace root.
		file, err := os.Open(filePath)
		if err != nil {
			return err
		}
		_, copyErr := copyWithContext(ctx, writer, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
}

func excludedArchiveDirectory(name string) bool {
	if strings.HasPrefix(name, ".omai-import-") {
		return true
	}
	switch name {
	case ".git", "node_modules", ".next", "dist", "build", ".cache":
		return true
	default:
		return false
	}
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
