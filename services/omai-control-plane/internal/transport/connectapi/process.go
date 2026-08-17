package connectapi

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

type TerminalService struct{ Core *Services }

func (s *TerminalService) ListShells(ctx context.Context, _ *connect.Request[uabv1.ListShellsRequest]) (*connect.Response[uabv1.ListShellsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	shells, err := s.Core.Processes.ListShells(ctx, principal)
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListShellsResponse{Shells: make([]*uabv1.ShellInfo, 0, len(shells))}
	for _, shell := range shells {
		response.Shells = append(response.Shells, &uabv1.ShellInfo{Path: shell.Path, Name: shell.Name, Acceptable: shell.Acceptable})
	}
	return connect.NewResponse(response), nil
}

func (s *TerminalService) Create(ctx context.Context, request *connect.Request[uabv1.CreateTerminalRequest]) (*connect.Response[uabv1.CreateTerminalResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	command := request.Msg.GetCommand()
	args := append([]string(nil), request.Msg.GetArgs()...)
	if command == "" {
		if len(args) != 0 {
			return nil, connectError(domain.ErrInvalid)
		}
		command = "/bin/sh"
	}
	environment := make(map[string]string, len(request.Msg.GetEnv()))
	for _, value := range request.Msg.GetEnv() {
		if value == nil || value.GetKey() == "" {
			return nil, connectError(domain.ErrInvalid)
		}
		if _, exists := environment[value.GetKey()]; exists {
			return nil, connectError(domain.ErrInvalid)
		}
		environment[value.GetKey()] = value.GetValue()
	}
	info, err := s.Core.Processes.Start(ctx, principal, domain.ProcessSpec{WorkspaceID: request.Msg.GetWorkspaceId(), Kind: "terminal", Command: command, Args: args, CWD: request.Msg.GetCwd(), Env: environment})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.CreateTerminalResponse{Terminal: terminalProto(info)}), nil
}

func (s *TerminalService) List(ctx context.Context, request *connect.Request[uabv1.ListTerminalsRequest]) (*connect.Response[uabv1.ListTerminalsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	values, err := s.Core.Processes.List(ctx, principal, request.Msg.GetWorkspaceId(), "terminal")
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListTerminalsResponse{}
	for _, value := range values {
		response.Terminals = append(response.Terminals, terminalProto(value))
	}
	return connect.NewResponse(response), nil
}

func (s *TerminalService) Write(ctx context.Context, request *connect.Request[uabv1.WriteTerminalRequest]) (*connect.Response[uabv1.WriteTerminalResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Core.Processes.Write(ctx, principal, request.Msg.GetTerminalId(), request.Msg.GetData()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.WriteTerminalResponse{}), nil
}

func (s *TerminalService) Resize(ctx context.Context, request *connect.Request[uabv1.ResizeTerminalRequest]) (*connect.Response[uabv1.ResizeTerminalResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Core.Processes.Resize(ctx, principal, request.Msg.GetTerminalId(), request.Msg.GetCols(), request.Msg.GetRows()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.ResizeTerminalResponse{}), nil
}

func (s *TerminalService) Remove(ctx context.Context, request *connect.Request[uabv1.RemoveTerminalRequest]) (*connect.Response[uabv1.RemoveTerminalResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Core.Processes.Remove(ctx, principal, request.Msg.GetTerminalId()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.RemoveTerminalResponse{}), nil
}

func (s *TerminalService) Watch(ctx context.Context, request *connect.Request[uabv1.WatchTerminalRequest], stream *connect.ServerStream[uabv1.TerminalChunk]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	replay, updates, stop, err := s.Core.Processes.Watch(ctx, principal, request.Msg.GetTerminalId(), request.Msg.GetCursor())
	if err != nil {
		return connectError(err)
	}
	defer stop()
	exited := false
	for _, value := range replay {
		exited = exited || value.Exited
		if err := stream.Send(terminalChunkProto(value)); err != nil {
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
				return connect.NewError(connect.CodeResourceExhausted, errors.New("terminal subscriber fell behind; reconnect with last cursor"))
			}
			exited = exited || value.Exited
			if err := stream.Send(terminalChunkProto(value)); err != nil {
				return err
			}
		}
	}
}

type LSPService struct{ Core *Services }

func (s *LSPService) List(ctx context.Context, request *connect.Request[uabv1.ListLSPRequest]) (*connect.Response[uabv1.ListLSPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListLSPResponse{}
	for _, value := range s.Core.LSP.List(ctx) {
		response.Servers = append(response.Servers, &uabv1.LSPServerInfo{Id: value.ID, Name: value.Name, Command: value.Command, Available: value.Available, Version: value.Version})
	}
	return connect.NewResponse(response), nil
}

func (s *LSPService) Start(ctx context.Context, request *connect.Request[uabv1.StartLSPRequest]) (*connect.Response[uabv1.StartLSPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	server, ok := s.Core.LSP.Get(request.Msg.GetServerId())
	if !ok {
		return nil, connectError(domain.ErrNotFound)
	}
	if !server.Available {
		return nil, connectError(domain.ErrUnavailable)
	}
	info, err := s.Core.Processes.Start(ctx, principal, domain.ProcessSpec{WorkspaceID: request.Msg.GetWorkspaceId(), Kind: "lsp", ServerID: server.ID, Title: server.Name, Command: server.Command, Args: server.Args})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.StartLSPResponse{Instance: lspInstanceProto(info, server.ID)}), nil
}

func (s *LSPService) ListInstances(ctx context.Context, request *connect.Request[uabv1.ListLSPInstancesRequest]) (*connect.Response[uabv1.ListLSPInstancesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	values, err := s.Core.Processes.List(ctx, principal, request.Msg.GetWorkspaceId(), "lsp")
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListLSPInstancesResponse{}
	for _, value := range values {
		response.Instances = append(response.Instances, lspInstanceProto(value, ""))
	}
	return connect.NewResponse(response), nil
}

func (s *LSPService) Write(ctx context.Context, request *connect.Request[uabv1.WriteLSPRequest]) (*connect.Response[uabv1.WriteLSPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Core.Processes.Write(ctx, principal, request.Msg.GetInstanceId(), request.Msg.GetData()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.WriteLSPResponse{}), nil
}

func (s *LSPService) Stop(ctx context.Context, request *connect.Request[uabv1.StopLSPRequest]) (*connect.Response[uabv1.StopLSPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Core.Processes.Stop(ctx, principal, request.Msg.GetInstanceId()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.StopLSPResponse{}), nil
}

func (s *LSPService) Watch(ctx context.Context, request *connect.Request[uabv1.WatchLSPRequest], stream *connect.ServerStream[uabv1.LSPChunk]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	replay, updates, stop, err := s.Core.Processes.Watch(ctx, principal, request.Msg.GetInstanceId(), request.Msg.GetCursor())
	if err != nil {
		return connectError(err)
	}
	defer stop()
	exited := false
	for _, value := range replay {
		exited = exited || value.Exited
		if err := stream.Send(lspChunkProto(value)); err != nil {
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
				return connect.NewError(connect.CodeResourceExhausted, errors.New("language-server subscriber fell behind; reconnect with last cursor"))
			}
			exited = exited || value.Exited
			if err := stream.Send(lspChunkProto(value)); err != nil {
				return err
			}
		}
	}
}

func terminalProto(value domain.ProcessInfo) *uabv1.TerminalInfo {
	return &uabv1.TerminalInfo{Id: value.ID, WorkspaceId: value.WorkspaceID, Title: value.Title, Cwd: value.CWD, Status: value.Status, Cursor: value.Cursor, ExitCode: value.ExitCode, StartedUnixMillis: value.StartedAt.UnixMilli(), EndedUnixMillis: unixMillis(value.EndedAt)}
}
func terminalChunkProto(value domain.ProcessChunk) *uabv1.TerminalChunk {
	return &uabv1.TerminalChunk{TerminalId: value.ProcessID, Cursor: value.Cursor, Data: value.Data, Exited: value.Exited, ExitCode: value.ExitCode}
}
func lspInstanceProto(value domain.ProcessInfo, serverID string) *uabv1.LSPInstanceInfo {
	if serverID == "" {
		serverID = value.ServerID
	}
	return &uabv1.LSPInstanceInfo{Id: value.ID, WorkspaceId: value.WorkspaceID, ServerId: serverID, Command: value.Command, Status: value.Status, Cursor: value.Cursor, ExitCode: value.ExitCode, StartedUnixMillis: value.StartedAt.UnixMilli(), EndedUnixMillis: unixMillis(value.EndedAt)}
}
func lspChunkProto(value domain.ProcessChunk) *uabv1.LSPChunk {
	return &uabv1.LSPChunk{InstanceId: value.ProcessID, Cursor: value.Cursor, Data: value.Data, Exited: value.Exited, ExitCode: value.ExitCode}
}
func unixMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}
