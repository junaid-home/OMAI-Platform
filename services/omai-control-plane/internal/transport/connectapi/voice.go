package connectapi

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/platform/auth"
	"google.golang.org/protobuf/types/known/structpb"
)

type VoiceService struct{ Core *Services }

func (s *VoiceService) MintTicket(ctx context.Context, request *connect.Request[uabv1.MintVoiceTicketRequest]) (*connect.Response[uabv1.MintVoiceTicketResponse], error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("principal missing"))
	}
	token, expires, err := s.Core.Voice.Mint(ctx, principal, request.Msg.GetWorkspaceId(), request.Msg.GetLocale(), request.Msg.GetVoice())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.MintVoiceTicketResponse{Ticket: token, ExpiresUnixMillis: expires.UnixMilli(), WebsocketPath: "/omai/voice/ws"}), nil
}
func (s *VoiceService) RedeemTicket(ctx context.Context, request *connect.Request[uabv1.RedeemVoiceTicketRequest]) (*connect.Response[uabv1.RedeemVoiceTicketResponse], error) {
	lease, err := s.Core.Voice.Redeem(ctx, request.Msg.GetTicket(), request.Msg.GetSessionId())
	if err != nil {
		return nil, connectError(err)
	}
	a := lease.Admission
	return connect.NewResponse(&uabv1.RedeemVoiceTicketResponse{LeaseToken: lease.Token, LeaseExpiresUnixMillis: lease.ExpiresAt.UnixMilli(), Claims: &uabv1.VoiceSessionClaims{TenantId: a.TenantID, ActorId: a.ActorID, WorkspaceId: a.WorkspaceID, Permissions: a.Permissions, Locale: a.Locale, Voice: a.Voice}}), nil
}
func (s *VoiceService) Heartbeat(ctx context.Context, request *connect.Request[uabv1.VoiceLeaseRequest]) (*connect.Response[uabv1.VoiceLeaseResponse], error) {
	lease, err := s.Core.Voice.Heartbeat(ctx, request.Msg.GetLeaseToken())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.VoiceLeaseResponse{Active: true, LeaseExpiresUnixMillis: lease.ExpiresAt.UnixMilli()}), nil
}
func (s *VoiceService) Release(ctx context.Context, request *connect.Request[uabv1.VoiceLeaseRequest]) (*connect.Response[uabv1.VoiceLeaseResponse], error) {
	if err := s.Core.Voice.Release(ctx, request.Msg.GetLeaseToken()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.VoiceLeaseResponse{Active: false}), nil
}
func (s *VoiceService) ListVoiceTools(ctx context.Context, request *connect.Request[uabv1.ListVoiceToolsRequest]) (*connect.Response[uabv1.ListVoiceToolsResponse], error) {
	tools, etag, err := s.Core.Voice.Tools(ctx, request.Msg.GetLeaseToken())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.ListVoiceToolsResponse{Tools: tools, RegistryEtag: etag}), nil
}
func (s *VoiceService) Dispatch(ctx context.Context, request *connect.Request[uabv1.VoiceDispatchRequest]) (*connect.Response[uabv1.VoiceDispatchResponse], error) {
	raw, confirmation, cached, err := s.Core.Voice.Dispatch(ctx, request.Msg.GetLeaseToken(), request.Msg.GetRequestId(), request.Msg.GetIdempotencyKey(), request.Msg.GetTool(), request.Msg.GetVersion(), request.Msg.GetArguments(), request.Msg.GetConfirmed())
	if err != nil {
		return nil, connectError(err)
	}
	if confirmation {
		return connect.NewResponse(&uabv1.VoiceDispatchResponse{Success: false, Code: "CONFIRMATION_REQUIRED", Message: "Explicit user confirmation is required", ConfirmationRequired: true}), nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, connectError(err)
	}
	result, err := structpb.NewStruct(object)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&uabv1.VoiceDispatchResponse{Success: true, Code: "EXECUTED", Result: result, Cached: cached}), nil
}
