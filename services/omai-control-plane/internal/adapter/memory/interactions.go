package memory

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type Permissions struct {
	mu   sync.RWMutex
	byID map[string]domain.PermissionRequest
}

func NewPermissions() *Permissions {
	return &Permissions{byID: make(map[string]domain.PermissionRequest)}
}

func (p *Permissions) Create(_ context.Context, principal domain.Principal, request domain.PermissionRequest) (domain.PermissionRequest, bool, error) {
	if principal.TenantID == "" || request.ID == "" || request.SessionID == "" || request.ProjectID == "" || request.TenantID != principal.TenantID {
		return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission identity is incomplete", domain.ErrInvalid)
	}
	key := interactionKey(principal.TenantID, request.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.byID[key]; ok {
		if !samePermissionRequest(existing, request) {
			return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission id already exists with different content", domain.ErrConflict)
		}
		return clonePermission(existing), false, nil
	}
	p.byID[key] = clonePermission(request)
	return clonePermission(request), true, nil
}

func (p *Permissions) ListPending(_ context.Context, principal domain.Principal, projectID, sessionID string) ([]domain.PermissionRequest, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]domain.PermissionRequest, 0)
	for _, request := range p.byID {
		if request.TenantID != principal.TenantID || request.Decision != "" {
			continue
		}
		if projectID != "" && request.ProjectID != projectID {
			continue
		}
		if sessionID != "" && request.SessionID != sessionID {
			continue
		}
		result = append(result, clonePermission(request))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (p *Permissions) Respond(_ context.Context, principal domain.Principal, sessionID, requestID string, decision domain.PermissionDecision) (domain.PermissionRequest, bool, error) {
	key := interactionKey(principal.TenantID, requestID)
	p.mu.Lock()
	defer p.mu.Unlock()
	request, ok := p.byID[key]
	if !ok || request.SessionID != sessionID {
		return domain.PermissionRequest{}, false, domain.ErrNotFound
	}
	if request.Decision != "" {
		if request.Decision != decision {
			return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission already has a different decision", domain.ErrConflict)
		}
		return clonePermission(request), false, nil
	}
	request.Decision = decision
	request.ResolvedAt = time.Now().UTC()
	p.byID[key] = clonePermission(request)
	return clonePermission(request), true, nil
}

func (p *Permissions) DeleteSession(_ context.Context, principal domain.Principal, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, request := range p.byID {
		if request.TenantID == principal.TenantID && request.SessionID == sessionID {
			delete(p.byID, key)
		}
	}
	return nil
}

type Questions struct {
	mu   sync.RWMutex
	byID map[string]domain.QuestionRequest
}

func NewQuestions() *Questions {
	return &Questions{byID: make(map[string]domain.QuestionRequest)}
}

func (q *Questions) Create(_ context.Context, principal domain.Principal, request domain.QuestionRequest) (domain.QuestionRequest, bool, error) {
	if principal.TenantID == "" || request.ID == "" || request.SessionID == "" || request.ProjectID == "" || request.TenantID != principal.TenantID {
		return domain.QuestionRequest{}, false, fmt.Errorf("%w: question identity is incomplete", domain.ErrInvalid)
	}
	key := interactionKey(principal.TenantID, request.ID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if existing, ok := q.byID[key]; ok {
		if !sameQuestionRequest(existing, request) {
			return domain.QuestionRequest{}, false, fmt.Errorf("%w: question id already exists with different content", domain.ErrConflict)
		}
		return cloneQuestionRequest(existing), false, nil
	}
	q.byID[key] = cloneQuestionRequest(request)
	return cloneQuestionRequest(request), true, nil
}

func (q *Questions) ListPending(_ context.Context, principal domain.Principal, projectID, sessionID string) ([]domain.QuestionRequest, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]domain.QuestionRequest, 0)
	for _, request := range q.byID {
		if request.TenantID != principal.TenantID || request.Rejected || !request.ResolvedAt.IsZero() {
			continue
		}
		if projectID != "" && request.ProjectID != projectID {
			continue
		}
		if sessionID != "" && request.SessionID != sessionID {
			continue
		}
		result = append(result, cloneQuestionRequest(request))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (q *Questions) Reply(_ context.Context, principal domain.Principal, sessionID, requestID string, answers [][]string, rejected bool) (domain.QuestionRequest, bool, error) {
	key := interactionKey(principal.TenantID, requestID)
	q.mu.Lock()
	defer q.mu.Unlock()
	request, ok := q.byID[key]
	if !ok || request.SessionID != sessionID {
		return domain.QuestionRequest{}, false, domain.ErrNotFound
	}
	if !request.ResolvedAt.IsZero() {
		if request.Rejected != rejected || !reflect.DeepEqual(request.Answers, answers) {
			return domain.QuestionRequest{}, false, fmt.Errorf("%w: question already has a different response", domain.ErrConflict)
		}
		return cloneQuestionRequest(request), false, nil
	}
	request.Answers = cloneAnswers(answers)
	request.Rejected = rejected
	request.ResolvedAt = time.Now().UTC()
	q.byID[key] = cloneQuestionRequest(request)
	return cloneQuestionRequest(request), true, nil
}

func (q *Questions) DeleteSession(_ context.Context, principal domain.Principal, sessionID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, request := range q.byID {
		if request.TenantID == principal.TenantID && request.SessionID == sessionID {
			delete(q.byID, key)
		}
	}
	return nil
}

func interactionKey(tenantID, requestID string) string { return tenantID + "\x00" + requestID }

func samePermissionRequest(left, right domain.PermissionRequest) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

func sameQuestionRequest(left, right domain.QuestionRequest) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(left, right)
}

func clonePermission(value domain.PermissionRequest) domain.PermissionRequest {
	value.Patterns = append([]string(nil), value.Patterns...)
	value.MetadataJSON = append([]byte(nil), value.MetadataJSON...)
	value.Always = append([]string(nil), value.Always...)
	if value.Tool != nil {
		tool := *value.Tool
		value.Tool = &tool
	}
	return value
}

func cloneQuestionRequest(value domain.QuestionRequest) domain.QuestionRequest {
	value.Questions = append([]domain.Question(nil), value.Questions...)
	for index := range value.Questions {
		value.Questions[index].Options = append([]domain.QuestionOption(nil), value.Questions[index].Options...)
	}
	value.Answers = cloneAnswers(value.Answers)
	if value.Tool != nil {
		tool := *value.Tool
		value.Tool = &tool
	}
	return value
}

func cloneAnswers(value [][]string) [][]string {
	result := make([][]string, len(value))
	for index := range value {
		result[index] = append([]string(nil), value[index]...)
	}
	return result
}
