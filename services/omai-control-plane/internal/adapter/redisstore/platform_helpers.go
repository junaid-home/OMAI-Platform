package redisstore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

const defaultPlatformPrefix = "omai:platform:"

func platformPrefix(value string) string {
	if value == "" {
		return defaultPlatformPrefix
	}
	if !strings.HasSuffix(value, ":") {
		value += ":"
	}
	return value
}

func redisKey(prefix, kind string, values ...string) string {
	result := prefix + kind
	for _, value := range values {
		result += ":" + base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	return result
}

func encodeValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode redis value: %w", err)
	}
	return string(encoded), nil
}

func decodeValue[T any](value string) (T, error) {
	var result T
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return result, fmt.Errorf("decode redis value: %w", err)
	}
	return result, nil
}

func randomPlatformID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate platform id: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func redisStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, redis.Nil) {
		return domain.ErrNotFound
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "OMAI_CONFLICT"):
		return domain.ErrConflict
	case strings.Contains(message, "OMAI_FORBIDDEN"):
		return domain.ErrForbidden
	case strings.Contains(message, "OMAI_NOT_FOUND"):
		return domain.ErrNotFound
	default:
		return fmt.Errorf("redis %s: %w", operation, err)
	}
}

func sortSessions(values []domain.Session) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})
}

func sortProjects(values []domain.Project) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].UpdatedAt.Equal(values[right].UpdatedAt) {
			return values[left].ID < values[right].ID
		}
		return values[left].UpdatedAt.After(values[right].UpdatedAt)
	})
}

func nowUTC() time.Time { return time.Now().UTC() }
