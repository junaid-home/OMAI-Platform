package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type VoiceLeases struct {
	mu      sync.Mutex
	tickets map[string]domain.VoiceAdmission
	leases  map[string]domain.VoiceLease
}

type voiceDispatchEntry struct {
	fingerprint string
	result      []byte
	running     bool
	expiresAt   time.Time
}

type VoiceDispatches struct {
	mu      sync.Mutex
	entries map[string]voiceDispatchEntry
}

func NewVoiceDispatches() *VoiceDispatches {
	return &VoiceDispatches{entries: make(map[string]voiceDispatchEntry)}
}

func (s *VoiceDispatches) Begin(_ context.Context, key, fingerprint string, ttl time.Duration) (port.VoiceDispatchRecord, port.VoiceDispatchState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for candidate, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, candidate)
		}
	}
	entry, exists := s.entries[key]
	if exists {
		if entry.fingerprint != fingerprint {
			return port.VoiceDispatchRecord{}, 0, domain.ErrConflict
		}
		if entry.running {
			return port.VoiceDispatchRecord{Fingerprint: fingerprint}, port.VoiceDispatchRunning, nil
		}
		return port.VoiceDispatchRecord{Fingerprint: fingerprint, Result: append([]byte(nil), entry.result...)}, port.VoiceDispatchCached, nil
	}
	s.entries[key] = voiceDispatchEntry{fingerprint: fingerprint, running: true, expiresAt: now.Add(ttl)}
	return port.VoiceDispatchRecord{Fingerprint: fingerprint}, port.VoiceDispatchStarted, nil
}

func (s *VoiceDispatches) Complete(_ context.Context, key, fingerprint string, result []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists || entry.fingerprint != fingerprint {
		return domain.ErrConflict
	}
	entry.running = false
	entry.result = append([]byte(nil), result...)
	entry.expiresAt = time.Now().UTC().Add(ttl)
	s.entries[key] = entry
	return nil
}

func (s *VoiceDispatches) Abort(_ context.Context, key, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, exists := s.entries[key]; exists && entry.running && entry.fingerprint == fingerprint {
		delete(s.entries, key)
	}
	return nil
}

func NewVoiceLeases() *VoiceLeases {
	return &VoiceLeases{tickets: make(map[string]domain.VoiceAdmission), leases: make(map[string]domain.VoiceLease)}
}

func (s *VoiceLeases) Issue(_ context.Context, digest string, admission domain.VoiceAdmission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now().UTC())
	if _, exists := s.tickets[digest]; exists {
		return domain.ErrConflict
	}
	s.tickets[digest] = cloneAdmission(admission)
	return nil
}

func (s *VoiceLeases) Redeem(_ context.Context, digest, sessionID string, maxSessions int, ttl time.Duration) (domain.VoiceLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.prune(now)
	admission, exists := s.tickets[digest]
	if !exists || !admission.ExpiresAt.After(now) {
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	delete(s.tickets, digest)
	active := 0
	for _, lease := range s.leases {
		if lease.Admission.TenantID == admission.TenantID && lease.Admission.ActorID == admission.ActorID {
			active++
		}
	}
	if active >= maxSessions {
		return domain.VoiceLease{}, fmt.Errorf("%w: voice session limit", domain.ErrUnavailable)
	}
	token, err := randomID("vls_")
	if err != nil {
		return domain.VoiceLease{}, err
	}
	lease := domain.VoiceLease{Token: token, SessionID: sessionID, Admission: cloneAdmission(admission), ExpiresAt: now.Add(ttl)}
	s.leases[token] = lease
	return cloneLease(lease), nil
}
func (s *VoiceLeases) Get(_ context.Context, token string) (domain.VoiceLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(time.Now().UTC())
	value, ok := s.leases[token]
	if !ok {
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	return cloneLease(value), nil
}
func (s *VoiceLeases) Heartbeat(_ context.Context, token string, ttl time.Duration) (domain.VoiceLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.prune(now)
	value, ok := s.leases[token]
	if !ok {
		return domain.VoiceLease{}, domain.ErrNotFound
	}
	value.ExpiresAt = now.Add(ttl)
	s.leases[token] = value
	return cloneLease(value), nil
}
func (s *VoiceLeases) Release(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.leases, token)
	return nil
}
func (s *VoiceLeases) prune(now time.Time) {
	for key, value := range s.tickets {
		if !value.ExpiresAt.After(now) {
			delete(s.tickets, key)
		}
	}
	for key, value := range s.leases {
		if !value.ExpiresAt.After(now) {
			delete(s.leases, key)
		}
	}
}
func cloneAdmission(value domain.VoiceAdmission) domain.VoiceAdmission {
	value.Permissions = append([]string(nil), value.Permissions...)
	return value
}
func cloneLease(value domain.VoiceLease) domain.VoiceLease {
	value.Admission = cloneAdmission(value.Admission)
	return value
}
