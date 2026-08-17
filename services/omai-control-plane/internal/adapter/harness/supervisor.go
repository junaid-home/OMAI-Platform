package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type Supervisor struct {
	driver   Driver
	runner   Runner
	sessions SessionStore
	models   ModelLeaseIssuer

	mu             sync.Mutex
	activeSessions map[string]context.CancelFunc
	activeExternal map[string]struct{}
}

func NewSupervisor(driver Driver, runner Runner, sessions SessionStore, models ModelLeaseIssuer) *Supervisor {
	return &Supervisor{
		driver: driver, runner: runner, sessions: sessions, models: models,
		activeSessions: make(map[string]context.CancelFunc),
		activeExternal: make(map[string]struct{}),
	}
}

func (s *Supervisor) Descriptor() domain.RuntimeDescriptor { return s.driver.Descriptor() }

func (s *Supervisor) Health(context.Context) domain.RuntimeHealth {
	result := domain.RuntimeHealth{
		RuntimeID: s.driver.Descriptor().ID, Version: s.driver.Descriptor().Version,
		Authenticated: true, CheckedAt: time.Now().UTC(),
	}
	if err := s.runner.Available(s.driver.Command()); err != nil {
		result.Reason = err.Error()
		return result
	}
	result.Available = true
	return result
}

func (s *Supervisor) Run(ctx context.Context, prompt domain.Prompt, emit func(domain.RuntimeEvent) error) error {
	if strings.TrimSpace(prompt.SessionID) == "" || strings.TrimSpace(prompt.ExternalSessionID) == "" {
		return fmt.Errorf("%w: session identifiers are required", domain.ErrInvalid)
	}
	runCtx, cancel := context.WithCancel(ctx)
	if !s.begin(prompt.SessionID, prompt.ExternalSessionID, cancel) {
		cancel()
		return ErrActiveRun
	}
	defer func() {
		s.finish(prompt.SessionID, prompt.ExternalSessionID)
		cancel()
	}()

	lease, err := s.models.Issue(prompt)
	if err != nil {
		return fmt.Errorf("issue model capability: %w", err)
	}
	defer s.models.Revoke(lease.Token)

	harnessSessionID, _ := s.sessions.Get(prompt.ExternalSessionID)
	invocation, err := s.driver.Invocation(prompt, harnessSessionID, lease)
	if err != nil {
		return err
	}
	decoder := s.driver.NewDecoder()
	if err := emit(domain.RuntimeEvent{Kind: domain.RuntimeEventStatus, Status: "running", At: time.Now().UTC()}); err != nil {
		return err
	}
	result, runErr := s.runner.Run(runCtx, invocation, func(line []byte) error {
		decoded, err := decoder.Decode(line)
		if err != nil {
			return err
		}
		if decoded.HarnessSessionID != "" {
			if harnessSessionID != "" && decoded.HarnessSessionID != harnessSessionID {
				return ErrSessionMismatch
			}
			if harnessSessionID == "" {
				harnessSessionID = decoded.HarnessSessionID
				if err := s.sessions.Put(prompt.ExternalSessionID, harnessSessionID); err != nil {
					return fmt.Errorf("persist harness session: %w", err)
				}
			}
		}
		for _, event := range decoded.Events {
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			if err := emit(event); err != nil {
				return err
			}
		}
		return nil
	})
	if runErr != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			return runCtx.Err()
		}
		return fmt.Errorf("run %s harness: %w", s.driver.Descriptor().Runtime, runErr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s harness exited with code %d: %s", s.driver.Descriptor().Runtime, result.ExitCode, result.Stderr)
	}
	return emit(domain.RuntimeEvent{Kind: domain.RuntimeEventDone, Status: "completed", At: time.Now().UTC()})
}

func (s *Supervisor) Cancel(_ context.Context, sessionID string) bool {
	s.mu.Lock()
	cancel := s.activeSessions[sessionID]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *Supervisor) begin(sessionID, externalSessionID string, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.activeSessions[sessionID]; exists {
		return false
	}
	if _, exists := s.activeExternal[externalSessionID]; exists {
		return false
	}
	s.activeSessions[sessionID] = cancel
	s.activeExternal[externalSessionID] = struct{}{}
	return true
}

func (s *Supervisor) finish(sessionID, externalSessionID string) {
	s.mu.Lock()
	delete(s.activeSessions, sessionID)
	delete(s.activeExternal, externalSessionID)
	s.mu.Unlock()
}
