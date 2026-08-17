package redisstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type Conversations struct {
	client redis.UniversalClient
	prefix string
}

func NewConversations(client redis.UniversalClient, prefix string) *Conversations {
	return &Conversations{client: client, prefix: platformPrefix(prefix)}
}

var appendMessageScript = redis.NewScript(`
if redis.call('HSETNX', KEYS[1], ARGV[1], ARGV[2]) == 1 then
  redis.call('RPUSH', KEYS[2], ARGV[1])
  return 1
end
return 0
`)

func (c *Conversations) Append(ctx context.Context, principal domain.Principal, message domain.Message) error {
	if principal.TenantID == "" || message.SessionID == "" || message.ID == "" {
		return fmt.Errorf("%w: message identity is incomplete", domain.ErrInvalid)
	}
	message.DataJSON = append([]byte(nil), message.DataJSON...)
	raw, err := encodeValue(message)
	if err != nil {
		return err
	}
	field := conversationField(message.ID, message.Role, message.Kind)
	err = appendMessageScript.Run(ctx, c.client, []string{
		c.itemsKey(principal.TenantID, message.SessionID),
		c.orderKey(principal.TenantID, message.SessionID),
	}, field, raw).Err()
	return redisStoreError("append conversation message", err)
}

var appendTextScript = redis.NewScript(`
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if raw then
  local value = cjson.decode(raw)
  value.Text = (value.Text or '') .. ARGV[2]
  redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(value))
  return 0
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[3])
redis.call('RPUSH', KEYS[2], ARGV[1])
return 1
`)

func (c *Conversations) AppendText(ctx context.Context, principal domain.Principal, sessionID, messageID, role, kind, text string) error {
	if sessionID == "" || messageID == "" || text == "" {
		return nil
	}
	message := domain.Message{
		ID: messageID, SessionID: sessionID, Role: role, Kind: kind,
		Text: text, CreatedAt: nowUTC(),
	}
	raw, err := encodeValue(message)
	if err != nil {
		return err
	}
	field := conversationField(messageID, role, kind)
	err = appendTextScript.Run(ctx, c.client, []string{
		c.itemsKey(principal.TenantID, sessionID), c.orderKey(principal.TenantID, sessionID),
	}, field, text, raw).Err()
	return redisStoreError("append conversation text", err)
}

func (c *Conversations) List(ctx context.Context, principal domain.Principal, sessionID string) ([]domain.Message, error) {
	fields, err := c.client.LRange(ctx, c.orderKey(principal.TenantID, sessionID), 0, -1).Result()
	if err != nil || len(fields) == 0 {
		return nil, redisStoreError("list conversation", err)
	}
	values, err := c.client.HMGet(ctx, c.itemsKey(principal.TenantID, sessionID), fields...).Result()
	if err != nil {
		return nil, redisStoreError("load conversation", err)
	}
	result := make([]domain.Message, 0, len(values))
	for _, item := range values {
		raw, ok := item.(string)
		if !ok || raw == "" {
			continue
		}
		message, err := decodeValue[domain.Message](raw)
		if err != nil {
			return nil, err
		}
		message.DataJSON = append([]byte(nil), message.DataJSON...)
		result = append(result, message)
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].CreatedAt.Before(result[right].CreatedAt) })
	return result, nil
}

func (c *Conversations) DeleteSession(ctx context.Context, principal domain.Principal, sessionID string) error {
	return redisStoreError("delete conversation", c.client.Del(ctx,
		c.itemsKey(principal.TenantID, sessionID), c.orderKey(principal.TenantID, sessionID),
	).Err())
}

func (c *Conversations) itemsKey(tenant, sessionID string) string {
	return redisKey(c.prefix, "conversation-items", tenant, sessionID)
}

func (c *Conversations) orderKey(tenant, sessionID string) string {
	return redisKey(c.prefix, "conversation-order", tenant, sessionID)
}

func conversationField(id, role, kind string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id + "\x00" + role + "\x00" + kind))
}
