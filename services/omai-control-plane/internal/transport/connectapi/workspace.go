package connectapi

import (
	"context"
	"io"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

func (s *Services) ResolveWorkspace(ctx context.Context, request *connect.Request[uabv1.ResolveWorkspaceRequest]) (*connect.Response[uabv1.ResolveWorkspaceResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	workspace, err := s.Workspaces.Resolve(ctx, principal, request.Msg.GetRoot())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.ResolveWorkspaceResponse{Workspace: workspaceProto(workspace)}), nil
}
func (s *Services) ListWorkspaces(ctx context.Context, _ *connect.Request[uabv1.ListWorkspacesRequest]) (*connect.Response[uabv1.ListWorkspacesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	workspaces, err := s.Workspaces.List(ctx, principal)
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListWorkspacesResponse{}
	for _, workspace := range workspaces {
		response.Workspaces = append(response.Workspaces, workspaceProto(workspace))
	}
	return connect.NewResponse(response), nil
}
func (s *Services) ListFiles(ctx context.Context, request *connect.Request[uabv1.ListFilesRequest]) (*connect.Response[uabv1.ListFilesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.Workspaces.ListFiles(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath())
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListFilesResponse{}
	for _, entry := range entries {
		response.Entries = append(response.Entries, &uabv1.FileEntry{Name: entry.Name, Path: entry.Path, Directory: entry.Directory, Size: entry.Size, ModifiedUnixMillis: entry.ModifiedAt.UnixMilli()})
	}
	return connect.NewResponse(response), nil
}
func (s *Services) WatchFiles(ctx context.Context, request *connect.Request[uabv1.WatchFilesRequest], stream *connect.ServerStream[uabv1.WorkspaceFileChange]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	updates, err := s.Workspaces.WatchFiles(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPaths())
	if err != nil {
		return connectError(err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case change, ok := <-updates:
			if !ok {
				return nil
			}
			if err := stream.Send(&uabv1.WorkspaceFileChange{Sequence: change.Sequence, Path: change.Path, Kind: publicFileChangeKind(change.Kind)}); err != nil {
				return err
			}
		}
	}
}
func (s *Services) ReadFile(ctx context.Context, request *connect.Request[uabv1.ReadFileRequest]) (*connect.Response[uabv1.ReadFileResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	content, err := s.Workspaces.ReadFile(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.ReadFileResponse{
		Data: content.Data, Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Services) CreateDirectory(ctx context.Context, request *connect.Request[uabv1.CreateDirectoryRequest]) (*connect.Response[uabv1.CreateDirectoryResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Workspaces.CreateDirectory(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.CreateDirectoryResponse{}), nil
}

func publicFileChangeKind(kind domain.FileChangeKind) uabv1.FileChangeKind {
	switch kind {
	case domain.FileChangeAdd:
		return uabv1.FileChangeKind_FILE_CHANGE_KIND_ADD
	case domain.FileChangeChange:
		return uabv1.FileChangeKind_FILE_CHANGE_KIND_CHANGE
	case domain.FileChangeUnlink:
		return uabv1.FileChangeKind_FILE_CHANGE_KIND_UNLINK
	default:
		return uabv1.FileChangeKind_FILE_CHANGE_KIND_RESYNC
	}
}
func (s *Services) WriteFile(ctx context.Context, request *connect.Request[uabv1.WriteFileRequest]) (*connect.Response[uabv1.WriteFileResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	content, err := s.Workspaces.WriteFile(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath(), request.Msg.GetData(), domain.WriteFileOptions{
		ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.WriteFileResponse{
		Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Services) MovePath(ctx context.Context, request *connect.Request[uabv1.MovePathRequest]) (*connect.Response[uabv1.MovePathResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	content, err := s.Workspaces.MovePath(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetSourcePath(), request.Msg.GetDestinationPath(), domain.MovePathOptions{
		Overwrite: request.Msg.GetOverwrite(), ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.MovePathResponse{
		Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Services) DeletePath(ctx context.Context, request *connect.Request[uabv1.DeletePathRequest]) (*connect.Response[uabv1.DeletePathResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Workspaces.DeletePath(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetPath(), domain.DeletePathOptions{
		Recursive: request.Msg.GetRecursive(), ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	}); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.DeletePathResponse{}), nil
}

func (s *Services) ImportArchive(ctx context.Context, request *connect.Request[uabv1.ImportWorkspaceArchiveRequest]) (*connect.Response[uabv1.ImportWorkspaceArchiveResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.Workspaces.ImportArchive(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetData(), domain.ArchiveImportOptions{
		StripSingleRoot: request.Msg.GetStripSingleRoot(),
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.ImportWorkspaceArchiveResponse{
		Files: result.Files, Directories: result.Dirs, UncompressedBytes: result.Bytes,
	}), nil
}

func (s *Services) ExportArchive(ctx context.Context, request *connect.Request[uabv1.ExportWorkspaceArchiveRequest], stream *connect.ServerStream[uabv1.WorkspaceArchiveChunk]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	archive, err := s.Workspaces.ExportArchive(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return connectError(err)
	}
	defer archive.Close()
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := archive.Read(buffer)
		if read > 0 {
			if err := stream.Send(&uabv1.WorkspaceArchiveChunk{Data: append([]byte(nil), buffer[:read]...)}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return connectError(readErr)
		}
	}
}
func (s *Services) SearchFiles(ctx context.Context, request *connect.Request[uabv1.SearchFilesRequest]) (*connect.Response[uabv1.SearchFilesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	paths, err := s.Workspaces.SearchFiles(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetQuery(), publicFileSearchKind(request.Msg.GetKind()), int(request.Msg.GetLimit()))
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.SearchFilesResponse{Paths: paths}), nil
}

func publicFileSearchKind(kind uabv1.FileSearchKind) domain.FileSearchKind {
	switch kind {
	case uabv1.FileSearchKind_FILE_SEARCH_KIND_DIRECTORY:
		return domain.FileSearchDirectories
	case uabv1.FileSearchKind_FILE_SEARCH_KIND_ANY:
		return domain.FileSearchAny
	default:
		return domain.FileSearchFiles
	}
}
func (s *Services) SearchText(ctx context.Context, request *connect.Request[uabv1.SearchTextRequest]) (*connect.Response[uabv1.SearchTextResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	matches, err := s.Workspaces.SearchText(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetQuery(), int(request.Msg.GetLimit()))
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.SearchTextResponse{}
	for _, match := range matches {
		response.Matches = append(response.Matches, &uabv1.SearchMatch{Path: match.Path, Line: match.Line, Text: match.Text})
	}
	return connect.NewResponse(response), nil
}

func (s *Services) ListHealth(ctx context.Context, _ *connect.Request[uabv1.ListRuntimeHealthRequest]) (*connect.Response[uabv1.ListRuntimeHealthResponse], error) {
	response := &uabv1.ListRuntimeHealthResponse{}
	for _, runtimeAdapter := range s.Runtimes.List() {
		response.Runtimes = append(response.Runtimes, runtimeHealthProto(runtimeAdapter.Health(ctx)))
	}
	return connect.NewResponse(response), nil
}
func (s *Services) ListMessages(ctx context.Context, request *connect.Request[uabv1.ListMessagesRequest]) (*connect.Response[uabv1.ListMessagesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Sessions.Get(ctx, principal, request.Msg.GetSessionId()); err != nil {
		return nil, connectError(err)
	}
	messages, err := s.Conversations.List(ctx, principal, request.Msg.GetSessionId())
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListMessagesResponse{}
	for _, message := range messages {
		response.Messages = append(response.Messages, &uabv1.ConversationMessageInfo{Id: message.ID, SessionId: message.SessionID, Role: message.Role, Kind: message.Kind, Text: message.Text, DataJson: message.DataJSON, CreatedUnixMillis: message.CreatedAt.UnixMilli()})
	}
	return connect.NewResponse(response), nil
}

func workspaceProto(value domain.Workspace) *uabv1.WorkspaceInfo {
	return &uabv1.WorkspaceInfo{Id: value.ID, NodeId: value.NodeID, Root: value.Root, RepoRoot: value.RepoRoot, CreatedUnixMillis: value.CreatedAt.UnixMilli(), UpdatedUnixMillis: value.UpdatedAt.UnixMilli()}
}
func runtimeHealthProto(value domain.RuntimeHealth) *uabv1.RuntimeHealthResponse {
	return &uabv1.RuntimeHealthResponse{RuntimeId: value.RuntimeID, Available: value.Available, Authenticated: value.Authenticated, Version: value.Version, LatencyMillis: value.Latency.Milliseconds(), Reason: value.Reason, CheckedUnixMillis: value.CheckedAt.UnixMilli()}
}
