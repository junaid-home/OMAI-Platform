package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type Orchestrator struct {
	runtimes      port.RuntimeRegistry
	workspaces    port.WorkspaceRepository
	sessions      port.SessionRepository
	conversations port.ConversationRepository
	events        port.EventRepository
	turnLeases    port.TurnLeaseStore
	turnLeaseTTL  time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	leases  map[string]string
}

func NewOrchestrator(runtimes port.RuntimeRegistry, workspaces port.WorkspaceRepository, sessions port.SessionRepository, conversations port.ConversationRepository, events port.EventRepository) *Orchestrator {
	return &Orchestrator{
		runtimes: runtimes, workspaces: workspaces, sessions: sessions, conversations: conversations, events: events,
		turnLeaseTTL: 2 * time.Minute, cancels: make(map[string]context.CancelFunc), leases: make(map[string]string),
	}
}

func (o *Orchestrator) UseTurnLeases(store port.TurnLeaseStore) {
	o.turnLeases = store
}

func (o *Orchestrator) Prompt(ctx context.Context, principal domain.Principal, request domain.Prompt) (domain.Session, error) {
	runtimeAdapter, ok := o.runtimes.Get(request.RuntimeID)
	if request.RuntimeID == "" {
		return domain.Session{}, fmt.Errorf("%w: runtime id is required", domain.ErrInvalid)
	}
	if !ok {
		return domain.Session{}, fmt.Errorf("%w: runtime", domain.ErrNotFound)
	}
	if hasRuntimeCapability(runtimeAdapter.Descriptor(), "model-gateway") && (request.ProviderID == "" || request.ModelID == "") {
		return domain.Session{}, fmt.Errorf("%w: coding harness turns require an explicit provider_id and model_id", domain.ErrInvalid)
	}
	workspace, err := o.workspaces.Resolve(ctx, principal, request.Root)
	if err != nil {
		return domain.Session{}, err
	}
	projectID := request.ProjectID
	if projectID == "" {
		projectID = workspace.ID
	}
	session, _, err := o.sessions.Resolve(ctx, principal, runtimeAdapter.Descriptor().ID, request.ExternalSessionID, projectID, workspace.ID, workspace.Root, request.Title)
	if err != nil {
		return domain.Session{}, err
	}
	request.SessionID = session.ID
	request.ProjectID = session.ProjectID
	request.WorkspaceID = workspace.ID
	request.Root = workspace.Root
	request.Principal = principal
	if request.ProviderID != "" && request.ModelID != "" {
		session, err = o.sessions.Update(ctx, principal, session.ID, domain.SessionPatch{ProviderID: &request.ProviderID, ModelID: &request.ModelID})
		if err != nil {
			return domain.Session{}, err
		}
	}
	turnID, err := newTurnID()
	if err != nil {
		return domain.Session{}, err
	}
	turnCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if o.turnLeases != nil {
		acquired, err := o.turnLeases.Acquire(ctx, principal, session.ID, turnID, o.turnLeaseTTL)
		if err != nil {
			cancel()
			return domain.Session{}, err
		}
		if !acquired {
			cancel()
			return domain.Session{}, fmt.Errorf("%w: a turn is already running", domain.ErrConflict)
		}
	}
	o.mu.Lock()
	if old := o.cancels[session.ID]; old != nil {
		o.mu.Unlock()
		cancel()
		o.releaseTurnLease(principal, session.ID, turnID)
		return domain.Session{}, fmt.Errorf("%w: a turn is already running", domain.ErrConflict)
	}
	o.cancels[session.ID] = cancel
	o.leases[session.ID] = turnID
	o.mu.Unlock()

	if err := o.conversations.Append(ctx, principal, domain.Message{ID: newMessageID(), SessionID: session.ID, Role: "user", Kind: "text", Text: request.Text, CreatedAt: time.Now().UTC()}); err != nil {
		o.finish(principal, session.ID, turnID)
		return domain.Session{}, err
	}
	if err := o.publish(ctx, principal, session, "session.status", map[string]any{"status": "running"}); err != nil {
		o.finish(principal, session.ID, turnID)
		return domain.Session{}, err
	}
	go o.run(turnCtx, principal, session, request, runtimeAdapter, turnID)
	return session, nil
}

func hasRuntimeCapability(descriptor domain.RuntimeDescriptor, name string) bool {
	for _, capability := range descriptor.Capabilities {
		if capability.Name == name && capability.Enabled {
			return true
		}
	}
	return false
}

func (o *Orchestrator) Active(ctx context.Context, principal domain.Principal, sessionID string) bool {
	o.mu.Lock()
	active := o.cancels[sessionID] != nil
	o.mu.Unlock()
	if active || o.turnLeases == nil {
		return active
	}
	active, err := o.turnLeases.Active(ctx, principal, sessionID)
	return err != nil || active
}

func (o *Orchestrator) Cancel(ctx context.Context, principal domain.Principal, runtimeID, externalID string) bool {
	session, err := o.sessions.Find(ctx, principal, runtimeID, externalID)
	if err != nil {
		return false
	}
	o.mu.Lock()
	cancel := o.cancels[session.ID]
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	leased := false
	if o.turnLeases != nil {
		leased, _ = o.turnLeases.Active(ctx, principal, session.ID)
	}
	if runtimeAdapter, ok := o.runtimes.Get(runtimeID); ok {
		return runtimeAdapter.Cancel(ctx, session.ID) || cancel != nil || leased
	}
	return cancel != nil || leased
}

func (o *Orchestrator) run(parent context.Context, principal domain.Principal, session domain.Session, prompt domain.Prompt, runtimeAdapter domain.Runtime, turnID string) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer o.finish(principal, session.ID, turnID)
	if o.turnLeases != nil {
		go o.renewTurnLease(ctx, cancel, principal, session.ID, turnID)
	}
	err := runtimeAdapter.Run(ctx, prompt, func(event domain.RuntimeEvent) error {
		return o.handleRuntimeEvent(ctx, principal, session, event)
	})
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err != nil && !errors.Is(err, context.Canceled) {
		_ = o.publish(cleanup, principal, session, "session.error", map[string]any{"message": err.Error()})
	}
	_ = o.publish(cleanup, principal, session, "session.status", map[string]any{"status": "idle"})
	_ = o.sessions.Touch(cleanup, principal, session.ID)
}

func (o *Orchestrator) handleRuntimeEvent(ctx context.Context, principal domain.Principal, session domain.Session, event domain.RuntimeEvent) error {
	var update map[string]any
	switch event.Kind {
	case domain.RuntimeEventAgentMessage:
		update = map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": event.MessageID, "content": map[string]any{"type": "text", "text": event.Text}}
		_ = o.conversations.AppendText(ctx, principal, session.ID, event.MessageID, "assistant", "text", event.Text)
	case domain.RuntimeEventAgentThought:
		update = map[string]any{"sessionUpdate": "agent_thought_chunk", "messageId": event.MessageID, "content": map[string]any{"type": "text", "text": event.Text}}
		_ = o.conversations.AppendText(ctx, principal, session.ID, event.MessageID, "assistant", "thought", event.Text)
	case domain.RuntimeEventToolCall:
		update = map[string]any{"sessionUpdate": "tool_call", "toolCallId": event.ToolCallID, "title": event.ToolName, "kind": "other", "status": "pending", "rawInput": json.RawMessage(event.ArgumentsJSON)}
	case domain.RuntimeEventToolUpdate:
		update = map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": event.ToolCallID, "status": event.Status, "rawOutput": json.RawMessage(event.OutputJSON)}
	case domain.RuntimeEventStatus:
		return o.publish(ctx, principal, session, "session.status", map[string]any{"status": event.Status})
	case domain.RuntimeEventError:
		return o.publish(ctx, principal, session, "session.error", map[string]any{"message": event.Text})
	default:
		return nil
	}
	return o.publish(ctx, principal, session, "acp.session/update", map[string]any{"update": update})
}

func (o *Orchestrator) publish(ctx context.Context, principal domain.Principal, session domain.Session, eventType string, payload any) error {
	body, err := json.Marshal(map[string]any{"payload": payload})
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	_, err = o.events.Publish(ctx, principal, domain.Event{At: time.Now().UTC(), Type: eventType, WorkspaceID: session.WorkspaceID, SessionID: session.ID, RuntimeID: session.RuntimeID, ExternalSessionID: session.ExternalSessionID, PayloadJSON: body})
	return err
}

func (o *Orchestrator) renewTurnLease(ctx context.Context, cancel context.CancelFunc, principal domain.Principal, sessionID, turnID string) {
	ticker := time.NewTicker(o.turnLeaseTTL / 4)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			renewed, err := o.turnLeases.Renew(renewCtx, principal, sessionID, turnID, o.turnLeaseTTL)
			done()
			if err != nil || !renewed {
				cancel()
				return
			}
		}
	}
}

func (o *Orchestrator) finish(principal domain.Principal, sessionID, turnID string) {
	o.mu.Lock()
	var cancel context.CancelFunc
	if o.leases[sessionID] == turnID {
		cancel = o.cancels[sessionID]
		delete(o.cancels, sessionID)
		delete(o.leases, sessionID)
	}
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	o.releaseTurnLease(principal, sessionID, turnID)
}

func (o *Orchestrator) releaseTurnLease(principal domain.Principal, sessionID, turnID string) {
	if o.turnLeases == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = o.turnLeases.Release(ctx, principal, sessionID, turnID)
}

func newMessageID() string { return fmt.Sprintf("msg_%d", time.Now().UnixNano()) }

func newTurnID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate turn id: %w", err)
	}
	return "turn_" + hex.EncodeToString(value[:]), nil
}
