package redisstore

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Events struct {
	client redis.UniversalClient
	prefix string
	max    int
}

const eventSubscriberTTL = 2 * time.Minute

func NewEvents(client redis.UniversalClient, prefix string, max int) *Events {
	if max < 1 {
		max = 1
	}
	return &Events{client: client, prefix: platformPrefix(prefix), max: max}
}

var publishEventScript = redis.NewScript(`
local tenant = redis.call('HGET', KEYS[1], 'tenant')
if tenant and tenant ~= ARGV[1] then return redis.error_reply('OMAI_FORBIDDEN event stream') end
if not tenant then redis.call('HSET', KEYS[1], 'tenant', ARGV[1], 'next', 0) end
local sequence = redis.call('HINCRBY', KEYS[1], 'next', 1)
local value = cjson.decode(ARGV[2])
value.Sequence = sequence
local raw = cjson.encode(value)
redis.call('RPUSH', KEYS[2], raw)
redis.call('LTRIM', KEYS[2], -tonumber(ARGV[3]), -1)
redis.call('PUBLISH', KEYS[3], raw)
return raw
`)

func (e *Events) Publish(ctx context.Context, principal domain.Principal, event domain.Event) (domain.Event, error) {
	if event.SessionID == "" || principal.TenantID == "" {
		return domain.Event{}, fmt.Errorf("%w: event identity is incomplete", domain.ErrInvalid)
	}
	if event.At.IsZero() {
		event.At = nowUTC()
	}
	event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
	raw, err := encodeValue(event)
	if err != nil {
		return domain.Event{}, err
	}
	result, err := publishEventScript.Run(ctx, e.client, []string{
		e.metaKey(principal.TenantID, event.SessionID),
		e.listKey(principal.TenantID, event.SessionID),
		e.channelKey(principal.TenantID, event.SessionID),
	}, principal.TenantID, raw, e.max).Text()
	if err != nil {
		return domain.Event{}, redisStoreError("publish event", err)
	}
	published, err := decodeValue[domain.Event](result)
	if err != nil {
		return domain.Event{}, err
	}
	published.PayloadJSON = append([]byte(nil), published.PayloadJSON...)
	return published, nil
}

func (e *Events) Subscribe(ctx context.Context, principal domain.Principal, sessionID string, since uint64) ([]domain.Event, <-chan domain.Event, func(), error) {
	if principal.TenantID == "" || sessionID == "" {
		return nil, nil, nil, fmt.Errorf("%w: event subscription identity is incomplete", domain.ErrInvalid)
	}
	channelKey := e.channelKey(principal.TenantID, sessionID)
	pubsub := e.client.Subscribe(ctx, channelKey)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, nil, nil, redisStoreError("subscribe events", err)
	}
	subscribersKey := e.subscribersKey(principal.TenantID, sessionID)
	if err := e.client.Incr(ctx, subscribersKey).Err(); err != nil {
		_ = pubsub.Close()
		return nil, nil, nil, redisStoreError("register event subscriber", err)
	}
	if err := e.client.Expire(ctx, subscribersKey, eventSubscriberTTL).Err(); err != nil {
		_ = e.client.Decr(ctx, subscribersKey).Err()
		_ = pubsub.Close()
		return nil, nil, nil, redisStoreError("lease event subscriber", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = pubsub.Close()
			cleanup, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			remaining, err := e.client.Decr(cleanup, subscribersKey).Result()
			if err == nil && remaining <= 0 {
				_ = e.client.Del(cleanup, subscribersKey).Err()
			}
		})
	}
	fail := func(err error) ([]domain.Event, <-chan domain.Event, func(), error) {
		stop()
		return nil, nil, nil, err
	}

	pipe := e.client.Pipeline()
	tenantCmd := pipe.HGet(ctx, e.metaKey(principal.TenantID, sessionID), "tenant")
	nextCmd := pipe.HGet(ctx, e.metaKey(principal.TenantID, sessionID), "next")
	eventsCmd := pipe.LRange(ctx, e.listKey(principal.TenantID, sessionID), 0, -1)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return fail(redisStoreError("read event replay", err))
	}
	tenant, err := tenantCmd.Result()
	if err != nil {
		return fail(redisStoreError("read event stream", err))
	}
	if tenant != principal.TenantID {
		return fail(domain.ErrForbidden)
	}
	nextText, err := nextCmd.Result()
	if err != nil {
		return fail(redisStoreError("read event cursor", err))
	}
	next, err := strconv.ParseUint(nextText, 10, 64)
	if err != nil {
		return fail(fmt.Errorf("decode redis event cursor: %w", err))
	}
	if since > next {
		return fail(fmt.Errorf("%w: cursor is ahead of the stream", domain.ErrInvalid))
	}
	rawEvents, err := eventsCmd.Result()
	if err != nil {
		return fail(redisStoreError("read event replay", err))
	}
	stored := make([]domain.Event, 0, len(rawEvents))
	for _, raw := range rawEvents {
		event, err := decodeValue[domain.Event](raw)
		if err != nil {
			return fail(err)
		}
		stored = append(stored, event)
	}
	if since != 0 && len(stored) > 0 && since+1 < stored[0].Sequence {
		return fail(domain.ErrReplayTooOld)
	}
	replay := make([]domain.Event, 0, len(stored))
	last := since
	for _, event := range stored {
		if event.Sequence <= since {
			continue
		}
		event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
		replay = append(replay, event)
		last = event.Sequence
	}

	updates := make(chan domain.Event, 256)
	messages := pubsub.Channel(redis.WithChannelSize(512))
	go func(cursor uint64) {
		defer close(updates)
		defer stop()
		heartbeat := time.NewTicker(eventSubscriberTTL / 4)
		defer heartbeat.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-heartbeat.C:
				if err := e.client.Expire(watchCtx, subscribersKey, eventSubscriberTTL).Err(); err != nil {
					return
				}
			case message, ok := <-messages:
				if !ok {
					return
				}
				event, err := decodeValue[domain.Event](message.Payload)
				if err != nil || event.Sequence <= cursor {
					continue
				}
				cursor = event.Sequence
				event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
				select {
				case updates <- event:
				case <-watchCtx.Done():
					return
				default:
					return
				}
			}
		}
	}(last)
	return replay, updates, stop, nil
}

var deleteEventStreamScript = redis.NewScript(`
if tonumber(redis.call('GET', KEYS[3]) or '0') > 0 then
  return redis.error_reply('OMAI_CONFLICT event stream has active subscribers')
end
redis.call('DEL', KEYS[1], KEYS[2], KEYS[3])
return 1
`)

func (e *Events) DeleteSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	err := deleteEventStreamScript.Run(ctx, e.client, []string{
		e.metaKey(principal.TenantID, sessionID),
		e.listKey(principal.TenantID, sessionID),
		e.subscribersKey(principal.TenantID, sessionID),
	}).Err()
	return redisStoreError("delete event stream", err)
}

func (e *Events) metaKey(tenant, sessionID string) string {
	return redisKey(e.prefix, "event-meta", tenant, sessionID)
}

func (e *Events) listKey(tenant, sessionID string) string {
	return redisKey(e.prefix, "events", tenant, sessionID)
}

func (e *Events) channelKey(tenant, sessionID string) string {
	return redisKey(e.prefix, "event-channel", tenant, sessionID)
}

func (e *Events) subscribersKey(tenant, sessionID string) string {
	return redisKey(e.prefix, "event-subscribers", tenant, sessionID)
}
