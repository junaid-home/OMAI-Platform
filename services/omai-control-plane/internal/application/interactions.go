package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

const (
	maxInteractionItems    = 128
	maxInteractionMetadata = 64 << 10
	maxInteractionText     = 4096
)

type Interactions struct {
	sessions    port.SessionRepository
	permissions port.PermissionRepository
	questions   port.QuestionRepository
	events      port.EventRepository
}

func NewInteractions(sessions port.SessionRepository, permissions port.PermissionRepository, questions port.QuestionRepository, events port.EventRepository) *Interactions {
	return &Interactions{sessions: sessions, permissions: permissions, questions: questions, events: events}
}

func (i *Interactions) AskPermission(ctx context.Context, principal domain.Principal, request domain.PermissionRequest) (domain.PermissionRequest, bool, error) {
	session, err := i.sessions.Get(ctx, principal, request.SessionID)
	if err != nil {
		return domain.PermissionRequest{}, false, err
	}
	if request.ID == "" {
		request.ID, err = interactionID("per_")
		if err != nil {
			return domain.PermissionRequest{}, false, err
		}
	}
	request.ProjectID = session.ProjectID
	request.TenantID = principal.TenantID
	request.CreatedAt = request.CreatedAt.UTC()
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	if err := validatePermission(request); err != nil {
		return domain.PermissionRequest{}, false, err
	}
	created, isNew, err := i.permissions.Create(ctx, principal, request)
	if err != nil || !isNew {
		return created, isNew, err
	}
	metadata := map[string]any{}
	if len(created.MetadataJSON) != 0 {
		_ = json.Unmarshal(created.MetadataJSON, &metadata)
	}
	payload := map[string]any{
		"id": created.ID, "sessionID": created.SessionID, "permission": created.Permission,
		"patterns": created.Patterns, "metadata": metadata, "always": created.Always,
	}
	if created.Tool != nil {
		payload["tool"] = map[string]any{"messageID": created.Tool.MessageID, "callID": created.Tool.CallID}
	}
	return created, true, i.publish(ctx, principal, session, "permission.asked", payload)
}

func (i *Interactions) ListPermissions(ctx context.Context, principal domain.Principal, projectID, sessionID string, pageSize int, pageToken string) ([]domain.PermissionRequest, string, error) {
	projectID, sessionID, err := i.validateFilter(ctx, principal, projectID, sessionID)
	if err != nil {
		return nil, "", err
	}
	requests, err := i.permissions.ListPending(ctx, principal, projectID, sessionID)
	if err != nil {
		return nil, "", err
	}
	start, size, err := page(pageToken, pageSize, len(requests))
	if err != nil {
		return nil, "", err
	}
	end := min(start+size, len(requests))
	next := ""
	if end < len(requests) {
		next = encodePageToken(end)
	}
	return requests[start:end], next, nil
}

func (i *Interactions) RespondPermission(ctx context.Context, principal domain.Principal, sessionID, requestID string, decision domain.PermissionDecision) (domain.PermissionRequest, error) {
	if err := validID(sessionID, "session id"); err != nil {
		return domain.PermissionRequest{}, err
	}
	if err := validID(requestID, "permission id"); err != nil {
		return domain.PermissionRequest{}, err
	}
	if decision != domain.PermissionDecisionOnce && decision != domain.PermissionDecisionAlways && decision != domain.PermissionDecisionReject {
		return domain.PermissionRequest{}, fmt.Errorf("%w: invalid permission decision", domain.ErrInvalid)
	}
	session, err := i.sessions.Get(ctx, principal, sessionID)
	if err != nil {
		return domain.PermissionRequest{}, err
	}
	request, changed, err := i.permissions.Respond(ctx, principal, sessionID, requestID, decision)
	if err != nil || !changed {
		return request, err
	}
	err = i.publish(ctx, principal, session, "permission.replied", map[string]any{
		"sessionID": sessionID, "requestID": requestID, "reply": string(decision),
	})
	return request, err
}

func (i *Interactions) AskQuestion(ctx context.Context, principal domain.Principal, request domain.QuestionRequest) (domain.QuestionRequest, bool, error) {
	session, err := i.sessions.Get(ctx, principal, request.SessionID)
	if err != nil {
		return domain.QuestionRequest{}, false, err
	}
	if request.ID == "" {
		request.ID, err = interactionID("que_")
		if err != nil {
			return domain.QuestionRequest{}, false, err
		}
	}
	request.ProjectID = session.ProjectID
	request.TenantID = principal.TenantID
	request.CreatedAt = request.CreatedAt.UTC()
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	if err := validateQuestion(request); err != nil {
		return domain.QuestionRequest{}, false, err
	}
	created, isNew, err := i.questions.Create(ctx, principal, request)
	if err != nil || !isNew {
		return created, isNew, err
	}
	questions := make([]map[string]any, 0, len(created.Questions))
	for _, question := range created.Questions {
		options := make([]map[string]string, 0, len(question.Options))
		for _, option := range question.Options {
			options = append(options, map[string]string{"label": option.Label, "description": option.Description})
		}
		questions = append(questions, map[string]any{
			"question": question.Question, "header": question.Header, "options": options,
			"multiple": question.Multiple, "custom": question.Custom,
		})
	}
	payload := map[string]any{"id": created.ID, "sessionID": created.SessionID, "questions": questions}
	if created.Tool != nil {
		payload["tool"] = map[string]any{"messageID": created.Tool.MessageID, "callID": created.Tool.CallID}
	}
	return created, true, i.publish(ctx, principal, session, "question.asked", payload)
}

func (i *Interactions) ListQuestions(ctx context.Context, principal domain.Principal, projectID, sessionID string, pageSize int, pageToken string) ([]domain.QuestionRequest, string, error) {
	projectID, sessionID, err := i.validateFilter(ctx, principal, projectID, sessionID)
	if err != nil {
		return nil, "", err
	}
	requests, err := i.questions.ListPending(ctx, principal, projectID, sessionID)
	if err != nil {
		return nil, "", err
	}
	start, size, err := page(pageToken, pageSize, len(requests))
	if err != nil {
		return nil, "", err
	}
	end := min(start+size, len(requests))
	next := ""
	if end < len(requests) {
		next = encodePageToken(end)
	}
	return requests[start:end], next, nil
}

func (i *Interactions) ReplyQuestion(ctx context.Context, principal domain.Principal, sessionID, requestID string, answers [][]string, rejected bool) (domain.QuestionRequest, error) {
	if err := validID(sessionID, "session id"); err != nil {
		return domain.QuestionRequest{}, err
	}
	if err := validID(requestID, "question id"); err != nil {
		return domain.QuestionRequest{}, err
	}
	session, err := i.sessions.Get(ctx, principal, sessionID)
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	pending, err := i.questions.ListPending(ctx, principal, "", sessionID)
	if err != nil {
		return domain.QuestionRequest{}, err
	}
	if rejected {
		answers = nil
	} else {
		var request *domain.QuestionRequest
		for index := range pending {
			if pending[index].ID == requestID {
				request = &pending[index]
				break
			}
		}
		if request != nil {
			if err := validateAnswers(*request, answers); err != nil {
				return domain.QuestionRequest{}, err
			}
		} else if err := validateAnswerShape(answers); err != nil {
			return domain.QuestionRequest{}, err
		}
	}
	request, changed, err := i.questions.Reply(ctx, principal, sessionID, requestID, answers, rejected)
	if err != nil || !changed {
		return request, err
	}
	eventType := "question.replied"
	payload := map[string]any{"sessionID": sessionID, "requestID": requestID, "answers": answers}
	if rejected {
		eventType = "question.rejected"
		delete(payload, "answers")
	}
	return request, i.publish(ctx, principal, session, eventType, payload)
}

func (i *Interactions) DeleteSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	if err := i.permissions.DeleteSession(ctx, principal, sessionID); err != nil {
		return err
	}
	return i.questions.DeleteSession(ctx, principal, sessionID)
}

func (i *Interactions) validateFilter(ctx context.Context, principal domain.Principal, projectID, sessionID string) (string, string, error) {
	if projectID == "" && sessionID == "" {
		return "", "", fmt.Errorf("%w: project_id or session_id is required", domain.ErrInvalid)
	}
	if projectID != "" {
		if err := validID(projectID, "project id"); err != nil {
			return "", "", err
		}
	}
	if sessionID == "" {
		return projectID, "", nil
	}
	session, err := i.sessions.Get(ctx, principal, sessionID)
	if err != nil {
		return "", "", err
	}
	if projectID != "" && session.ProjectID != projectID {
		return "", "", fmt.Errorf("%w: session does not belong to project", domain.ErrConflict)
	}
	return session.ProjectID, session.ID, nil
}

func (i *Interactions) publish(ctx context.Context, principal domain.Principal, session domain.Session, eventType string, payload any) error {
	body, err := json.Marshal(map[string]any{"payload": payload})
	if err != nil {
		return fmt.Errorf("encode interaction event: %w", err)
	}
	_, err = i.events.Publish(ctx, principal, domain.Event{
		At: time.Now().UTC(), Type: eventType, WorkspaceID: session.WorkspaceID,
		SessionID: session.ID, RuntimeID: session.RuntimeID, ExternalSessionID: session.ExternalSessionID,
		PayloadJSON: body,
	})
	return err
}

func validatePermission(request domain.PermissionRequest) error {
	if err := validID(request.ID, "permission id"); err != nil {
		return err
	}
	if err := validLabel(strings.TrimSpace(request.Permission), 256, "permission"); err != nil {
		return err
	}
	if err := validateStrings(request.Patterns, maxInteractionItems, "permission patterns"); err != nil {
		return err
	}
	if err := validateStrings(request.Always, maxInteractionItems, "permission always patterns"); err != nil {
		return err
	}
	if len(request.MetadataJSON) > maxInteractionMetadata || (len(request.MetadataJSON) != 0 && !validJSONObject(request.MetadataJSON)) {
		return fmt.Errorf("%w: permission metadata must be a bounded JSON object", domain.ErrInvalid)
	}
	return validateToolReference(request.Tool)
}

func validateQuestion(request domain.QuestionRequest) error {
	if err := validID(request.ID, "question id"); err != nil {
		return err
	}
	if len(request.Questions) == 0 || len(request.Questions) > 32 {
		return fmt.Errorf("%w: a request requires between 1 and 32 questions", domain.ErrInvalid)
	}
	for _, question := range request.Questions {
		if err := validLabel(strings.TrimSpace(question.Question), maxInteractionText, "question"); err != nil {
			return err
		}
		if err := validLabel(strings.TrimSpace(question.Header), 64, "question header"); err != nil {
			return err
		}
		if len(question.Options) > 64 {
			return fmt.Errorf("%w: a question has too many options", domain.ErrInvalid)
		}
		for _, option := range question.Options {
			if err := validLabel(strings.TrimSpace(option.Label), 512, "question option"); err != nil {
				return err
			}
			if len(option.Description) > maxInteractionText || strings.ContainsRune(option.Description, '\x00') {
				return fmt.Errorf("%w: invalid question option description", domain.ErrInvalid)
			}
		}
	}
	return validateToolReference(request.Tool)
}

func validateAnswers(request domain.QuestionRequest, answers [][]string) error {
	if len(answers) != len(request.Questions) {
		return fmt.Errorf("%w: one answer set is required for every question", domain.ErrInvalid)
	}
	if err := validateAnswerShape(answers); err != nil {
		return err
	}
	for index, answer := range answers {
		question := request.Questions[index]
		if len(answer) == 0 || (!question.Multiple && len(answer) != 1) {
			return fmt.Errorf("%w: answer cardinality does not match question", domain.ErrInvalid)
		}
		allowed := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			allowed[option.Label] = struct{}{}
		}
		for _, value := range answer {
			if _, ok := allowed[value]; !ok && !question.Custom {
				return fmt.Errorf("%w: answer is not an allowed option", domain.ErrInvalid)
			}
		}
	}
	return nil
}

func validateAnswerShape(answers [][]string) error {
	if len(answers) == 0 || len(answers) > 32 {
		return fmt.Errorf("%w: invalid question answers", domain.ErrInvalid)
	}
	for _, answer := range answers {
		if err := validateStrings(answer, 64, "question answer"); err != nil {
			return err
		}
	}
	return nil
}

func validateStrings(values []string, maximum int, name string) error {
	if len(values) > maximum {
		return fmt.Errorf("%w: too many %s", domain.ErrInvalid, name)
	}
	for _, value := range values {
		if value == "" || len(value) > maxInteractionText || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: invalid %s", domain.ErrInvalid, name)
		}
	}
	return nil
}

func validateToolReference(tool *domain.ToolReference) error {
	if tool == nil {
		return nil
	}
	if err := validID(tool.MessageID, "tool message id"); err != nil {
		return err
	}
	return validID(tool.CallID, "tool call id")
}

func validJSONObject(value []byte) bool {
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

func interactionID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate interaction id: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
