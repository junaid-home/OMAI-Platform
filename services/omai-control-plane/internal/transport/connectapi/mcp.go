package connectapi

import (
	"context"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

type MCPService struct{ Core *Services }

func (s *MCPService) List(ctx context.Context, request *connect.Request[uabv1.ListMCPRequest]) (*connect.Response[uabv1.ListMCPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	values, err := s.Core.MCP.List(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connectError(err)
	}
	response := &uabv1.ListMCPResponse{}
	for _, value := range values {
		response.Servers = append(response.Servers, mcpProto(value))
	}
	return connect.NewResponse(response), nil
}
func (s *MCPService) Upsert(ctx context.Context, request *connect.Request[uabv1.UpsertMCPRequest]) (*connect.Response[uabv1.UpsertMCPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	value := request.Msg.GetServer()
	if value == nil {
		return nil, connectError(domain.ErrInvalid)
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	stored, err := s.Core.MCP.Upsert(ctx, principal, domain.MCPServer{ID: value.GetId(), WorkspaceID: request.Msg.GetWorkspaceId(), Name: value.GetName(), Transport: value.GetTransport(), Command: value.GetCommand(), Args: value.GetArgs(), URL: value.GetUrl(), Enabled: value.GetEnabled()})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.UpsertMCPResponse{Server: mcpProto(stored)}), nil
}
func (s *MCPService) Delete(ctx context.Context, request *connect.Request[uabv1.DeleteMCPRequest]) (*connect.Response[uabv1.DeleteMCPResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	deleted, err := s.Core.MCP.Delete(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetServerId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.DeleteMCPResponse{Deleted: deleted}), nil
}
func mcpProto(value domain.MCPServer) *uabv1.MCPServerInfo {
	return &uabv1.MCPServerInfo{Id: value.ID, WorkspaceId: value.WorkspaceID, Name: value.Name, Transport: value.Transport, Command: value.Command, Args: value.Args, Url: value.URL, Enabled: value.Enabled}
}
