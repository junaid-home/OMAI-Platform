package connectapi

import (
	"context"

	"connectrpc.com/connect"
	platformv1 "github.com/omai/backend/gen/go/omai/platform/v1"
	"github.com/omai/backend/internal/application"
	"github.com/omai/backend/internal/domain"
)

type PermissionService struct {
	Interactions *application.Interactions
}

func (s *PermissionService) ListPermissions(ctx context.Context, request *connect.Request[platformv1.ListPermissionsRequest]) (*connect.Response[platformv1.ListPermissionsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	permissions, next, err := s.Interactions.ListPermissions(
		ctx, principal, request.Msg.GetProjectId(), request.Msg.GetSessionId(), int(request.Msg.GetPageSize()), request.Msg.GetPageToken(),
	)
	if err != nil {
		return nil, connectError(err)
	}
	response := &platformv1.ListPermissionsResponse{NextPageToken: next}
	for _, permission := range permissions {
		response.Permissions = append(response.Permissions, permissionV1(permission))
	}
	return connect.NewResponse(response), nil
}

func (s *PermissionService) RespondPermission(ctx context.Context, request *connect.Request[platformv1.RespondPermissionRequest]) (*connect.Response[platformv1.RespondPermissionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	permission, err := s.Interactions.RespondPermission(
		ctx, principal, request.Msg.GetSessionId(), request.Msg.GetPermissionId(), permissionDecision(request.Msg.GetDecision()),
	)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.RespondPermissionResponse{Permission: permissionV1(permission)}), nil
}

type QuestionService struct {
	Interactions *application.Interactions
}

func (s *QuestionService) ListQuestions(ctx context.Context, request *connect.Request[platformv1.ListQuestionsRequest]) (*connect.Response[platformv1.ListQuestionsResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	questions, next, err := s.Interactions.ListQuestions(
		ctx, principal, request.Msg.GetProjectId(), request.Msg.GetSessionId(), int(request.Msg.GetPageSize()), request.Msg.GetPageToken(),
	)
	if err != nil {
		return nil, connectError(err)
	}
	response := &platformv1.ListQuestionsResponse{NextPageToken: next}
	for _, question := range questions {
		response.Questions = append(response.Questions, questionV1(question))
	}
	return connect.NewResponse(response), nil
}

func (s *QuestionService) ReplyQuestion(ctx context.Context, request *connect.Request[platformv1.ReplyQuestionRequest]) (*connect.Response[platformv1.ReplyQuestionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	answers := make([][]string, 0, len(request.Msg.GetAnswers()))
	for _, answer := range request.Msg.GetAnswers() {
		answers = append(answers, append([]string(nil), answer.GetValues()...))
	}
	question, err := s.Interactions.ReplyQuestion(ctx, principal, request.Msg.GetSessionId(), request.Msg.GetQuestionId(), answers, false)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.ReplyQuestionResponse{Question: questionV1(question)}), nil
}

func (s *QuestionService) RejectQuestion(ctx context.Context, request *connect.Request[platformv1.RejectQuestionRequest]) (*connect.Response[platformv1.RejectQuestionResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	question, err := s.Interactions.ReplyQuestion(ctx, principal, request.Msg.GetSessionId(), request.Msg.GetQuestionId(), nil, true)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(&platformv1.RejectQuestionResponse{Question: questionV1(question)}), nil
}

func permissionV1(value domain.PermissionRequest) *platformv1.Permission {
	result := &platformv1.Permission{
		Id: value.ID, SessionId: value.SessionID, ProjectId: value.ProjectID,
		Permission: value.Permission, Patterns: append([]string(nil), value.Patterns...),
		MetadataJson: append([]byte(nil), value.MetadataJSON...), Always: append([]string(nil), value.Always...),
		Decision: permissionDecisionV1(value.Decision), CreatedUnixMillis: value.CreatedAt.UnixMilli(),
	}
	if !value.ResolvedAt.IsZero() {
		result.ResolvedUnixMillis = value.ResolvedAt.UnixMilli()
	}
	if value.Tool != nil {
		result.Tool = &platformv1.InteractionToolReference{MessageId: value.Tool.MessageID, CallId: value.Tool.CallID}
	}
	return result
}

func questionV1(value domain.QuestionRequest) *platformv1.QuestionRequestResource {
	result := &platformv1.QuestionRequestResource{
		Id: value.ID, SessionId: value.SessionID, ProjectId: value.ProjectID,
		Rejected: value.Rejected, CreatedUnixMillis: value.CreatedAt.UnixMilli(),
	}
	if !value.ResolvedAt.IsZero() {
		result.ResolvedUnixMillis = value.ResolvedAt.UnixMilli()
	}
	if value.Tool != nil {
		result.Tool = &platformv1.InteractionToolReference{MessageId: value.Tool.MessageID, CallId: value.Tool.CallID}
	}
	for _, question := range value.Questions {
		item := &platformv1.Question{
			Question: question.Question, Header: question.Header, Multiple: question.Multiple, Custom: question.Custom,
		}
		for _, option := range question.Options {
			item.Options = append(item.Options, &platformv1.QuestionOption{Label: option.Label, Description: option.Description})
		}
		result.Questions = append(result.Questions, item)
	}
	for _, answer := range value.Answers {
		result.Answers = append(result.Answers, &platformv1.QuestionAnswer{Values: append([]string(nil), answer...)})
	}
	return result
}

func permissionDecision(value platformv1.PermissionDecision) domain.PermissionDecision {
	switch value {
	case platformv1.PermissionDecision_PERMISSION_DECISION_ONCE:
		return domain.PermissionDecisionOnce
	case platformv1.PermissionDecision_PERMISSION_DECISION_ALWAYS:
		return domain.PermissionDecisionAlways
	case platformv1.PermissionDecision_PERMISSION_DECISION_REJECT:
		return domain.PermissionDecisionReject
	default:
		return ""
	}
}

func permissionDecisionV1(value domain.PermissionDecision) platformv1.PermissionDecision {
	switch value {
	case domain.PermissionDecisionOnce:
		return platformv1.PermissionDecision_PERMISSION_DECISION_ONCE
	case domain.PermissionDecisionAlways:
		return platformv1.PermissionDecision_PERMISSION_DECISION_ALWAYS
	case domain.PermissionDecisionReject:
		return platformv1.PermissionDecision_PERMISSION_DECISION_REJECT
	default:
		return platformv1.PermissionDecision_PERMISSION_DECISION_UNSPECIFIED
	}
}
