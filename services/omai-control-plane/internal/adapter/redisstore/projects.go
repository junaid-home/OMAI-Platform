package redisstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Projects struct {
	client redis.UniversalClient
	prefix string
}

func NewProjects(client redis.UniversalClient, prefix string) *Projects {
	return &Projects{client: client, prefix: platformPrefix(prefix)}
}

var resolveProjectScript = redis.NewScript(`
local id = redis.call('GET', KEYS[1])
if id then
  local raw = redis.call('HGET', KEYS[2], id)
  if not raw then return redis.error_reply('OMAI_NOT_FOUND project mapping is corrupt') end
  if ARGV[3] ~= '' then
    local value = cjson.decode(raw)
    if value.Name ~= ARGV[3] then
      value.Name = ARGV[3]
      value.UpdatedAt = ARGV[4]
      raw = cjson.encode(value)
      redis.call('HSET', KEYS[2], id, raw)
      redis.call('ZADD', KEYS[3], ARGV[5], id)
    end
  end
  return {0, raw}
end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('ZADD', KEYS[3], ARGV[5], ARGV[1])
return {1, ARGV[2]}
`)

func (p *Projects) Resolve(ctx context.Context, principal domain.Principal, workspace domain.Workspace, name string) (domain.Project, bool, error) {
	if principal.TenantID == "" || workspace.ID == "" || workspace.TenantID != principal.TenantID {
		return domain.Project{}, false, fmt.Errorf("%w: project identity is incomplete", domain.ErrInvalid)
	}
	now := nowUTC()
	id := "prj_" + strings.TrimPrefix(workspace.ID, "wsp_")
	project := domain.Project{
		ID: id, WorkspaceID: workspace.ID, TenantID: principal.TenantID,
		Root: workspace.Root, RepoRoot: workspace.RepoRoot, Name: name,
		CreatedAt: now, UpdatedAt: now,
	}
	raw, err := encodeValue(project)
	if err != nil {
		return domain.Project{}, false, err
	}
	result, err := resolveProjectScript.Run(ctx, p.client, []string{
		p.workspaceKey(principal.TenantID, workspace.ID),
		p.itemsKey(principal.TenantID),
		p.indexKey(principal.TenantID),
	}, id, raw, name, now.Format(timeLayout), now.UnixMilli()).Slice()
	if err != nil {
		return domain.Project{}, false, redisStoreError("resolve project", err)
	}
	if len(result) != 2 {
		return domain.Project{}, false, fmt.Errorf("redis resolve project returned an invalid result")
	}
	encoded, ok := result[1].(string)
	if !ok {
		return domain.Project{}, false, fmt.Errorf("redis resolve project returned invalid data")
	}
	resolved, err := decodeValue[domain.Project](encoded)
	if err != nil {
		return domain.Project{}, false, err
	}
	created, _ := result[0].(int64)
	return resolved, created == 1, nil
}

func (p *Projects) Get(ctx context.Context, principal domain.Principal, id string) (domain.Project, error) {
	raw, err := p.client.HGet(ctx, p.itemsKey(principal.TenantID), id).Result()
	if err != nil {
		return domain.Project{}, redisStoreError("get project", err)
	}
	value, err := decodeValue[domain.Project](raw)
	if err != nil {
		return domain.Project{}, err
	}
	if value.TenantID != principal.TenantID {
		return domain.Project{}, domain.ErrNotFound
	}
	return value, nil
}

func (p *Projects) List(ctx context.Context, principal domain.Principal) ([]domain.Project, error) {
	ids, err := p.client.ZRevRange(ctx, p.indexKey(principal.TenantID), 0, -1).Result()
	if err != nil || len(ids) == 0 {
		return nil, redisStoreError("list projects", err)
	}
	values, err := p.client.HMGet(ctx, p.itemsKey(principal.TenantID), ids...).Result()
	if err != nil {
		return nil, redisStoreError("load projects", err)
	}
	result := make([]domain.Project, 0, len(values))
	for _, item := range values {
		raw, ok := item.(string)
		if !ok || raw == "" {
			continue
		}
		value, err := decodeValue[domain.Project](raw)
		if err != nil {
			return nil, err
		}
		if value.TenantID == principal.TenantID {
			result = append(result, value)
		}
	}
	sortProjects(result)
	return result, nil
}

var updateProjectScript = redis.NewScript(`
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then return redis.error_reply('OMAI_NOT_FOUND project') end
local value = cjson.decode(raw)
if ARGV[2] == '1' then value.Name = ARGV[3] end
if ARGV[4] == '1' then value.IconColor = ARGV[5] end
if ARGV[6] == '1' then value.IconOverride = ARGV[7] end
if ARGV[8] == '1' then value.StartupCommand = ARGV[9] end
value.UpdatedAt = ARGV[10]
raw = cjson.encode(value)
redis.call('HSET', KEYS[1], ARGV[1], raw)
redis.call('ZADD', KEYS[2], ARGV[11], ARGV[1])
return raw
`)

func (p *Projects) Update(ctx context.Context, principal domain.Principal, id string, patch domain.ProjectPatch) (domain.Project, error) {
	now := nowUTC()
	result, err := updateProjectScript.Run(ctx, p.client, []string{
		p.itemsKey(principal.TenantID), p.indexKey(principal.TenantID),
	}, id,
		boolFlag(patch.Name != nil), stringValue(patch.Name),
		boolFlag(patch.IconColor != nil), stringValue(patch.IconColor),
		boolFlag(patch.IconOverride != nil), stringValue(patch.IconOverride),
		boolFlag(patch.StartupCommand != nil), stringValue(patch.StartupCommand),
		now.Format(timeLayout), now.UnixMilli()).Text()
	if err != nil {
		return domain.Project{}, redisStoreError("update project", err)
	}
	return decodeValue[domain.Project](result)
}

func (p *Projects) itemsKey(tenant string) string {
	return redisKey(p.prefix, "projects", tenant)
}

func (p *Projects) indexKey(tenant string) string {
	return redisKey(p.prefix, "projects-by-updated", tenant)
}

func (p *Projects) workspaceKey(tenant, workspaceID string) string {
	return redisKey(p.prefix, "project-workspace", tenant, workspaceID)
}
