package executorapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"connectrpc.com/connect"
	executorv1 "github.com/omai/backend/gen/go/omai/executor/v1"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

// Service is the private transport hosted inside a workspace sandbox. The
// control plane supplies the public workspace identity; this service maps it
// to the sandbox's single mounted root before handing work to the Go process
// manager.
type Service struct {
	Processes           port.ProcessManager
	Commands            port.WorkspaceCommandRunner
	Workspaces          port.WorkspaceRepository
	Root                string
	AllowedTenant       string
	ExpectedWorkspaceID string
	processWorkspaces   sync.Map
}

func (s *Service) ListShells(ctx context.Context, request *connect.Request[executorv1.ListShellsRequest]) (*connect.Response[executorv1.ListShellsResponse], error) {
	principal := s.principal(request.Msg.GetTenantId())
	if principal.TenantID == "" {
		return nil, executorError(domain.ErrForbidden)
	}
	shells, err := s.Processes.ListShells(ctx, principal)
	if err != nil {
		return nil, executorError(err)
	}
	response := &executorv1.ListShellsResponse{Shells: make([]*executorv1.ShellInfo, 0, len(shells))}
	for _, shell := range shells {
		response.Shells = append(response.Shells, &executorv1.ShellInfo{Path: shell.Path, Name: shell.Name, Acceptable: shell.Acceptable})
	}
	return connect.NewResponse(response), nil
}

func (s *Service) ListFiles(ctx context.Context, request *connect.Request[executorv1.ListFilesRequest]) (*connect.Response[executorv1.ListFilesResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	entries, err := s.Workspaces.ListFiles(ctx, principal, workspace.ID, request.Msg.GetPath())
	if err != nil {
		return nil, executorError(err)
	}
	response := &executorv1.ListFilesResponse{Entries: make([]*executorv1.FileEntry, 0, len(entries))}
	for _, entry := range entries {
		response.Entries = append(response.Entries, &executorv1.FileEntry{
			Name: entry.Name, Path: entry.Path, Directory: entry.Directory, Size: entry.Size, ModifiedUnixMillis: entry.ModifiedAt.UnixMilli(),
		})
	}
	return connect.NewResponse(response), nil
}

func (s *Service) WatchFiles(ctx context.Context, request *connect.Request[executorv1.WatchFilesRequest], stream *connect.ServerStream[executorv1.WorkspaceFileChange]) error {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return executorError(err)
	}
	updates, err := s.Workspaces.WatchFiles(ctx, principal, workspace.ID, request.Msg.GetPaths())
	if err != nil {
		return executorError(err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case change, ok := <-updates:
			if !ok {
				return nil
			}
			if err := stream.Send(&executorv1.WorkspaceFileChange{Sequence: change.Sequence, Path: change.Path, Kind: executorFileChangeKind(change.Kind)}); err != nil {
				return err
			}
		}
	}
}

func (s *Service) ReadFile(ctx context.Context, request *connect.Request[executorv1.ReadFileRequest]) (*connect.Response[executorv1.ReadFileResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	content, err := s.Workspaces.ReadFile(ctx, principal, workspace.ID, request.Msg.GetPath())
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.ReadFileResponse{
		Data: content.Data, Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Service) CreateDirectory(ctx context.Context, request *connect.Request[executorv1.CreateDirectoryRequest]) (*connect.Response[executorv1.CreateDirectoryResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	if err := s.Workspaces.CreateDirectory(ctx, principal, workspace.ID, request.Msg.GetPath()); err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.CreateDirectoryResponse{}), nil
}

func (s *Service) WriteFile(ctx context.Context, request *connect.Request[executorv1.WriteFileRequest]) (*connect.Response[executorv1.WriteFileResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	content, err := s.Workspaces.WriteFile(ctx, principal, workspace.ID, request.Msg.GetPath(), request.Msg.GetData(), domain.WriteFileOptions{
		ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	})
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.WriteFileResponse{
		Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Service) MovePath(ctx context.Context, request *connect.Request[executorv1.MovePathRequest]) (*connect.Response[executorv1.MovePathResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	content, err := s.Workspaces.MovePath(ctx, principal, workspace.ID, request.Msg.GetSourcePath(), request.Msg.GetDestinationPath(), domain.MovePathOptions{
		Overwrite: request.Msg.GetOverwrite(), ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	})
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.MovePathResponse{
		Revision: content.Revision, Size: content.Size, ModifiedUnixMillis: content.ModifiedAt.UnixMilli(),
	}), nil
}

func (s *Service) DeletePath(ctx context.Context, request *connect.Request[executorv1.DeletePathRequest]) (*connect.Response[executorv1.DeletePathResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	if err := s.Workspaces.DeletePath(ctx, principal, workspace.ID, request.Msg.GetPath(), domain.DeletePathOptions{
		Recursive: request.Msg.GetRecursive(), ExpectedRevision: request.Msg.GetExpectedRevision(), RequireRevisionMatch: request.Msg.GetRequireRevisionMatch(),
	}); err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.DeletePathResponse{}), nil
}

func (s *Service) ImportArchive(ctx context.Context, request *connect.Request[executorv1.ImportWorkspaceArchiveRequest]) (*connect.Response[executorv1.ImportWorkspaceArchiveResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	result, err := s.Workspaces.ImportArchive(ctx, principal, workspace.ID, request.Msg.GetData(), domain.ArchiveImportOptions{
		StripSingleRoot: request.Msg.GetStripSingleRoot(),
	})
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.ImportWorkspaceArchiveResponse{
		Files: result.Files, Directories: result.Dirs, UncompressedBytes: result.Bytes,
	}), nil
}

func (s *Service) ExportArchive(ctx context.Context, request *connect.Request[executorv1.ExportWorkspaceArchiveRequest], stream *connect.ServerStream[executorv1.WorkspaceArchiveChunk]) error {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return executorError(err)
	}
	archive, err := s.Workspaces.ExportArchive(ctx, principal, workspace.ID)
	if err != nil {
		return executorError(err)
	}
	defer archive.Close()
	buffer := make([]byte, 64*1024)
	for {
		read, readErr := archive.Read(buffer)
		if read > 0 {
			if err := stream.Send(&executorv1.WorkspaceArchiveChunk{Data: append([]byte(nil), buffer[:read]...)}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return executorError(readErr)
		}
	}
}

func (s *Service) SearchFiles(ctx context.Context, request *connect.Request[executorv1.SearchFilesRequest]) (*connect.Response[executorv1.SearchFilesResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	paths, err := s.Workspaces.SearchFiles(ctx, principal, workspace.ID, request.Msg.GetQuery(), privateFileSearchKind(request.Msg.GetKind()), int(request.Msg.GetLimit()))
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.SearchFilesResponse{Paths: paths}), nil
}

func privateFileSearchKind(kind executorv1.FileSearchKind) domain.FileSearchKind {
	switch kind {
	case executorv1.FileSearchKind_FILE_SEARCH_KIND_DIRECTORY:
		return domain.FileSearchDirectories
	case executorv1.FileSearchKind_FILE_SEARCH_KIND_ANY:
		return domain.FileSearchAny
	default:
		return domain.FileSearchFiles
	}
}

func (s *Service) SearchText(ctx context.Context, request *connect.Request[executorv1.SearchTextRequest]) (*connect.Response[executorv1.SearchTextResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	matches, err := s.Workspaces.SearchText(ctx, principal, workspace.ID, request.Msg.GetQuery(), int(request.Msg.GetLimit()))
	if err != nil {
		return nil, executorError(err)
	}
	response := &executorv1.SearchTextResponse{Matches: make([]*executorv1.SearchMatch, 0, len(matches))}
	for _, match := range matches {
		response.Matches = append(response.Matches, &executorv1.SearchMatch{Path: match.Path, Line: match.Line, Text: match.Text})
	}
	return connect.NewResponse(response), nil
}

func executorFileChangeKind(kind domain.FileChangeKind) executorv1.FileChangeKind {
	switch kind {
	case domain.FileChangeAdd:
		return executorv1.FileChangeKind_FILE_CHANGE_KIND_ADD
	case domain.FileChangeChange:
		return executorv1.FileChangeKind_FILE_CHANGE_KIND_CHANGE
	case domain.FileChangeUnlink:
		return executorv1.FileChangeKind_FILE_CHANGE_KIND_UNLINK
	default:
		return executorv1.FileChangeKind_FILE_CHANGE_KIND_RESYNC
	}
}

func (s *Service) AllocatePreviewPort(ctx context.Context, request *connect.Request[executorv1.AllocatePreviewPortRequest]) (*connect.Response[executorv1.AllocatePreviewPortResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	port, err := s.Processes.AllocatePreviewPort(ctx, principal, workspace.ID)
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.AllocatePreviewPortResponse{Port: port}), nil
}

func (s *Service) WaitPreviewPort(ctx context.Context, request *connect.Request[executorv1.WaitPreviewPortRequest]) (*connect.Response[executorv1.WaitPreviewPortResponse], error) {
	principal := s.principal(request.Msg.GetTenantId())
	if principal.TenantID == "" {
		return nil, executorError(domain.ErrForbidden)
	}
	portNumber, err := s.Processes.WaitPreviewPort(ctx, principal, request.Msg.GetProcessId(), append([]uint32(nil), request.Msg.GetPreferredPorts()...))
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.WaitPreviewPortResponse{Port: portNumber}), nil
}

func (s *Service) RunCommand(ctx context.Context, request *connect.Request[executorv1.RunCommandRequest]) (*connect.Response[executorv1.RunCommandResponse], error) {
	principal, workspace, _, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	maxOutput := request.Msg.GetMaxOutputBytes()
	if maxOutput <= 0 || uint64(maxOutput) > uint64(^uint(0)>>1) {
		return nil, executorError(domain.ErrInvalid)
	}
	result, err := s.Commands.Run(ctx, principal, domain.CommandSpec{
		WorkspaceID: workspace.ID, Command: request.Msg.GetCommand(), Args: append([]string(nil), request.Msg.GetArgs()...),
		CWD: request.Msg.GetCwd(), Env: cloneEnvironment(request.Msg.GetEnv()), MaxOutputBytes: int(maxOutput),
	})
	if err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.RunCommandResponse{Output: append([]byte(nil), result.Output...), ExitCode: result.ExitCode, WorkspaceRoot: workspace.Root}), nil
}

func (s *Service) StartProcess(ctx context.Context, request *connect.Request[executorv1.StartProcessRequest]) (*connect.Response[executorv1.StartProcessResponse], error) {
	principal, workspace, publicWorkspaceID, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	info, err := s.Processes.Start(ctx, principal, domain.ProcessSpec{
		WorkspaceID: workspace.ID,
		Kind:        request.Msg.GetKind(),
		ServerID:    request.Msg.GetServerId(),
		Title:       request.Msg.GetTitle(),
		Command:     request.Msg.GetCommand(),
		Args:        append([]string(nil), request.Msg.GetArgs()...),
		CWD:         request.Msg.GetCwd(),
		Env:         cloneEnvironment(request.Msg.GetEnv()),
	})
	if err != nil {
		return nil, executorError(err)
	}
	s.processWorkspaces.Store(info.ID, publicWorkspaceID)
	return connect.NewResponse(&executorv1.StartProcessResponse{Process: processProto(info, publicWorkspaceID, workspace.Root)}), nil
}

func (s *Service) GetProcess(ctx context.Context, request *connect.Request[executorv1.GetProcessRequest]) (*connect.Response[executorv1.GetProcessResponse], error) {
	principal := s.principal(request.Msg.GetTenantId())
	if principal.TenantID == "" {
		return nil, executorError(domain.ErrForbidden)
	}
	info, err := s.Processes.Get(ctx, principal, request.Msg.GetProcessId())
	if err != nil {
		return nil, executorError(err)
	}
	workspace, err := s.Workspaces.Get(ctx, principal, info.WorkspaceID)
	if err != nil {
		return nil, executorError(err)
	}
	publicWorkspaceID := s.ExpectedWorkspaceID
	if value, ok := s.processWorkspaces.Load(info.ID); ok {
		publicWorkspaceID, _ = value.(string)
	}
	if publicWorkspaceID == "" {
		publicWorkspaceID = info.WorkspaceID
	}
	return connect.NewResponse(&executorv1.GetProcessResponse{Process: processProto(info, publicWorkspaceID, workspace.Root)}), nil
}

func (s *Service) ListProcesses(ctx context.Context, request *connect.Request[executorv1.ListProcessesRequest]) (*connect.Response[executorv1.ListProcessesResponse], error) {
	principal, workspace, publicWorkspaceID, err := s.identity(ctx, request.Msg.GetIdentity())
	if err != nil {
		return nil, executorError(err)
	}
	values, err := s.Processes.List(ctx, principal, workspace.ID, request.Msg.GetKind())
	if err != nil {
		return nil, executorError(err)
	}
	response := &executorv1.ListProcessesResponse{Processes: make([]*executorv1.ProcessInfo, 0, len(values))}
	for _, value := range values {
		s.processWorkspaces.Store(value.ID, publicWorkspaceID)
		response.Processes = append(response.Processes, processProto(value, publicWorkspaceID, workspace.Root))
	}
	return connect.NewResponse(response), nil
}

func (s *Service) WriteProcess(ctx context.Context, request *connect.Request[executorv1.WriteProcessRequest]) (*connect.Response[executorv1.WriteProcessResponse], error) {
	if err := s.Processes.Write(ctx, s.principal(request.Msg.GetTenantId()), request.Msg.GetProcessId(), request.Msg.GetData()); err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.WriteProcessResponse{}), nil
}

func (s *Service) ResizeProcess(ctx context.Context, request *connect.Request[executorv1.ResizeProcessRequest]) (*connect.Response[executorv1.ResizeProcessResponse], error) {
	if err := s.Processes.Resize(ctx, s.principal(request.Msg.GetTenantId()), request.Msg.GetProcessId(), request.Msg.GetCols(), request.Msg.GetRows()); err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.ResizeProcessResponse{}), nil
}

func (s *Service) StopProcess(ctx context.Context, request *connect.Request[executorv1.StopProcessRequest]) (*connect.Response[executorv1.StopProcessResponse], error) {
	if err := s.Processes.Stop(ctx, s.principal(request.Msg.GetTenantId()), request.Msg.GetProcessId()); err != nil {
		return nil, executorError(err)
	}
	return connect.NewResponse(&executorv1.StopProcessResponse{}), nil
}

func (s *Service) RemoveProcess(ctx context.Context, request *connect.Request[executorv1.RemoveProcessRequest]) (*connect.Response[executorv1.RemoveProcessResponse], error) {
	if err := s.Processes.Remove(ctx, s.principal(request.Msg.GetTenantId()), request.Msg.GetProcessId()); err != nil {
		return nil, executorError(err)
	}
	s.processWorkspaces.Delete(request.Msg.GetProcessId())
	return connect.NewResponse(&executorv1.RemoveProcessResponse{}), nil
}

func (s *Service) WatchProcess(ctx context.Context, request *connect.Request[executorv1.WatchProcessRequest], stream *connect.ServerStream[executorv1.ProcessChunk]) error {
	replay, updates, stop, err := s.Processes.Watch(ctx, s.principal(request.Msg.GetTenantId()), request.Msg.GetProcessId(), request.Msg.GetCursor())
	if err != nil {
		return executorError(err)
	}
	defer stop()
	exited := false
	for _, value := range replay {
		exited = exited || value.Exited
		if err := stream.Send(chunkProto(value)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case value, ok := <-updates:
			if !ok {
				if exited {
					return nil
				}
				return connect.NewError(connect.CodeResourceExhausted, errors.New("executor subscriber fell behind; reconnect with last cursor"))
			}
			exited = exited || value.Exited
			if err := stream.Send(chunkProto(value)); err != nil {
				return err
			}
		}
	}
}

func (s *Service) identity(ctx context.Context, identity *executorv1.ProcessIdentity) (domain.Principal, domain.Workspace, string, error) {
	if identity == nil || strings.TrimSpace(identity.GetTenantId()) == "" || strings.TrimSpace(identity.GetWorkspaceId()) == "" {
		return domain.Principal{}, domain.Workspace{}, "", domain.ErrInvalid
	}
	principal := s.principal(identity.GetTenantId())
	if principal.TenantID == "" {
		return domain.Principal{}, domain.Workspace{}, "", domain.ErrForbidden
	}
	if s.ExpectedWorkspaceID != "" && identity.GetWorkspaceId() != s.ExpectedWorkspaceID {
		return domain.Principal{}, domain.Workspace{}, "", domain.ErrForbidden
	}
	root, err := executorWorkspaceRoot(s.Root, identity.GetRelativeRoot())
	if err != nil {
		return domain.Principal{}, domain.Workspace{}, "", err
	}
	workspace, err := s.Workspaces.Resolve(ctx, principal, root)
	if err != nil {
		return domain.Principal{}, domain.Workspace{}, "", err
	}
	return principal, workspace, identity.GetWorkspaceId(), nil
}

func executorWorkspaceRoot(root, relative string) (string, error) {
	relative = strings.TrimSpace(relative)
	if relative == "" {
		relative = "."
	}
	relative = filepath.FromSlash(relative)
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("%w: executor workspace root must be relative", domain.ErrInvalid)
	}
	cleaned := filepath.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: executor workspace root escapes its mount", domain.ErrForbidden)
	}
	candidate := filepath.Join(root, cleaned)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", domain.ErrNotFound
		}
		return "", err
	}
	contained, err := filepath.Rel(filepath.Clean(root), filepath.Clean(resolved))
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: executor workspace root escapes its mount", domain.ErrForbidden)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: executor workspace root is not a directory", domain.ErrInvalid)
	}
	return resolved, nil
}

func (s *Service) principal(tenantID string) domain.Principal {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || (s.AllowedTenant != "" && tenantID != s.AllowedTenant) {
		return domain.Principal{}
	}
	return domain.Principal{TenantID: tenantID, ActorID: "omai-control-plane", Service: true}
}

func processProto(value domain.ProcessInfo, workspaceID, root string) *executorv1.ProcessInfo {
	cwd := ""
	if relative, err := filepath.Rel(root, value.CWD); err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		cwd = filepath.ToSlash(relative)
	}
	return &executorv1.ProcessInfo{
		Id:                value.ID,
		WorkspaceId:       workspaceID,
		Kind:              value.Kind,
		ServerId:          value.ServerID,
		Title:             value.Title,
		Command:           value.Command,
		Cwd:               cwd,
		Status:            value.Status,
		Cursor:            value.Cursor,
		ExitCode:          value.ExitCode,
		StartedUnixMillis: value.StartedAt.UnixMilli(),
		EndedUnixMillis:   unixMillis(value.EndedAt),
	}
}

func chunkProto(value domain.ProcessChunk) *executorv1.ProcessChunk {
	return &executorv1.ProcessChunk{ProcessId: value.ProcessID, Cursor: value.Cursor, Data: append([]byte(nil), value.Data...), Exited: value.Exited, ExitCode: value.ExitCode}
}

func cloneEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func unixMillis(value interface {
	IsZero() bool
	UnixMilli() int64
}) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func executorError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrStaleRevision):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrConflict):
		code = connect.CodeAlreadyExists
	case errors.Is(err, domain.ErrForbidden):
		code = connect.CodePermissionDenied
	case errors.Is(err, domain.ErrUnavailable):
		code = connect.CodeUnavailable
	case errors.Is(err, domain.ErrReplayTooOld):
		code = connect.CodeOutOfRange
	case errors.Is(err, domain.ErrOutputTruncated):
		code = connect.CodeResourceExhausted
	}
	return connect.NewError(code, fmt.Errorf("workspace executor: %w", err))
}
