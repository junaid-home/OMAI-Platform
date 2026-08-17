package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/omai/backend/internal/domain"
)

var (
	ErrActiveRun       = fmt.Errorf("%w: harness session already has an active turn", domain.ErrConflict)
	ErrInvalidEvent    = fmt.Errorf("%w: invalid harness event", domain.ErrUnavailable)
	ErrSessionMismatch = fmt.Errorf("%w: harness returned a different session identifier", domain.ErrConflict)
)

type Invocation struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
	Stdin   []byte
}

type RunResult struct {
	ExitCode int
	Stderr   string
}

type Runner interface {
	Available(string) error
	Run(context.Context, Invocation, func([]byte) error) (RunResult, error)
}

type ModelLease struct {
	Token   string
	RouteID string
	BaseURL string
	Expiry  time.Time
}

type ModelLeaseIssuer interface {
	Issue(domain.Prompt) (ModelLease, error)
	Revoke(string)
}

type DecodedEvent struct {
	HarnessSessionID string
	Events           []domain.RuntimeEvent
}

type Decoder interface {
	Decode([]byte) (DecodedEvent, error)
}

type Driver interface {
	Descriptor() domain.RuntimeDescriptor
	Command() string
	Invocation(domain.Prompt, string, ModelLease) (Invocation, error)
	NewDecoder() Decoder
}

type SessionStore interface {
	Get(string) (string, bool)
	Put(string, string) error
}
