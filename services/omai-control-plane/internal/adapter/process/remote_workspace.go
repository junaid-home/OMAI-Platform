package process

import (
	"context"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect"
	executorv1 "github.com/omai/backend/gen/go/omai/executor/v1"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

// WorkspaceRepository returns a data-plane adapter that keeps public workspace
// identity in the control plane but delegates every file byte and search to the
// private sandbox executor.
func (r *Remote) WorkspaceRepository() port.WorkspaceRepository {
	return &remoteWorkspaces{remote: r}
}

type remoteWorkspaces struct{ remote *Remote }

func (r *remoteWorkspaces) Resolve(ctx context.Context, principal domain.Principal, root string) (domain.Workspace, error) {
	return r.remote.workspaces.Resolve(ctx, principal, root)
}

func (r *remoteWorkspaces) Get(ctx context.Context, principal domain.Principal, id string) (domain.Workspace, error) {
	return r.remote.workspaces.Get(ctx, principal, id)
}

func (r *remoteWorkspaces) List(ctx context.Context, principal domain.Principal) ([]domain.Workspace, error) {
	return r.remote.workspaces.List(ctx, principal)
}

func (r *remoteWorkspaces) ListFiles(ctx context.Context, principal domain.Principal, workspaceID, path string) ([]domain.FileEntry, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	response, err := r.remote.client.ListFiles(ctx, connect.NewRequest(&executorv1.ListFilesRequest{Identity: identity, Path: path}))
	if err != nil {
		return nil, remoteError(err)
	}
	result := make([]domain.FileEntry, 0, len(response.Msg.GetEntries()))
	for _, entry := range response.Msg.GetEntries() {
		if entry == nil || entry.GetName() == "" {
			return nil, fmt.Errorf("%w: executor returned an invalid file entry", domain.ErrUnavailable)
		}
		result = append(result, domain.FileEntry{
			Name: entry.GetName(), Path: entry.GetPath(), Directory: entry.GetDirectory(), Size: entry.GetSize(),
			ModifiedAt: time.UnixMilli(entry.GetModifiedUnixMillis()).UTC(),
		})
	}
	return result, nil
}

func (r *remoteWorkspaces) WatchFiles(ctx context.Context, principal domain.Principal, workspaceID string, paths []string) (<-chan domain.FileChange, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	stream, err := r.remote.client.WatchFiles(ctx, connect.NewRequest(&executorv1.WatchFilesRequest{
		Identity: identity,
		Paths:    append([]string(nil), paths...),
	}))
	if err != nil {
		return nil, remoteError(err)
	}
	updates := make(chan domain.FileChange, 512)
	go func() {
		defer close(updates)
		for stream.Receive() {
			value := stream.Msg()
			change := domain.FileChange{Sequence: value.GetSequence(), Path: value.GetPath(), Kind: remoteFileChangeKind(value.GetKind())}
			select {
			case updates <- change:
			case <-ctx.Done():
				return
			default:
				select {
				case <-updates:
				default:
				}
				select {
				case updates <- domain.FileChange{Sequence: value.GetSequence(), Kind: domain.FileChangeResync}:
				case <-ctx.Done():
				default:
				}
			}
		}
	}()
	return updates, nil
}

func (r *remoteWorkspaces) CreateDirectory(ctx context.Context, principal domain.Principal, workspaceID, path string) error {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	_, err = r.remote.client.CreateDirectory(ctx, connect.NewRequest(&executorv1.CreateDirectoryRequest{Identity: identity, Path: path}))
	return remoteError(err)
}

func (r *remoteWorkspaces) ReadFile(ctx context.Context, principal domain.Principal, workspaceID, path string) (domain.FileContent, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	response, err := r.remote.client.ReadFile(ctx, connect.NewRequest(&executorv1.ReadFileRequest{Identity: identity, Path: path}))
	if err != nil {
		return domain.FileContent{}, remoteError(err)
	}
	return executorFileContent(response.Msg.GetData(), response.Msg.GetRevision(), response.Msg.GetSize(), response.Msg.GetModifiedUnixMillis())
}

func (r *remoteWorkspaces) WriteFile(ctx context.Context, principal domain.Principal, workspaceID, path string, data []byte, options domain.WriteFileOptions) (domain.FileContent, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	response, err := r.remote.client.WriteFile(ctx, connect.NewRequest(&executorv1.WriteFileRequest{
		Identity: identity, Path: path, Data: append([]byte(nil), data...), ExpectedRevision: options.ExpectedRevision, RequireRevisionMatch: options.RequireRevisionMatch,
	}))
	if err != nil {
		return domain.FileContent{}, remoteError(err)
	}
	return executorFileContent(data, response.Msg.GetRevision(), response.Msg.GetSize(), response.Msg.GetModifiedUnixMillis())
}

func (r *remoteWorkspaces) MovePath(ctx context.Context, principal domain.Principal, workspaceID, source, destination string, options domain.MovePathOptions) (domain.FileContent, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return domain.FileContent{}, err
	}
	response, err := r.remote.client.MovePath(ctx, connect.NewRequest(&executorv1.MovePathRequest{
		Identity: identity, SourcePath: source, DestinationPath: destination, Overwrite: options.Overwrite,
		ExpectedRevision: options.ExpectedRevision, RequireRevisionMatch: options.RequireRevisionMatch,
	}))
	if err != nil {
		return domain.FileContent{}, remoteError(err)
	}
	return executorFileContent(nil, response.Msg.GetRevision(), response.Msg.GetSize(), response.Msg.GetModifiedUnixMillis())
}

func (r *remoteWorkspaces) DeletePath(ctx context.Context, principal domain.Principal, workspaceID, path string, options domain.DeletePathOptions) error {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return err
	}
	_, err = r.remote.client.DeletePath(ctx, connect.NewRequest(&executorv1.DeletePathRequest{
		Identity: identity, Path: path, Recursive: options.Recursive, ExpectedRevision: options.ExpectedRevision, RequireRevisionMatch: options.RequireRevisionMatch,
	}))
	return remoteError(err)
}

func (r *remoteWorkspaces) ImportArchive(ctx context.Context, principal domain.Principal, workspaceID string, data []byte, options domain.ArchiveImportOptions) (domain.ArchiveImportResult, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return domain.ArchiveImportResult{}, err
	}
	response, err := r.remote.client.ImportArchive(ctx, connect.NewRequest(&executorv1.ImportWorkspaceArchiveRequest{
		Identity: identity, Data: append([]byte(nil), data...), StripSingleRoot: options.StripSingleRoot,
	}))
	if err != nil {
		return domain.ArchiveImportResult{}, remoteError(err)
	}
	return domain.ArchiveImportResult{
		Files: response.Msg.GetFiles(), Dirs: response.Msg.GetDirectories(), Bytes: response.Msg.GetUncompressedBytes(),
	}, nil
}

func (r *remoteWorkspaces) ExportArchive(ctx context.Context, principal domain.Principal, workspaceID string) (io.ReadCloser, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	streamContext, cancel := context.WithCancel(ctx)
	stream, err := r.remote.client.ExportArchive(streamContext, connect.NewRequest(&executorv1.ExportWorkspaceArchiveRequest{Identity: identity}))
	if err != nil {
		cancel()
		return nil, remoteError(err)
	}
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		for stream.Receive() {
			if _, err := writer.Write(stream.Msg().GetData()); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		_ = writer.CloseWithError(remoteError(stream.Err()))
	}()
	return &remoteArchiveReader{PipeReader: reader, cancel: cancel}, nil
}

type remoteArchiveReader struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (r *remoteArchiveReader) Close() error {
	r.cancel()
	return r.PipeReader.Close()
}

func executorFileContent(data []byte, revision string, size, modifiedUnixMillis int64) (domain.FileContent, error) {
	if revision == "" && data != nil {
		return domain.FileContent{}, fmt.Errorf("%w: executor returned an empty file revision", domain.ErrUnavailable)
	}
	return domain.FileContent{
		Data: append([]byte(nil), data...), Revision: revision, Size: size, ModifiedAt: time.UnixMilli(modifiedUnixMillis).UTC(),
	}, nil
}

func (r *remoteWorkspaces) SearchFiles(ctx context.Context, principal domain.Principal, workspaceID, query string, kind domain.FileSearchKind, limit int) ([]string, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	response, err := r.remote.client.SearchFiles(ctx, connect.NewRequest(&executorv1.SearchFilesRequest{
		Identity: identity, Query: query, Kind: executorFileSearchKind(kind), Limit: executorSearchLimit(limit),
	}))
	if err != nil {
		return nil, remoteError(err)
	}
	return append([]string(nil), response.Msg.GetPaths()...), nil
}

func executorFileSearchKind(kind domain.FileSearchKind) executorv1.FileSearchKind {
	switch kind {
	case domain.FileSearchDirectories:
		return executorv1.FileSearchKind_FILE_SEARCH_KIND_DIRECTORY
	case domain.FileSearchAny:
		return executorv1.FileSearchKind_FILE_SEARCH_KIND_ANY
	default:
		return executorv1.FileSearchKind_FILE_SEARCH_KIND_FILE
	}
}

func (r *remoteWorkspaces) SearchText(ctx context.Context, principal domain.Principal, workspaceID, query string, limit int) ([]domain.SearchMatch, error) {
	identity, err := r.identity(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	response, err := r.remote.client.SearchText(ctx, connect.NewRequest(&executorv1.SearchTextRequest{
		Identity: identity, Query: query, Limit: executorSearchLimit(limit),
	}))
	if err != nil {
		return nil, remoteError(err)
	}
	result := make([]domain.SearchMatch, 0, len(response.Msg.GetMatches()))
	for _, match := range response.Msg.GetMatches() {
		if match == nil {
			return nil, fmt.Errorf("%w: executor returned an invalid search match", domain.ErrUnavailable)
		}
		result = append(result, domain.SearchMatch{Path: match.GetPath(), Line: match.GetLine(), Text: match.GetText()})
	}
	return result, nil
}

func executorSearchLimit(limit int) int32 {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	// #nosec G115 -- the two guards above constrain limit to [1, 1000].
	return int32(limit)
}

func (r *remoteWorkspaces) identity(ctx context.Context, principal domain.Principal, workspaceID string) (*executorv1.ProcessIdentity, error) {
	workspace, err := r.remote.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	return r.remote.executorIdentity(principal, workspace)
}

func remoteFileChangeKind(kind executorv1.FileChangeKind) domain.FileChangeKind {
	switch kind {
	case executorv1.FileChangeKind_FILE_CHANGE_KIND_ADD:
		return domain.FileChangeAdd
	case executorv1.FileChangeKind_FILE_CHANGE_KIND_CHANGE:
		return domain.FileChangeChange
	case executorv1.FileChangeKind_FILE_CHANGE_KIND_UNLINK:
		return domain.FileChangeUnlink
	default:
		return domain.FileChangeResync
	}
}
