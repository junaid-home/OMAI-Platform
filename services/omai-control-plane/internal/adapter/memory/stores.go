package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
)

type Sessions struct {
	mu         sync.RWMutex
	byID       map[string]domain.Session
	byExternal map[string]string
}

func NewSessions() *Sessions {
	return &Sessions{byID: make(map[string]domain.Session), byExternal: make(map[string]string)}
}

func (s *Sessions) Resolve(_ context.Context, principal domain.Principal, runtimeID, externalID, projectID, workspaceID, root, title string) (domain.Session, bool, error) {
	if principal.TenantID == "" || runtimeID == "" || externalID == "" || projectID == "" || workspaceID == "" || root == "" {
		return domain.Session{}, false, fmt.Errorf("%w: session identity is incomplete", domain.ErrInvalid)
	}
	key := principal.TenantID + "\x00" + runtimeID + "\x00" + externalID
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.byExternal[key]; id != "" {
		session := s.byID[id]
		if session.ProjectID != projectID || session.WorkspaceID != workspaceID || session.Root != root {
			return domain.Session{}, false, fmt.Errorf("%w: existing session belongs to another workspace", domain.ErrConflict)
		}
		session.UpdatedAt = time.Now().UTC()
		if title != "" {
			session.Title = title
		}
		s.byID[id] = session
		return session, false, nil
	}
	now := time.Now().UTC()
	id, err := randomID("ses_")
	if err != nil {
		return domain.Session{}, false, err
	}
	session := domain.Session{
		ID: id, ExternalSessionID: externalID, ProjectID: projectID, WorkspaceID: workspaceID,
		RuntimeID: runtimeID, TenantID: principal.TenantID, ActorID: principal.ActorID,
		Root: root, Title: title, CreatedAt: now, UpdatedAt: now,
	}
	s.byID[id] = session
	s.byExternal[key] = id
	return session, true, nil
}

func (s *Sessions) List(_ context.Context, principal domain.Principal, projectID string, includeArchived bool) ([]domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Session, 0)
	for _, session := range s.byID {
		if session.TenantID != principal.TenantID || session.ProjectID != projectID || (!includeArchived && session.Archived) {
			continue
		}
		result = append(result, session)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result, nil
}

func (s *Sessions) Update(_ context.Context, principal domain.Principal, id string, patch domain.SessionPatch) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.byID[id]
	if !ok || session.TenantID != principal.TenantID {
		return domain.Session{}, domain.ErrNotFound
	}
	if patch.Title != nil {
		session.Title = *patch.Title
	}
	if patch.Archived != nil {
		session.Archived = *patch.Archived
	}
	if patch.ProviderID != nil {
		session.ProviderID = *patch.ProviderID
	}
	if patch.ModelID != nil {
		session.ModelID = *patch.ModelID
	}
	session.UpdatedAt = time.Now().UTC()
	s.byID[id] = session
	return session, nil
}

func (s *Sessions) Delete(_ context.Context, principal domain.Principal, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.byID[id]
	if !ok || session.TenantID != principal.TenantID {
		return domain.ErrNotFound
	}
	delete(s.byID, id)
	delete(s.byExternal, principal.TenantID+"\x00"+session.RuntimeID+"\x00"+session.ExternalSessionID)
	return nil
}

func (s *Sessions) Get(_ context.Context, principal domain.Principal, id string) (domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.byID[id]
	if !ok || session.TenantID != principal.TenantID {
		return domain.Session{}, domain.ErrNotFound
	}
	return session, nil
}

func (s *Sessions) Find(_ context.Context, principal domain.Principal, runtimeID, externalID string) (domain.Session, error) {
	key := principal.TenantID + "\x00" + runtimeID + "\x00" + externalID
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.byExternal[key]
	if id == "" {
		return domain.Session{}, domain.ErrNotFound
	}
	return s.byID[id], nil
}

func (s *Sessions) Touch(_ context.Context, principal domain.Principal, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.byID[id]
	if !ok || session.TenantID != principal.TenantID {
		return domain.ErrNotFound
	}
	session.UpdatedAt = time.Now().UTC()
	s.byID[id] = session
	return nil
}

type Conversations struct {
	mu       sync.RWMutex
	messages map[string][]domain.Message
}

func NewConversations() *Conversations {
	return &Conversations{messages: make(map[string][]domain.Message)}
}

func (c *Conversations) Append(_ context.Context, principal domain.Principal, message domain.Message) error {
	if principal.TenantID == "" || message.SessionID == "" || message.ID == "" {
		return fmt.Errorf("%w: message identity is incomplete", domain.ErrInvalid)
	}
	key := principal.TenantID + "\x00" + message.SessionID
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.messages[key] {
		if existing.ID == message.ID {
			return nil
		}
	}
	message.DataJSON = append([]byte(nil), message.DataJSON...)
	c.messages[key] = append(c.messages[key], message)
	return nil
}

func (c *Conversations) AppendText(_ context.Context, principal domain.Principal, sessionID, messageID, role, kind, text string) error {
	if sessionID == "" || messageID == "" || text == "" {
		return nil
	}
	key := principal.TenantID + "\x00" + sessionID
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.messages[key] {
		message := &c.messages[key][index]
		if message.ID == messageID && message.Role == role && message.Kind == kind {
			message.Text += text
			return nil
		}
	}
	c.messages[key] = append(c.messages[key], domain.Message{
		ID: messageID, SessionID: sessionID, Role: role, Kind: kind,
		Text: text, CreatedAt: time.Now().UTC(),
	})
	return nil
}

func (c *Conversations) List(_ context.Context, principal domain.Principal, sessionID string) ([]domain.Message, error) {
	key := principal.TenantID + "\x00" + sessionID
	c.mu.RLock()
	defer c.mu.RUnlock()
	messages := c.messages[key]
	result := make([]domain.Message, len(messages))
	copy(result, messages)
	for index := range result {
		result[index].DataJSON = append([]byte(nil), result[index].DataJSON...)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

func (c *Conversations) DeleteSession(_ context.Context, principal domain.Principal, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.messages, principal.TenantID+"\x00"+sessionID)
	return nil
}

type eventStream struct {
	tenant      string
	next        uint64
	events      []domain.Event
	subscribers map[uint64]chan domain.Event
	nextWatcher uint64
}

type Events struct {
	mu      sync.Mutex
	max     int
	streams map[string]*eventStream
}

func NewEvents(max int) *Events {
	return &Events{max: max, streams: make(map[string]*eventStream)}
}

func (e *Events) Publish(_ context.Context, principal domain.Principal, event domain.Event) (domain.Event, error) {
	if event.SessionID == "" || principal.TenantID == "" {
		return domain.Event{}, fmt.Errorf("%w: event identity is incomplete", domain.ErrInvalid)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	stream := e.streams[event.SessionID]
	if stream == nil {
		stream = &eventStream{tenant: principal.TenantID, subscribers: make(map[uint64]chan domain.Event)}
		e.streams[event.SessionID] = stream
	}
	if stream.tenant != principal.TenantID {
		return domain.Event{}, domain.ErrForbidden
	}
	stream.next++
	event.Sequence = stream.next
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
	stream.events = append(stream.events, event)
	if len(stream.events) > e.max {
		stream.events = append([]domain.Event(nil), stream.events[len(stream.events)-e.max:]...)
	}
	for id, subscriber := range stream.subscribers {
		select {
		case subscriber <- cloneEvent(event):
		default:
			close(subscriber)
			delete(stream.subscribers, id)
		}
	}
	return cloneEvent(event), nil
}

func (e *Events) Subscribe(_ context.Context, principal domain.Principal, sessionID string, since uint64) ([]domain.Event, <-chan domain.Event, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	stream := e.streams[sessionID]
	if stream == nil || stream.tenant != principal.TenantID {
		return nil, nil, nil, domain.ErrNotFound
	}
	if since > stream.next {
		return nil, nil, nil, fmt.Errorf("%w: cursor is ahead of the stream", domain.ErrInvalid)
	}
	if since != 0 && len(stream.events) > 0 && since+1 < stream.events[0].Sequence {
		return nil, nil, nil, domain.ErrReplayTooOld
	}
	replay := make([]domain.Event, 0)
	for _, event := range stream.events {
		if event.Sequence > since {
			replay = append(replay, cloneEvent(event))
		}
	}
	stream.nextWatcher++
	id := stream.nextWatcher
	updates := make(chan domain.Event, 256)
	stream.subscribers[id] = updates
	var once sync.Once
	stop := func() {
		once.Do(func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			current := e.streams[sessionID]
			if current == nil {
				return
			}
			if subscriber, ok := current.subscribers[id]; ok {
				close(subscriber)
				delete(current.subscribers, id)
			}
		})
	}
	return replay, updates, stop, nil
}

func (e *Events) DeleteSession(_ context.Context, principal domain.Principal, sessionID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	stream := e.streams[sessionID]
	if stream == nil {
		return nil
	}
	if stream.tenant != principal.TenantID {
		return domain.ErrNotFound
	}
	if len(stream.subscribers) != 0 {
		return fmt.Errorf("%w: session event stream has active subscribers", domain.ErrConflict)
	}
	delete(e.streams, sessionID)
	return nil
}

type Projects struct {
	mu          sync.RWMutex
	byID        map[string]domain.Project
	byWorkspace map[string]string
}

func NewProjects() *Projects {
	return &Projects{byID: make(map[string]domain.Project), byWorkspace: make(map[string]string)}
}

func (p *Projects) Resolve(_ context.Context, principal domain.Principal, workspace domain.Workspace, name string) (domain.Project, bool, error) {
	if principal.TenantID == "" || workspace.ID == "" || workspace.TenantID != principal.TenantID {
		return domain.Project{}, false, fmt.Errorf("%w: project identity is incomplete", domain.ErrInvalid)
	}
	key := principal.TenantID + "\x00" + workspace.ID
	p.mu.Lock()
	defer p.mu.Unlock()
	if id := p.byWorkspace[key]; id != "" {
		project := p.byID[id]
		if name != "" && name != project.Name {
			project.Name = name
			project.UpdatedAt = time.Now().UTC()
			p.byID[id] = project
		}
		return project, false, nil
	}
	now := time.Now().UTC()
	id := "prj_" + strings.TrimPrefix(workspace.ID, "wsp_")
	project := domain.Project{
		ID: id, WorkspaceID: workspace.ID, TenantID: principal.TenantID,
		Root: workspace.Root, RepoRoot: workspace.RepoRoot, Name: name,
		CreatedAt: now, UpdatedAt: now,
	}
	p.byID[id] = project
	p.byWorkspace[key] = id
	return project, true, nil
}

func (p *Projects) Get(_ context.Context, principal domain.Principal, id string) (domain.Project, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	project, ok := p.byID[id]
	if !ok || project.TenantID != principal.TenantID {
		return domain.Project{}, domain.ErrNotFound
	}
	return project, nil
}

func (p *Projects) List(_ context.Context, principal domain.Principal) ([]domain.Project, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]domain.Project, 0)
	for _, project := range p.byID {
		if project.TenantID == principal.TenantID {
			result = append(result, project)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result, nil
}

func (p *Projects) Update(_ context.Context, principal domain.Principal, id string, patch domain.ProjectPatch) (domain.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	project, ok := p.byID[id]
	if !ok || project.TenantID != principal.TenantID {
		return domain.Project{}, domain.ErrNotFound
	}
	if patch.Name != nil {
		project.Name = *patch.Name
	}
	if patch.IconColor != nil {
		project.IconColor = *patch.IconColor
	}
	if patch.IconOverride != nil {
		project.IconOverride = *patch.IconOverride
	}
	if patch.StartupCommand != nil {
		project.StartupCommand = *patch.StartupCommand
	}
	project.UpdatedAt = time.Now().UTC()
	p.byID[id] = project
	return project, nil
}

type MCP struct {
	mu      sync.RWMutex
	servers map[string]map[string]domain.MCPServer
}

func NewMCP() *MCP {
	return &MCP{servers: make(map[string]map[string]domain.MCPServer)}
}

func (m *MCP) List(_ context.Context, principal domain.Principal, workspaceID string) ([]domain.MCPServer, error) {
	key := principal.TenantID + "\x00" + workspaceID
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]domain.MCPServer, 0, len(m.servers[key]))
	for _, server := range m.servers[key] {
		server.Args = append([]string(nil), server.Args...)
		result = append(result, server)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (m *MCP) Upsert(_ context.Context, principal domain.Principal, server domain.MCPServer) (domain.MCPServer, error) {
	if err := server.Validate(); err != nil {
		return domain.MCPServer{}, err
	}
	key := principal.TenantID + "\x00" + server.WorkspaceID
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.servers[key] == nil {
		m.servers[key] = make(map[string]domain.MCPServer)
	}
	server.Args = append([]string(nil), server.Args...)
	m.servers[key][server.ID] = server
	return server, nil
}

func (m *MCP) Delete(_ context.Context, principal domain.Principal, workspaceID, serverID string) (bool, error) {
	if err := domain.ValidateMCPIdentity(workspaceID, serverID); err != nil {
		return false, err
	}
	key := principal.TenantID + "\x00" + workspaceID
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.servers[key][serverID]; !exists {
		return false, nil
	}
	delete(m.servers[key], serverID)
	if len(m.servers[key]) == 0 {
		delete(m.servers, key)
	}
	return true, nil
}

func cloneEvent(event domain.Event) domain.Event {
	event.PayloadJSON = append([]byte(nil), event.PayloadJSON...)
	return event
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
