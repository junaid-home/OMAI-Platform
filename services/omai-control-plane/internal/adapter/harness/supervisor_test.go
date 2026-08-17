package harness

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omai/backend/internal/domain"
)

type testDriver struct {
	mu       sync.Mutex
	sessions []string
}

func (*testDriver) Descriptor() domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{ID: "driver", Runtime: "test", Version: "1", Enabled: true}
}
func (*testDriver) Command() string { return "test-harness" }
func (d *testDriver) Invocation(_ domain.Prompt, session string, _ ModelLease) (Invocation, error) {
	d.mu.Lock()
	d.sessions = append(d.sessions, session)
	d.mu.Unlock()
	return Invocation{Command: "test-harness"}, nil
}
func (*testDriver) NewDecoder() Decoder { return &openCodeDecoder{} }

type testSessions struct {
	mu   sync.Mutex
	data map[string]string
}

func (s *testSessions) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	return value, ok
}
func (s *testSessions) Put(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

type testLeases struct {
	mu      sync.Mutex
	revoked int
}

func (*testLeases) Issue(domain.Prompt) (ModelLease, error) {
	return ModelLease{Token: "lease-token", RouteID: "route", BaseURL: "http://127.0.0.1:1/v1"}, nil
}
func (l *testLeases) Revoke(string) {
	l.mu.Lock()
	l.revoked++
	l.mu.Unlock()
}

type eventRunner struct{}

func (*eventRunner) Available(string) error { return nil }
func (*eventRunner) Run(_ context.Context, _ Invocation, consume func([]byte) error) (RunResult, error) {
	if err := consume([]byte(`{"type":"text","timestamp":1700000000000,"sessionID":"ses_native","part":{"id":"part","type":"text","text":"hello"}}`)); err != nil {
		return RunResult{}, err
	}
	return RunResult{}, nil
}

func TestSupervisorPersistsNativeSessionAndStreamsEvents(t *testing.T) {
	driver := &testDriver{}
	sessions := &testSessions{data: make(map[string]string)}
	leases := &testLeases{}
	supervisor := NewSupervisor(driver, &eventRunner{}, sessions, leases)
	prompt := domain.Prompt{SessionID: "session", ExternalSessionID: "portal", Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"}}
	var events []domain.RuntimeEvent
	if err := supervisor.Run(context.Background(), prompt, func(event domain.RuntimeEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Status != "running" || events[1].Text != "hello" || events[2].Kind != domain.RuntimeEventDone {
		t.Fatalf("unexpected normalized stream: %#v", events)
	}
	if value, ok := sessions.Get("portal"); !ok || value != "ses_native" {
		t.Fatal("native session identifier was not persisted")
	}
	if err := supervisor.Run(context.Background(), prompt, func(domain.RuntimeEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.sessions) != 2 || driver.sessions[0] != "" || driver.sessions[1] != "ses_native" {
		t.Fatalf("session resume was not passed to driver: %#v", driver.sessions)
	}
	if leases.revoked != 2 {
		t.Fatalf("model capabilities were not revoked: %d", leases.revoked)
	}
}

type blockingRunner struct{ started chan struct{} }

func (*blockingRunner) Available(string) error { return nil }
func (r *blockingRunner) Run(ctx context.Context, _ Invocation, _ func([]byte) error) (RunResult, error) {
	close(r.started)
	<-ctx.Done()
	return RunResult{}, ctx.Err()
}

func TestSupervisorRejectsConcurrentExternalSessionAndCancels(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{})}
	supervisor := NewSupervisor(&testDriver{}, runner, &testSessions{data: make(map[string]string)}, &testLeases{})
	prompt := domain.Prompt{SessionID: "session-1", ExternalSessionID: "portal", Principal: domain.Principal{TenantID: "tenant", ActorID: "actor"}}
	done := make(chan error, 1)
	go func() {
		done <- supervisor.Run(context.Background(), prompt, func(domain.RuntimeEvent) error { return nil })
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	second := prompt
	second.SessionID = "session-2"
	if err := supervisor.Run(context.Background(), second, func(domain.RuntimeEvent) error { return nil }); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("concurrent external session was not rejected: %v", err)
	}
	if !supervisor.Cancel(context.Background(), prompt.SessionID) {
		t.Fatal("active run was not cancelled")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop run")
	}
}
