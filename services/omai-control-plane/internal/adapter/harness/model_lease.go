package harness

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type modelAuthorization struct {
	TenantID  string
	ActorID   string
	SessionID string
	Provider  string
	Model     string
	RouteID   string
	ExpiresAt time.Time
}

type LeaseStore struct {
	mu      sync.Mutex
	baseURL string
	ttl     time.Duration
	limit   int
	leases  map[[sha256.Size]byte]modelAuthorization
	now     func() time.Time
}

func NewLeaseStore(baseURL string, ttl time.Duration, limit int) (*LeaseStore, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("harness model edge must use a loopback HTTP URL")
	}
	host, port, err := net.SplitHostPort(endpoint.Host)
	ip := net.ParseIP(host)
	if err != nil || port == "" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return nil, errors.New("harness model edge must use a loopback HTTP URL")
	}
	if ttl <= 0 || limit <= 0 {
		return nil, errors.New("harness model lease TTL and limit must be positive")
	}
	return &LeaseStore{baseURL: strings.TrimRight(baseURL, "/"), ttl: ttl, limit: limit, leases: make(map[[sha256.Size]byte]modelAuthorization), now: time.Now}, nil
}

func (s *LeaseStore) Issue(prompt domain.Prompt) (ModelLease, error) {
	for name, value := range map[string]string{
		"tenant": prompt.Principal.TenantID, "actor": prompt.Principal.ActorID,
		"session": prompt.SessionID, "provider": prompt.ProviderID, "model": prompt.ModelID,
	} {
		if !validModelIdentity(value) {
			return ModelLease{}, fmt.Errorf("invalid %s identity for model lease", name)
		}
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return ModelLease{}, fmt.Errorf("create model capability: %w", err)
	}
	routeBytes := make([]byte, 18)
	if _, err := rand.Read(routeBytes); err != nil {
		return ModelLease{}, fmt.Errorf("create model route: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	routeID := "route-" + base64.RawURLEncoding.EncodeToString(routeBytes)
	now := s.now().UTC()
	authorization := modelAuthorization{
		TenantID: prompt.Principal.TenantID, ActorID: prompt.Principal.ActorID, SessionID: prompt.SessionID,
		Provider: prompt.ProviderID, Model: prompt.ModelID, RouteID: routeID, ExpiresAt: now.Add(s.ttl),
	}
	hash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	s.pruneLocked(now)
	if len(s.leases) >= s.limit {
		s.mu.Unlock()
		return ModelLease{}, errors.New("model capability limit reached")
	}
	s.leases[hash] = authorization
	s.mu.Unlock()
	return ModelLease{Token: token, RouteID: routeID, BaseURL: s.baseURL + "/v1", Expiry: authorization.ExpiresAt}, nil
}

func (s *LeaseStore) Revoke(token string) {
	hash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	delete(s.leases, hash)
	s.mu.Unlock()
}

func (s *LeaseStore) authorize(token, routeID string) (modelAuthorization, bool) {
	if len(token) < 32 || len(token) > 256 {
		return modelAuthorization{}, false
	}
	hash := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	s.mu.Lock()
	authorization, ok := s.leases[hash]
	if ok && now.After(authorization.ExpiresAt) {
		delete(s.leases, hash)
		ok = false
	}
	if ok && authorization.RouteID != routeID {
		ok = false
	}
	s.mu.Unlock()
	return authorization, ok
}

func (s *LeaseStore) pruneLocked(now time.Time) {
	for hash, lease := range s.leases {
		if now.After(lease.ExpiresAt) {
			delete(s.leases, hash)
		}
	}
}

func validModelIdentity(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
