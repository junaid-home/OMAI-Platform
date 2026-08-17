package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type Registry struct {
	mu       sync.RWMutex
	runtimes map[string]domain.Runtime
}

func NewRegistry() *Registry {
	return &Registry{runtimes: make(map[string]domain.Runtime)}
}

func (r *Registry) Register(runtime domain.Runtime) error {
	descriptor := runtime.Descriptor()
	if descriptor.ID == "" {
		return fmt.Errorf("%w: runtime id is required", domain.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.runtimes[descriptor.ID]; exists {
		return fmt.Errorf("%w: duplicate runtime %s", domain.ErrConflict, descriptor.ID)
	}
	r.runtimes[descriptor.ID] = runtime
	return nil
}

func (r *Registry) Get(id string) (domain.Runtime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime, ok := r.runtimes[id]
	return runtime, ok
}

func (r *Registry) List() []domain.Runtime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]domain.Runtime, 0, len(r.runtimes))
	for _, runtime := range r.runtimes {
		result = append(result, runtime)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Descriptor().ID < result[right].Descriptor().ID
	})
	return result
}

type Demo struct{}

func NewDemo() *Demo { return &Demo{} }

func (*Demo) Descriptor() domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{
		ID: "go-adk-demo", Runtime: "demo", Label: "OMAI deterministic demo",
		Version: "1", NodeID: "local", Transport: "in-process", Enabled: true,
		Capabilities: []domain.Capability{{Name: "chat", Enabled: true}, {Name: "streaming", Enabled: true}},
	}
}

func (*Demo) Health(context.Context) domain.RuntimeHealth {
	return domain.RuntimeHealth{
		RuntimeID: "go-adk-demo", Available: true, Authenticated: true,
		Version: "1", CheckedAt: time.Now().UTC(),
	}
}

func (*Demo) Run(ctx context.Context, prompt domain.Prompt, emit func(domain.RuntimeEvent) error) error {
	messageID := "demo-" + prompt.SessionID
	for _, chunk := range []string{"OMAI demo runtime received: ", prompt.Text} {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		if err := emit(domain.RuntimeEvent{Kind: domain.RuntimeEventAgentMessage, MessageID: messageID, Text: chunk, At: time.Now().UTC()}); err != nil {
			return err
		}
	}
	return emit(domain.RuntimeEvent{Kind: domain.RuntimeEventDone, MessageID: messageID, At: time.Now().UTC()})
}

func (*Demo) Cancel(context.Context, string) bool { return false }
