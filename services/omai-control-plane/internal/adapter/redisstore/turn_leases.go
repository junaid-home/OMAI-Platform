package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type TurnLeases struct {
	client redis.UniversalClient
	prefix string
}

func NewTurnLeases(client redis.UniversalClient, prefix string) *TurnLeases {
	return &TurnLeases{client: client, prefix: platformPrefix(prefix)}
}

func (s *TurnLeases) Acquire(ctx context.Context, principal domain.Principal, sessionID, owner string, ttl time.Duration) (bool, error) {
	if principal.TenantID == "" || sessionID == "" || owner == "" || ttl <= 0 {
		return false, fmt.Errorf("%w: turn lease identity is incomplete", domain.ErrInvalid)
	}
	acquired, err := s.client.SetNX(ctx, s.key(principal.TenantID, sessionID), owner, ttl).Result()
	if err != nil {
		return false, redisStoreError("acquire turn lease", err)
	}
	return acquired, nil
}

var renewTurnLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

func (s *TurnLeases) Renew(ctx context.Context, principal domain.Principal, sessionID, owner string, ttl time.Duration) (bool, error) {
	result, err := renewTurnLeaseScript.Run(ctx, s.client, []string{s.key(principal.TenantID, sessionID)}, owner, ttl.Milliseconds()).Int()
	if err != nil {
		return false, redisStoreError("renew turn lease", err)
	}
	return result == 1, nil
}

var releaseTurnLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])
`)

func (s *TurnLeases) Release(ctx context.Context, principal domain.Principal, sessionID, owner string) error {
	return redisStoreError("release turn lease", releaseTurnLeaseScript.Run(ctx, s.client,
		[]string{s.key(principal.TenantID, sessionID)}, owner).Err())
}

func (s *TurnLeases) Active(ctx context.Context, principal domain.Principal, sessionID string) (bool, error) {
	count, err := s.client.Exists(ctx, s.key(principal.TenantID, sessionID)).Result()
	if err != nil {
		return false, redisStoreError("read turn lease", err)
	}
	return count == 1, nil
}

func (s *TurnLeases) key(tenant, sessionID string) string {
	return redisKey(s.prefix, "turn-lease", tenant, sessionID)
}
