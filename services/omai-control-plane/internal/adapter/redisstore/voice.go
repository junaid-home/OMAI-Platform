package redisstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
	"github.com/redis/go-redis/v9"
)

type VoiceLeases struct {
	client redis.UniversalClient
	prefix string
}

func NewVoiceLeases(client redis.UniversalClient, prefix string) *VoiceLeases {
	if prefix == "" {
		prefix = "omai:voice:"
	}
	return &VoiceLeases{client: client, prefix: prefix}
}
func NewClient(address, password string, database int) redis.UniversalClient {
	return redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{address}, Password: password, DB: database, MaxRetries: 3, MinRetryBackoff: 10 * time.Millisecond, MaxRetryBackoff: 250 * time.Millisecond})
}

func (s *VoiceLeases) Issue(ctx context.Context, digest string, admission domain.VoiceAdmission) error {
	data, err := json.Marshal(admission)
	if err != nil {
		return err
	}
	ttl := time.Until(admission.ExpiresAt)
	if ttl <= 0 {
		return domain.ErrInvalid
	}
	ok, err := s.client.SetNX(ctx, s.prefix+"ticket:"+digest, data, ttl).Result()
	if err != nil {
		return fmt.Errorf("redis issue voice ticket: %w", err)
	}
	if !ok {
		return domain.ErrConflict
	}
	return nil
}

var redeemScript = redis.NewScript(`
local raw = redis.call('GETDEL', KEYS[1])
if not raw then return {0, 'NOT_FOUND'} end
local value = cjson.decode(raw)
local active = ARGV[1] .. value.SubjectKey
redis.call('ZREMRANGEBYSCORE', active, '-inf', ARGV[2])
if redis.call('ZCARD', active) >= tonumber(ARGV[3]) then return {0, 'LIMIT'} end
redis.call('ZADD', active, ARGV[4], ARGV[5])
redis.call('PEXPIRE', active, ARGV[6])
redis.call('SET', KEYS[2], raw, 'PX', ARGV[6], 'NX')
return {1, raw}
`)

func (s *VoiceLeases) Redeem(ctx context.Context, digest, sessionID string, maxSessions int, ttl time.Duration) (domain.VoiceLease, error) {
	token, err := randomToken()
	if err != nil {
		return domain.VoiceLease{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	result, err := redeemScript.Run(ctx, s.client, []string{s.prefix + "ticket:" + digest, s.prefix + "lease:" + token}, s.prefix+"active:", now.UnixMilli(), maxSessions, expires.UnixMilli(), token, ttl.Milliseconds()).Slice()
	if err != nil {
		return domain.VoiceLease{}, fmt.Errorf("redis redeem voice ticket: %w", err)
	}
	if len(result) != 2 || result[0].(int64) != 1 {
		if len(result) == 2 && result[1] == "LIMIT" {
			return domain.VoiceLease{}, domain.ErrUnavailable
		}
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	var admission domain.VoiceAdmission
	if err := json.Unmarshal([]byte(result[1].(string)), &admission); err != nil {
		return domain.VoiceLease{}, fmt.Errorf("decode voice admission: %w", err)
	}
	lease := domain.VoiceLease{Token: token, SessionID: sessionID, Admission: admission, ExpiresAt: expires}
	encoded, _ := json.Marshal(lease)
	if err := s.client.Set(ctx, s.prefix+"lease:"+token, encoded, ttl).Err(); err != nil {
		return domain.VoiceLease{}, err
	}
	return lease, nil
}
func (s *VoiceLeases) Get(ctx context.Context, token string) (domain.VoiceLease, error) {
	data, err := s.client.Get(ctx, s.prefix+"lease:"+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.VoiceLease{}, err
	}
	var lease domain.VoiceLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return domain.VoiceLease{}, err
	}
	return lease, nil
}

var heartbeatScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2], 'XX')
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`)

func (s *VoiceLeases) Heartbeat(ctx context.Context, token string, ttl time.Duration) (domain.VoiceLease, error) {
	lease, err := s.Get(ctx, token)
	if err != nil {
		return domain.VoiceLease{}, err
	}
	lease.ExpiresAt = time.Now().UTC().Add(ttl)
	data, _ := json.Marshal(lease)
	active := s.prefix + "active:" + lease.Admission.SubjectKey
	updated, err := heartbeatScript.Run(
		ctx,
		s.client,
		[]string{s.prefix + "lease:" + token, active},
		data,
		ttl.Milliseconds(),
		lease.ExpiresAt.UnixMilli(),
		token,
	).Int()
	if err != nil {
		return domain.VoiceLease{}, err
	}
	if updated != 1 {
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	return lease, nil
}
func (s *VoiceLeases) Release(ctx context.Context, token string) error {
	lease, err := s.Get(ctx, token)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.prefix+"lease:"+token)
	pipe.ZRem(ctx, s.prefix+"active:"+lease.Admission.SubjectKey, token)
	_, err = pipe.Exec(ctx)
	return err
}
func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "vls_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type VoiceDispatches struct {
	client redis.UniversalClient
	prefix string
}

func NewVoiceDispatches(client redis.UniversalClient, prefix string) *VoiceDispatches {
	if prefix == "" {
		prefix = "omai:voice:dispatch:"
	}
	return &VoiceDispatches{client: client, prefix: prefix}
}

var beginDispatchScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  redis.call('HSET', KEYS[1], 'fingerprint', ARGV[1], 'state', 'running')
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  return {1, ''}
end
if redis.call('HGET', KEYS[1], 'fingerprint') ~= ARGV[1] then return {-1, ''} end
if redis.call('HGET', KEYS[1], 'state') == 'done' then
  return {3, redis.call('HGET', KEYS[1], 'result') or ''}
end
return {2, ''}
`)

func (s *VoiceDispatches) Begin(ctx context.Context, key, fingerprint string, ttl time.Duration) (port.VoiceDispatchRecord, port.VoiceDispatchState, error) {
	result, err := beginDispatchScript.Run(ctx, s.client, []string{s.prefix + key}, fingerprint, ttl.Milliseconds()).Slice()
	if err != nil {
		return port.VoiceDispatchRecord{}, 0, fmt.Errorf("redis begin voice dispatch: %w", err)
	}
	if len(result) != 2 {
		return port.VoiceDispatchRecord{}, 0, errors.New("redis returned an invalid voice dispatch result")
	}
	state, ok := result[0].(int64)
	if !ok {
		return port.VoiceDispatchRecord{}, 0, errors.New("redis returned an invalid voice dispatch state")
	}
	if state == -1 {
		return port.VoiceDispatchRecord{}, 0, domain.ErrConflict
	}
	record := port.VoiceDispatchRecord{Fingerprint: fingerprint}
	if value, ok := result[1].(string); ok {
		record.Result = []byte(value)
	}
	switch state {
	case 1:
		return record, port.VoiceDispatchStarted, nil
	case 2:
		return record, port.VoiceDispatchRunning, nil
	case 3:
		return record, port.VoiceDispatchCached, nil
	default:
		return port.VoiceDispatchRecord{}, 0, errors.New("redis returned an unknown voice dispatch state")
	}
}

var completeDispatchScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'fingerprint') ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], 'state', 'done', 'result', ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

func (s *VoiceDispatches) Complete(ctx context.Context, key, fingerprint string, result []byte, ttl time.Duration) error {
	ok, err := completeDispatchScript.Run(ctx, s.client, []string{s.prefix + key}, fingerprint, result, ttl.Milliseconds()).Int()
	if err != nil {
		return fmt.Errorf("redis complete voice dispatch: %w", err)
	}
	if ok != 1 {
		return domain.ErrConflict
	}
	return nil
}

var abortDispatchScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'fingerprint') == ARGV[1] and redis.call('HGET', KEYS[1], 'state') == 'running' then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func (s *VoiceDispatches) Abort(ctx context.Context, key, fingerprint string) error {
	if err := abortDispatchScript.Run(ctx, s.client, []string{s.prefix + key}, fingerprint).Err(); err != nil {
		return fmt.Errorf("redis abort voice dispatch: %w", err)
	}
	return nil
}
