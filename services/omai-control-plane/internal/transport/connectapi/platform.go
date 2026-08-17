package connectapi

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	platformv1 "github.com/omai/backend/gen/go/omai/platform/v1"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
)

type ProjectService struct {
	Platform *application.Platform
}

func (s *ProjectService) ResolveProject(ctx context.Context, request *connect.Request[platformv1.ResolveProjectRequest]) (*connect.Response[platformv1.ResolveProjectResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	project, _, err := s.Platform.ResolveProject(ctx, principal, request.Msg.GetRoot(), request.Msg.GetName())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.ResolveProjectResponse{Project: projectV1(project)}), nil
}

func (s *ProjectService) ListProjects(ctx context.Context, request *connect.Request[platformv1.ListProjectsRequest]) (*connect.Response[platformv1.ListProjectsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projects, next, err := s.Platform.ListProjects(ctx, principal, int(request.Msg.GetPageSize()), request.Msg.GetPageToken())
	if err != nil {
		return nil, connectError(err)
	}
	response := &platformv1.ListProjectsResponse{NextPageToken: next}
	for _, project := range projects {
		response.Projects = append(response.Projects, projectV1(project))
	}
	return connect.NewResponse(response), nil
}

func (s *ProjectService) GetProject(ctx context.Context, request *connect.Request[platformv1.GetProjectRequest]) (*connect.Response[platformv1.GetProjectResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.Platform.GetProject(ctx, principal, request.Msg.GetProjectId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.GetProjectResponse{Project: projectV1(project)}), nil
}

func (s *ProjectService) UpdateProject(ctx context.Context, request *connect.Request[platformv1.UpdateProjectRequest]) (*connect.Response[platformv1.UpdateProjectResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.Platform.UpdateProject(ctx, principal, request.Msg.GetProjectId(), domain.ProjectPatch{
		Name: request.Msg.Name, IconColor: request.Msg.IconColor,
		IconOverride: request.Msg.IconOverride, StartupCommand: request.Msg.StartupCommand,
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.UpdateProjectResponse{Project: projectV1(project)}), nil
}

type SessionService struct {
	Platform *application.Platform
}

func (s *SessionService) CreateSession(ctx context.Context, request *connect.Request[platformv1.CreateSessionRequest]) (*connect.Response[platformv1.CreateSessionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	session, created, err := s.Platform.CreateSession(ctx, principal, request.Msg.GetProjectId(), request.Msg.GetRuntimeId(), request.Msg.GetExternalSessionId(), request.Msg.GetTitle())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.CreateSessionResponse{Session: sessionV1(session), Created: created}), nil
}

func (s *SessionService) ListSessions(ctx context.Context, request *connect.Request[platformv1.ListSessionsRequest]) (*connect.Response[platformv1.ListSessionsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	sessions, next, err := s.Platform.ListSessions(ctx, principal, request.Msg.GetProjectId(), request.Msg.GetIncludeArchived(), int(request.Msg.GetPageSize()), request.Msg.GetPageToken())
	if err != nil {
		return nil, connectError(err)
	}
	response := &platformv1.ListSessionsResponse{NextPageToken: next}
	for _, session := range sessions {
		response.Sessions = append(response.Sessions, sessionV1(session))
	}
	return connect.NewResponse(response), nil
}

func (s *SessionService) GetSession(ctx context.Context, request *connect.Request[platformv1.GetSessionRequest]) (*connect.Response[platformv1.GetSessionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	session, err := s.Platform.GetSession(ctx, principal, request.Msg.GetSessionId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.GetSessionResponse{Session: sessionV1(session)}), nil
}

func (s *SessionService) UpdateSession(ctx context.Context, request *connect.Request[platformv1.UpdateSessionRequest]) (*connect.Response[platformv1.UpdateSessionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	session, err := s.Platform.UpdateSession(ctx, principal, request.Msg.GetSessionId(), domain.SessionPatch{
		Title: request.Msg.Title, Archived: request.Msg.Archived,
	})
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.UpdateSessionResponse{Session: sessionV1(session)}), nil
}

func (s *SessionService) DeleteSession(ctx context.Context, request *connect.Request[platformv1.DeleteSessionRequest]) (*connect.Response[platformv1.DeleteSessionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Platform.DeleteSession(ctx, principal, request.Msg.GetSessionId()); err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.DeleteSessionResponse{Deleted: true}), nil
}

func (s *SessionService) SubmitText(ctx context.Context, request *connect.Request[platformv1.SubmitTextRequest]) (*connect.Response[platformv1.SubmitTextResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	session, err := s.Platform.SubmitText(ctx, principal, request.Msg.GetSessionId(), request.Msg.GetProviderId(), request.Msg.GetModelId(), request.Msg.GetText())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.SubmitTextResponse{Session: sessionV1(session)}), nil
}

func (s *SessionService) CancelSession(ctx context.Context, request *connect.Request[platformv1.CancelSessionRequest]) (*connect.Response[platformv1.CancelSessionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	cancelled, err := s.Platform.CancelSession(ctx, principal, request.Msg.GetSessionId())
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.CancelSessionResponse{Cancelled: cancelled}), nil
}

func (s *SessionService) ListMessages(ctx context.Context, request *connect.Request[platformv1.ListMessagesRequest]) (*connect.Response[platformv1.ListMessagesResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	messages, err := s.Platform.ListMessages(ctx, principal, request.Msg.GetSessionId())
	if err != nil {
		return nil, connectError(err)
	}
	response := &platformv1.ListMessagesResponse{}
	for _, message := range messages {
		response.Messages = append(response.Messages, &platformv1.Message{
			Id: message.ID, SessionId: message.SessionID, Role: message.Role,
			Kind: message.Kind, Text: message.Text, DataJson: append([]byte(nil), message.DataJSON...),
			CreatedUnixMillis: message.CreatedAt.UnixMilli(),
		})
	}
	return connect.NewResponse(response), nil
}

func (s *SessionService) SubscribeSessionEvents(ctx context.Context, request *connect.Request[platformv1.SubscribeSessionEventsRequest], stream *connect.ServerStream[platformv1.SessionEvent]) error {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return err
	}
	replay, updates, stop, err := s.Platform.SubscribeSessionEvents(ctx, principal, request.Msg.GetSessionId(), request.Msg.GetAfterSequence())
	if err != nil {
		return connectError(err)
	}
	defer stop()
	for _, event := range replay {
		if err := stream.Send(sessionEventV1(event)); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-updates:
			if !ok {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("subscriber fell behind; reconnect with the last sequence"))
			}
			if err := stream.Send(sessionEventV1(event)); err != nil {
				return err
			}
		}
	}
}

func projectV1(project domain.Project) *platformv1.Project {
	return &platformv1.Project{
		Id: project.ID, WorkspaceId: project.WorkspaceID, Root: project.Root,
		RepoRoot: project.RepoRoot, Name: project.Name, IconColor: project.IconColor,
		IconOverride: project.IconOverride, StartupCommand: project.StartupCommand,
		CreatedUnixMillis: project.CreatedAt.UnixMilli(), UpdatedUnixMillis: project.UpdatedAt.UnixMilli(),
	}
}

func sessionV1(session domain.Session) *platformv1.Session {
	return &platformv1.Session{
		Id: session.ID, ExternalSessionId: session.ExternalSessionID, ProjectId: session.ProjectID,
		WorkspaceId: session.WorkspaceID, RuntimeId: session.RuntimeID,
		ProviderId: session.ProviderID, ModelId: session.ModelID, Title: session.Title,
		Archived: session.Archived, CreatedUnixMillis: session.CreatedAt.UnixMilli(),
		UpdatedUnixMillis: session.UpdatedAt.UnixMilli(),
	}
}

func sessionEventV1(event domain.Event) *platformv1.SessionEvent {
	result := &platformv1.SessionEvent{
		Sequence: event.Sequence, OccurredAtUnixMillis: event.At.UnixMilli(),
		SessionId: event.SessionID, WorkspaceId: event.WorkspaceID, RuntimeId: event.RuntimeID,
		ExternalSessionId: event.ExternalSessionID,
	}
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(event.PayloadJSON, &envelope) != nil {
		result.Payload = unknownEvent(event)
		return result
	}
	switch event.Type {
	case "session.status":
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(envelope.Payload, &payload) == nil {
			state := platformv1.SessionState_SESSION_STATE_UNSPECIFIED
			switch payload.Status {
			case "running":
				state = platformv1.SessionState_SESSION_STATE_RUNNING
			case "idle":
				state = platformv1.SessionState_SESSION_STATE_IDLE
			}
			if state != platformv1.SessionState_SESSION_STATE_UNSPECIFIED {
				result.Payload = &platformv1.SessionEvent_StateChanged{StateChanged: &platformv1.SessionStateChanged{State: state}}
				return result
			}
		}
	case "session.error":
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(envelope.Payload, &payload) == nil && payload.Message != "" {
			result.Payload = &platformv1.SessionEvent_Failed{Failed: &platformv1.SessionFailed{Message: payload.Message}}
			return result
		}
	case "permission.asked":
		if permissionRequestedV1(envelope.Payload, event, result) {
			return result
		}
	case "permission.replied":
		if permissionResolvedV1(envelope.Payload, result) {
			return result
		}
	case "question.asked":
		if questionRequestedV1(envelope.Payload, event, result) {
			return result
		}
	case "question.replied", "question.rejected":
		if questionResolvedV1(envelope.Payload, event.Type == "question.rejected", result) {
			return result
		}
	case "acp.session/update":
		if runtimeUpdateV1(envelope.Payload, result) {
			return result
		}
	}
	result.Payload = unknownEvent(event)
	return result
}

func permissionRequestedV1(raw json.RawMessage, event domain.Event, result *platformv1.SessionEvent) bool {
	var payload struct {
		ID         string          `json:"id"`
		SessionID  string          `json:"sessionID"`
		Permission string          `json:"permission"`
		Patterns   []string        `json:"patterns"`
		Metadata   json.RawMessage `json:"metadata"`
		Always     []string        `json:"always"`
		Tool       *struct {
			MessageID string `json:"messageID"`
			CallID    string `json:"callID"`
		} `json:"tool"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ID == "" || payload.SessionID == "" || payload.Permission == "" {
		return false
	}
	request := domain.PermissionRequest{
		ID: payload.ID, SessionID: payload.SessionID, Permission: payload.Permission,
		Patterns: payload.Patterns, MetadataJSON: append([]byte(nil), payload.Metadata...),
		Always: payload.Always, CreatedAt: event.At,
	}
	if payload.Tool != nil {
		request.Tool = &domain.ToolReference{MessageID: payload.Tool.MessageID, CallID: payload.Tool.CallID}
	}
	result.Payload = &platformv1.SessionEvent_PermissionRequested{PermissionRequested: &platformv1.PermissionRequested{Permission: permissionV1(request)}}
	return true
}

func permissionResolvedV1(raw json.RawMessage, result *platformv1.SessionEvent) bool {
	var payload struct {
		SessionID string `json:"sessionID"`
		RequestID string `json:"requestID"`
		Reply     string `json:"reply"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.SessionID == "" || payload.RequestID == "" {
		return false
	}
	decision := domain.PermissionDecision(payload.Reply)
	if decision != domain.PermissionDecisionOnce && decision != domain.PermissionDecisionAlways && decision != domain.PermissionDecisionReject {
		return false
	}
	result.Payload = &platformv1.SessionEvent_PermissionResolved{PermissionResolved: &platformv1.PermissionResolved{
		SessionId: payload.SessionID, PermissionId: payload.RequestID, Decision: permissionDecisionV1(decision),
	}}
	return true
}

func questionRequestedV1(raw json.RawMessage, event domain.Event, result *platformv1.SessionEvent) bool {
	var payload struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
			Multiple bool `json:"multiple"`
			Custom   bool `json:"custom"`
		} `json:"questions"`
		Tool *struct {
			MessageID string `json:"messageID"`
			CallID    string `json:"callID"`
		} `json:"tool"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ID == "" || payload.SessionID == "" || len(payload.Questions) == 0 {
		return false
	}
	request := domain.QuestionRequest{ID: payload.ID, SessionID: payload.SessionID, CreatedAt: event.At}
	for _, question := range payload.Questions {
		item := domain.Question{Question: question.Question, Header: question.Header, Multiple: question.Multiple, Custom: question.Custom}
		for _, option := range question.Options {
			item.Options = append(item.Options, domain.QuestionOption{Label: option.Label, Description: option.Description})
		}
		request.Questions = append(request.Questions, item)
	}
	if payload.Tool != nil {
		request.Tool = &domain.ToolReference{MessageID: payload.Tool.MessageID, CallID: payload.Tool.CallID}
	}
	result.Payload = &platformv1.SessionEvent_QuestionRequested{QuestionRequested: &platformv1.QuestionRequested{Question: questionV1(request)}}
	return true
}

func questionResolvedV1(raw json.RawMessage, rejected bool, result *platformv1.SessionEvent) bool {
	var payload struct {
		SessionID string     `json:"sessionID"`
		RequestID string     `json:"requestID"`
		Answers   [][]string `json:"answers"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.SessionID == "" || payload.RequestID == "" {
		return false
	}
	resolved := &platformv1.QuestionResolved{SessionId: payload.SessionID, QuestionId: payload.RequestID, Rejected: rejected}
	for _, answer := range payload.Answers {
		resolved.Answers = append(resolved.Answers, &platformv1.QuestionAnswer{Values: append([]string(nil), answer...)})
	}
	result.Payload = &platformv1.SessionEvent_QuestionResolved{QuestionResolved: resolved}
	return true
}

func runtimeUpdateV1(raw json.RawMessage, event *platformv1.SessionEvent) bool {
	var payload struct {
		Update struct {
			Kind       string          `json:"sessionUpdate"`
			MessageID  string          `json:"messageId"`
			ToolCallID string          `json:"toolCallId"`
			Title      string          `json:"title"`
			Status     string          `json:"status"`
			RawInput   json.RawMessage `json:"rawInput"`
			RawOutput  json.RawMessage `json:"rawOutput"`
			Content    struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	switch payload.Update.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		if payload.Update.MessageID == "" || payload.Update.Content.Text == "" {
			return false
		}
		channel := platformv1.MessageChannel_MESSAGE_CHANNEL_ANSWER
		if payload.Update.Kind == "agent_thought_chunk" {
			channel = platformv1.MessageChannel_MESSAGE_CHANNEL_REASONING
		}
		event.Payload = &platformv1.SessionEvent_MessageDelta{MessageDelta: &platformv1.MessageDelta{
			MessageId: payload.Update.MessageID, Channel: channel, Delta: payload.Update.Content.Text,
		}}
		return true
	case "tool_call":
		if payload.Update.ToolCallID == "" {
			return false
		}
		event.Payload = &platformv1.SessionEvent_ToolCallStarted{ToolCallStarted: &platformv1.ToolCallStarted{
			ToolCallId: payload.Update.ToolCallID, MessageId: payload.Update.MessageID,
			ToolName: payload.Update.Title, Status: payload.Update.Status,
			ArgumentsJson: append([]byte(nil), payload.Update.RawInput...),
		}}
		return true
	case "tool_call_update":
		if payload.Update.ToolCallID == "" {
			return false
		}
		event.Payload = &platformv1.SessionEvent_ToolCallUpdated{ToolCallUpdated: &platformv1.ToolCallUpdated{
			ToolCallId: payload.Update.ToolCallID, Status: payload.Update.Status,
			OutputJson: append([]byte(nil), payload.Update.RawOutput...),
		}}
		return true
	default:
		return false
	}
}

func unknownEvent(event domain.Event) *platformv1.SessionEvent_Unknown {
	return &platformv1.SessionEvent_Unknown{Unknown: &platformv1.UnknownEvent{
		WireType: event.Type, PayloadJson: append([]byte(nil), event.PayloadJSON...),
	}}
}
