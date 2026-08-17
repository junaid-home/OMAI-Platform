package redisstore

import (
	"context"
	"fmt"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Sessions struct {
	client redis.UniversalClient
	prefix string
}

func NewSessions(client redis.UniversalClient, prefix string) *Sessions {
	return &Sessions{client: client, prefix: platformPrefix(prefix)}
}

var resolveSessionScript = redis.NewScript(`
local id = redis.call('GET', KEYS[1])
if id then
  local raw = redis.call('HGET', KEYS[2], id)
  if not raw then return redis.error_reply('OMAI_NOT_FOUND session mapping is corrupt') end
  local value = cjson.decode(raw)
  if value.ProjectID ~= ARGV[5] or value.WorkspaceID ~= ARGV[6] or value.Root ~= ARGV[7] then
    return redis.error_reply('OMAI_CONFLICT session belongs to another workspace')
  end
  value.UpdatedAt = ARGV[3]
  if ARGV[4] ~= '' then value.Title = ARGV[4] end
  raw = cjson.encode(value)
  redis.call('HSET', KEYS[2], id, raw)
  redis.call('ZADD', KEYS[3], ARGV[8], id)
  return {0, raw}
end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('ZADD', KEYS[3], ARGV[8], ARGV[1])
return {1, ARGV[2]}
`)

func (s *Sessions) Resolve(ctx context.Context, principal domain.Principal, runtimeID, externalID, projectID, workspaceID, root, title string) (domain.Session, bool, error) {
	if principal.TenantID == "" || runtimeID == "" || externalID == "" || projectID == "" || workspaceID == "" || root == "" {
		return domain.Session{}, false, fmt.Errorf("%w: session identity is incomplete", domain.ErrInvalid)
	}
	id, err := randomPlatformID("ses_")
	if err != nil {
		return domain.Session{}, false, err
	}
	now := nowUTC()
	session := domain.Session{
		ID: id, ExternalSessionID: externalID, ProjectID: projectID, WorkspaceID: workspaceID,
		RuntimeID: runtimeID, TenantID: principal.TenantID, ActorID: principal.ActorID,
		Root: root, Title: title, CreatedAt: now, UpdatedAt: now,
	}
	raw, err := encodeValue(session)
	if err != nil {
		return domain.Session{}, false, err
	}
	result, err := resolveSessionScript.Run(ctx, s.client, []string{
		s.externalKey(principal.TenantID, runtimeID, externalID),
		s.itemsKey(principal.TenantID),
		s.projectKey(principal.TenantID, projectID),
	}, id, raw, now.Format(timeLayout), title, projectID, workspaceID, root, now.UnixMilli()).Slice()
	if err != nil {
		return domain.Session{}, false, redisStoreError("resolve session", err)
	}
	if len(result) != 2 {
		return domain.Session{}, false, fmt.Errorf("redis resolve session returned an invalid result")
	}
	encoded, ok := result[1].(string)
	if !ok {
		return domain.Session{}, false, fmt.Errorf("redis resolve session returned invalid data")
	}
	resolved, err := decodeValue[domain.Session](encoded)
	if err != nil {
		return domain.Session{}, false, err
	}
	created, _ := result[0].(int64)
	return resolved, created == 1, nil
}

func (s *Sessions) Get(ctx context.Context, principal domain.Principal, id string) (domain.Session, error) {
	raw, err := s.client.HGet(ctx, s.itemsKey(principal.TenantID), id).Result()
	if err != nil {
		return domain.Session{}, redisStoreError("get session", err)
	}
	value, err := decodeValue[domain.Session](raw)
	if err != nil {
		return domain.Session{}, err
	}
	if value.TenantID != principal.TenantID {
		return domain.Session{}, domain.ErrNotFound
	}
	return value, nil
}

func (s *Sessions) Find(ctx context.Context, principal domain.Principal, runtimeID, externalID string) (domain.Session, error) {
	id, err := s.client.Get(ctx, s.externalKey(principal.TenantID, runtimeID, externalID)).Result()
	if err != nil {
		return domain.Session{}, redisStoreError("find session", err)
	}
	return s.Get(ctx, principal, id)
}

func (s *Sessions) List(ctx context.Context, principal domain.Principal, projectID string, includeArchived bool) ([]domain.Session, error) {
	ids, err := s.client.ZRevRange(ctx, s.projectKey(principal.TenantID, projectID), 0, -1).Result()
	if err != nil || len(ids) == 0 {
		return nil, redisStoreError("list sessions", err)
	}
	values, err := s.client.HMGet(ctx, s.itemsKey(principal.TenantID), ids...).Result()
	if err != nil {
		return nil, redisStoreError("load sessions", err)
	}
	result := make([]domain.Session, 0, len(values))
	for _, item := range values {
		raw, ok := item.(string)
		if !ok || raw == "" {
			continue
		}
		value, err := decodeValue[domain.Session](raw)
		if err != nil {
			return nil, err
		}
		if value.TenantID == principal.TenantID && value.ProjectID == projectID && (includeArchived || !value.Archived) {
			result = append(result, value)
		}
	}
	sortSessions(result)
	return result, nil
}

var updateSessionScript = redis.NewScript(`
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then return redis.error_reply('OMAI_NOT_FOUND session') end
local value = cjson.decode(raw)
if ARGV[2] == '1' then value.Title = ARGV[3] end
if ARGV[4] == '1' then value.Archived = ARGV[5] == '1' end
if ARGV[6] == '1' then value.ProviderID = ARGV[7] end
if ARGV[8] == '1' then value.ModelID = ARGV[9] end
value.UpdatedAt = ARGV[10]
raw = cjson.encode(value)
redis.call('HSET', KEYS[1], ARGV[1], raw)
redis.call('ZADD', KEYS[2], ARGV[11], ARGV[1])
return raw
`)

func (s *Sessions) Update(ctx context.Context, principal domain.Principal, id string, patch domain.SessionPatch) (domain.Session, error) {
	current, err := s.Get(ctx, principal, id)
	if err != nil {
		return domain.Session{}, err
	}
	now := nowUTC()
	result, err := updateSessionScript.Run(ctx, s.client, []string{
		s.itemsKey(principal.TenantID), s.projectKey(principal.TenantID, current.ProjectID),
	}, id,
		boolFlag(patch.Title != nil), stringValue(patch.Title),
		boolFlag(patch.Archived != nil), boolValue(patch.Archived),
		boolFlag(patch.ProviderID != nil), stringValue(patch.ProviderID),
		boolFlag(patch.ModelID != nil), stringValue(patch.ModelID),
		now.Format(timeLayout), now.UnixMilli()).Text()
	if err != nil {
		return domain.Session{}, redisStoreError("update session", err)
	}
	return decodeValue[domain.Session](result)
}

func (s *Sessions) Touch(ctx context.Context, principal domain.Principal, id string) error {
	_, err := s.Update(ctx, principal, id, domain.SessionPatch{})
	return err
}

var deleteSessionScript = redis.NewScript(`
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then return redis.error_reply('OMAI_NOT_FOUND session') end
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('DEL', KEYS[3])
return 1
`)

func (s *Sessions) Delete(ctx context.Context, principal domain.Principal, id string) error {
	current, err := s.Get(ctx, principal, id)
	if err != nil {
		return err
	}
	err = deleteSessionScript.Run(ctx, s.client, []string{
		s.itemsKey(principal.TenantID),
		s.projectKey(principal.TenantID, current.ProjectID),
		s.externalKey(principal.TenantID, current.RuntimeID, current.ExternalSessionID),
	}, id).Err()
	return redisStoreError("delete session", err)
}

func (s *Sessions) itemsKey(tenant string) string {
	return redisKey(s.prefix, "sessions", tenant)
}

func (s *Sessions) projectKey(tenant, project string) string {
	return redisKey(s.prefix, "sessions-by-project", tenant, project)
}

func (s *Sessions) externalKey(tenant, runtimeID, externalID string) string {
	return redisKey(s.prefix, "session-external", tenant, runtimeID, externalID)
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func boolFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func boolValue(value *bool) string {
	if value != nil && *value {
		return "1"
	}
	return "0"
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
