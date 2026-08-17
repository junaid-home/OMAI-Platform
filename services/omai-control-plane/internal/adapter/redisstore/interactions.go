package redisstore

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

var createInteractionScript = redis.NewScript(`
local existing = redis.call('HGET', KEYS[1], ARGV[1])
if existing then
  if existing == ARGV[2] then return 0 end
  return redis.error_reply('OMAI_CONFLICT')
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('SADD', KEYS[3], ARGV[1])
return 1
`)

var resolveInteractionScript = redis.NewScript(`
local current = redis.call('HGET', KEYS[1], ARGV[1])
if not current then return redis.error_reply('OMAI_NOT_FOUND') end
if current ~= ARGV[2] then return -1 end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[3])
redis.call('SREM', KEYS[2], ARGV[1])
return 1
`)

type Permissions struct {
	client redis.UniversalClient
	prefix string
}

func NewPermissions(client redis.UniversalClient, prefix string) *Permissions {
	return &Permissions{client: client, prefix: platformPrefix(prefix)}
}

func (p *Permissions) Create(ctx context.Context, principal domain.Principal, request domain.PermissionRequest) (domain.PermissionRequest, bool, error) {
	if principal.TenantID == "" || request.ID == "" || request.SessionID == "" || request.ProjectID == "" || request.TenantID != principal.TenantID {
		return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission identity is incomplete", domain.ErrInvalid)
	}
	raw, err := encodeValue(request)
	if err != nil {
		return domain.PermissionRequest{}, false, err
	}
	created, err := createInteractionScript.Run(ctx, p.client, []string{
		p.records(principal.TenantID), p.projectIndex(principal.TenantID, request.ProjectID), p.sessionIndex(principal.TenantID, request.SessionID),
	}, request.ID, raw).Int()
	if err != nil {
		return domain.PermissionRequest{}, false, redisStoreError("create permission", err)
	}
	return cloneRedisPermission(request), created == 1, nil
}

func (p *Permissions) ListPending(ctx context.Context, principal domain.Principal, projectID, sessionID string) ([]domain.PermissionRequest, error) {
	index := p.projectIndex(principal.TenantID, projectID)
	if sessionID != "" {
		index = p.sessionIndex(principal.TenantID, sessionID)
	}
	ids, err := p.client.SMembers(ctx, index).Result()
	if err != nil || len(ids) == 0 {
		return nil, redisStoreError("list permission ids", err)
	}
	values, err := p.client.HMGet(ctx, p.records(principal.TenantID), ids...).Result()
	if err != nil {
		return nil, redisStoreError("list permissions", err)
	}
	result := make([]domain.PermissionRequest, 0, len(values))
	for _, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		request, err := decodeValue[domain.PermissionRequest](raw)
		if err != nil {
			return nil, err
		}
		if request.TenantID != principal.TenantID || request.Decision != "" || (projectID != "" && request.ProjectID != projectID) || (sessionID != "" && request.SessionID != sessionID) {
			continue
		}
		result = append(result, cloneRedisPermission(request))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (p *Permissions) Respond(ctx context.Context, principal domain.Principal, sessionID, requestID string, decision domain.PermissionDecision) (domain.PermissionRequest, bool, error) {
	for range 4 {
		before, raw, err := loadRedisInteraction[domain.PermissionRequest](ctx, p.client, p.records(principal.TenantID), requestID)
		if err != nil || before.SessionID != sessionID {
			if err == nil {
				err = domain.ErrNotFound
			}
			return domain.PermissionRequest{}, false, err
		}
		if before.Decision != "" {
			if before.Decision != decision {
				return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission already has a different decision", domain.ErrConflict)
			}
			return cloneRedisPermission(before), false, nil
		}
		after := before
		after.Decision = decision
		after.ResolvedAt = nowUTC()
		encoded, err := encodeValue(after)
		if err != nil {
			return domain.PermissionRequest{}, false, err
		}
		changed, err := resolveInteractionScript.Run(ctx, p.client, []string{
			p.records(principal.TenantID), p.projectIndex(principal.TenantID, before.ProjectID),
		}, requestID, raw, encoded).Int()
		if err != nil {
			return domain.PermissionRequest{}, false, redisStoreError("resolve permission", err)
		}
		if changed == 1 {
			return cloneRedisPermission(after), true, nil
		}
	}
	return domain.PermissionRequest{}, false, fmt.Errorf("%w: permission changed concurrently", domain.ErrConflict)
}

func (p *Permissions) DeleteSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	return deleteRedisInteractions(ctx, p.client, p.records(principal.TenantID), p.sessionIndex(principal.TenantID, sessionID), func(projectID string) string {
		return p.projectIndex(principal.TenantID, projectID)
	}, func(raw string) (string, error) {
		value, err := decodeValue[domain.PermissionRequest](raw)
		return value.ProjectID, err
	})
}

func (p *Permissions) records(tenantID string) string {
	return redisKey(p.prefix, "permissions", tenantID)
}
func (p *Permissions) projectIndex(tenantID, projectID string) string {
	return redisKey(p.prefix, "permissions-project", tenantID, projectID)
}
func (p *Permissions) sessionIndex(tenantID, sessionID string) string {
	return redisKey(p.prefix, "permissions-session", tenantID, sessionID)
}

type Questions struct {
	client redis.UniversalClient
	prefix string
}

func NewQuestions(client redis.UniversalClient, prefix string) *Questions {
	return &Questions{client: client, prefix: platformPrefix(prefix)}
}

func (q *Questions) Create(ctx context.Context, principal domain.Principal, request domain.QuestionRequest) (domain.QuestionRequest, bool, error) {
	if principal.TenantID == "" || request.ID == "" || request.SessionID == "" || request.ProjectID == "" || request.TenantID != principal.TenantID {
		return domain.QuestionRequest{}, false, fmt.Errorf("%w: question identity is incomplete", domain.ErrInvalid)
	}
	raw, err := encodeValue(request)
	if err != nil {
		return domain.QuestionRequest{}, false, err
	}
	created, err := createInteractionScript.Run(ctx, q.client, []string{
		q.records(principal.TenantID), q.projectIndex(principal.TenantID, request.ProjectID), q.sessionIndex(principal.TenantID, request.SessionID),
	}, request.ID, raw).Int()
	if err != nil {
		return domain.QuestionRequest{}, false, redisStoreError("create question", err)
	}
	return cloneRedisQuestion(request), created == 1, nil
}

func (q *Questions) ListPending(ctx context.Context, principal domain.Principal, projectID, sessionID string) ([]domain.QuestionRequest, error) {
	index := q.projectIndex(principal.TenantID, projectID)
	if sessionID != "" {
		index = q.sessionIndex(principal.TenantID, sessionID)
	}
	ids, err := q.client.SMembers(ctx, index).Result()
	if err != nil || len(ids) == 0 {
		return nil, redisStoreError("list question ids", err)
	}
	values, err := q.client.HMGet(ctx, q.records(principal.TenantID), ids...).Result()
	if err != nil {
		return nil, redisStoreError("list questions", err)
	}
	result := make([]domain.QuestionRequest, 0, len(values))
	for _, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		request, err := decodeValue[domain.QuestionRequest](raw)
		if err != nil {
			return nil, err
		}
		if request.TenantID != principal.TenantID || !request.ResolvedAt.IsZero() || (projectID != "" && request.ProjectID != projectID) || (sessionID != "" && request.SessionID != sessionID) {
			continue
		}
		result = append(result, cloneRedisQuestion(request))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (q *Questions) Reply(ctx context.Context, principal domain.Principal, sessionID, requestID string, answers [][]string, rejected bool) (domain.QuestionRequest, bool, error) {
	for range 4 {
		before, raw, err := loadRedisInteraction[domain.QuestionRequest](ctx, q.client, q.records(principal.TenantID), requestID)
		if err != nil || before.SessionID != sessionID {
			if err == nil {
				err = domain.ErrNotFound
			}
			return domain.QuestionRequest{}, false, err
		}
		if !before.ResolvedAt.IsZero() {
			if before.Rejected != rejected || !reflect.DeepEqual(before.Answers, answers) {
				return domain.QuestionRequest{}, false, fmt.Errorf("%w: question already has a different response", domain.ErrConflict)
			}
			return cloneRedisQuestion(before), false, nil
		}
		after := before
		after.Answers = cloneRedisAnswers(answers)
		after.Rejected = rejected
		after.ResolvedAt = nowUTC()
		encoded, err := encodeValue(after)
		if err != nil {
			return domain.QuestionRequest{}, false, err
		}
		changed, err := resolveInteractionScript.Run(ctx, q.client, []string{
			q.records(principal.TenantID), q.projectIndex(principal.TenantID, before.ProjectID),
		}, requestID, raw, encoded).Int()
		if err != nil {
			return domain.QuestionRequest{}, false, redisStoreError("resolve question", err)
		}
		if changed == 1 {
			return cloneRedisQuestion(after), true, nil
		}
	}
	return domain.QuestionRequest{}, false, fmt.Errorf("%w: question changed concurrently", domain.ErrConflict)
}

func (q *Questions) DeleteSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	return deleteRedisInteractions(ctx, q.client, q.records(principal.TenantID), q.sessionIndex(principal.TenantID, sessionID), func(projectID string) string {
		return q.projectIndex(principal.TenantID, projectID)
	}, func(raw string) (string, error) {
		value, err := decodeValue[domain.QuestionRequest](raw)
		return value.ProjectID, err
	})
}

func (q *Questions) records(tenantID string) string { return redisKey(q.prefix, "questions", tenantID) }
func (q *Questions) projectIndex(tenantID, projectID string) string {
	return redisKey(q.prefix, "questions-project", tenantID, projectID)
}
func (q *Questions) sessionIndex(tenantID, sessionID string) string {
	return redisKey(q.prefix, "questions-session", tenantID, sessionID)
}

func loadRedisInteraction[T any](ctx context.Context, client redis.UniversalClient, key, id string) (T, string, error) {
	var zero T
	raw, err := client.HGet(ctx, key, id).Result()
	if err != nil {
		return zero, "", redisStoreError("read interaction", err)
	}
	value, err := decodeValue[T](raw)
	return value, raw, err
}

func deleteRedisInteractions(ctx context.Context, client redis.UniversalClient, records, sessionIndex string, projectIndex func(string) string, projectID func(string) (string, error)) error {
	ids, err := client.SMembers(ctx, sessionIndex).Result()
	if err != nil {
		return redisStoreError("list session interactions", err)
	}
	for _, id := range ids {
		raw, err := client.HGet(ctx, records, id).Result()
		if err != nil && err != redis.Nil {
			return redisStoreError("read session interaction", err)
		}
		if err == nil {
			project, err := projectID(raw)
			if err != nil {
				return err
			}
			if err := client.SRem(ctx, projectIndex(project), id).Err(); err != nil {
				return redisStoreError("remove project interaction", err)
			}
		}
		if err := client.HDel(ctx, records, id).Err(); err != nil {
			return redisStoreError("delete session interaction", err)
		}
	}
	return redisStoreError("delete session interaction index", client.Del(ctx, sessionIndex).Err())
}

func cloneRedisPermission(value domain.PermissionRequest) domain.PermissionRequest {
	value.Patterns = append([]string(nil), value.Patterns...)
	value.MetadataJSON = append([]byte(nil), value.MetadataJSON...)
	value.Always = append([]string(nil), value.Always...)
	if value.Tool != nil {
		tool := *value.Tool
		value.Tool = &tool
	}
	return value
}

func cloneRedisQuestion(value domain.QuestionRequest) domain.QuestionRequest {
	value.Questions = append([]domain.Question(nil), value.Questions...)
	for index := range value.Questions {
		value.Questions[index].Options = append([]domain.QuestionOption(nil), value.Questions[index].Options...)
	}
	value.Answers = cloneRedisAnswers(value.Answers)
	if value.Tool != nil {
		tool := *value.Tool
		value.Tool = &tool
	}
	return value
}

func cloneRedisAnswers(value [][]string) [][]string {
	result := make([][]string, len(value))
	for index := range value {
		result[index] = append([]string(nil), value[index]...)
	}
	return result
}
