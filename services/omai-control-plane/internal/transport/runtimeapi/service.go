package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
)

const (
	maxIdentityBytes = 512
	maxPromptBytes   = 4 << 20
)

type Service struct {
	Runtime             domain.Runtime
	AllowedTenant       string
	ExpectedWorkspaceID string
	Root                string
}

func (s *Service) RuntimeHealth(ctx context.Context, _ *connect.Request[uabv1.AgentRuntimeHealthRequest]) (*connect.Response[uabv1.AgentRuntimeHealthResponse], error) {
	health := s.Runtime.Health(ctx)
	return connect.NewResponse(&uabv1.AgentRuntimeHealthResponse{
		Available: health.Available, Authenticated: health.Authenticated,
		Version: health.Version, Reason: health.Reason,
	}), nil
}

func (s *Service) Run(ctx context.Context, request *connect.Request[uabv1.RuntimePrompt], stream *connect.ServerStream[uabv1.RuntimeEvent]) error {
	message := request.Msg
	for name, value := range map[string]string{
		"tenant_id": message.GetTenantId(), "actor_id": message.GetActorId(),
		"session_id": message.GetSessionId(), "external_session_id": message.GetExternalSessionId(),
		"workspace_id": message.GetWorkspaceId(), "provider_id": message.GetProviderId(), "model_id": message.GetModelId(),
	} {
		if !validIdentity(value) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is invalid", name))
		}
	}
	if s.AllowedTenant != "" && message.GetTenantId() != s.AllowedTenant {
		return connect.NewError(connect.CodePermissionDenied, errors.New("tenant is not assigned to this harness"))
	}
	if s.ExpectedWorkspaceID != "" && message.GetWorkspaceId() != s.ExpectedWorkspaceID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("workspace is not assigned to this harness"))
	}
	if len(message.GetText()) == 0 || len(message.GetText()) > maxPromptBytes {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("text must contain between 1 and %d bytes", maxPromptBytes))
	}
	if message.GetModelContextTokens() < 0 || message.GetModelContextTokens() > 16_777_216 || message.GetModelOutputTokens() < 0 || message.GetModelOutputTokens() > 1_048_576 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("model token limits are outside the supported range"))
	}
	prompt := domain.Prompt{
		RuntimeID: s.Runtime.Descriptor().ID, SessionID: message.GetSessionId(), ExternalSessionID: message.GetExternalSessionId(),
		WorkspaceID: message.GetWorkspaceId(), Root: s.Root, Text: message.GetText(), Title: message.GetTitle(),
		ProviderID: message.GetProviderId(), ModelID: message.GetModelId(),
		ModelContextTokens: message.GetModelContextTokens(), ModelOutputTokens: message.GetModelOutputTokens(),
		Principal: domain.Principal{TenantID: message.GetTenantId(), ActorID: message.GetActorId(), Service: true},
	}
	err := s.Runtime.Run(ctx, prompt, func(event domain.RuntimeEvent) error {
		return stream.Send(toProtoEvent(event))
	})
	if err != nil {
		return runtimeError(err)
	}
	return nil
}

func (s *Service) Cancel(ctx context.Context, request *connect.Request[uabv1.RuntimeCancelRequest]) (*connect.Response[uabv1.RuntimeCancelResponse], error) {
	if !validIdentity(request.Msg.GetSessionId()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is invalid"))
	}
	return connect.NewResponse(&uabv1.RuntimeCancelResponse{Cancelled: s.Runtime.Cancel(ctx, request.Msg.GetSessionId())}), nil
}

func toProtoEvent(event domain.RuntimeEvent) *uabv1.RuntimeEvent {
	at := event.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return &uabv1.RuntimeEvent{
		Kind: runtimeEventKind(event.Kind), MessageId: event.MessageID, Text: event.Text,
		ToolCallId: event.ToolCallID, ToolName: event.ToolName,
		ArgumentsJson: append([]byte(nil), event.ArgumentsJSON...), OutputJson: append([]byte(nil), event.OutputJSON...),
		Status: event.Status, UnixMillis: at.UnixMilli(),
	}
}

func runtimeEventKind(kind domain.RuntimeEventKind) uabv1.RuntimeEventKind {
	switch kind {
	case domain.RuntimeEventAgentMessage:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_MESSAGE_CHUNK
	case domain.RuntimeEventAgentThought:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_THOUGHT_CHUNK
	case domain.RuntimeEventToolCall:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL
	case domain.RuntimeEventToolUpdate:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL_UPDATE
	case domain.RuntimeEventStatus:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_STATUS
	case domain.RuntimeEventError:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_ERROR
	case domain.RuntimeEventDone:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_DONE
	default:
		return uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_UNSPECIFIED
	}
}

func runtimeError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, context.Canceled)
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, domain.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("harness execution failed"))
	}
}

func validIdentity(value string) bool {
	if value == "" || len(value) > maxIdentityBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
