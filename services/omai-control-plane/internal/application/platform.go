package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type Platform struct {
	projects      port.ProjectRepository
	sessions      port.SessionRepository
	conversations port.ConversationRepository
	events        port.EventRepository
	workspaces    port.WorkspaceRepository
	runtimes      port.RuntimeRegistry
	orchestrator  *Orchestrator
	catalog       *Catalog
	interactions  *Interactions
}

func (p *Platform) UseInteractions(interactions *Interactions) {
	p.interactions = interactions
}

func NewPlatform(
	projects port.ProjectRepository,
	sessions port.SessionRepository,
	conversations port.ConversationRepository,
	events port.EventRepository,
	workspaces port.WorkspaceRepository,
	runtimes port.RuntimeRegistry,
	orchestrator *Orchestrator,
	catalog *Catalog,
) *Platform {
	return &Platform{
		projects: projects, sessions: sessions, conversations: conversations,
		events: events, workspaces: workspaces, runtimes: runtimes,
		orchestrator: orchestrator, catalog: catalog,
	}
}

func (p *Platform) ResolveProject(ctx context.Context, principal domain.Principal, root, name string) (domain.Project, bool, error) {
	workspace, err := p.workspaces.Resolve(ctx, principal, root)
	if err != nil {
		return domain.Project{}, false, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(workspace.Root)
	}
	if err := validLabel(name, 200, "project name"); err != nil {
		return domain.Project{}, false, err
	}
	return p.projects.Resolve(ctx, principal, workspace, name)
}

func (p *Platform) ListProjects(ctx context.Context, principal domain.Principal, pageSize int, pageToken string) ([]domain.Project, string, error) {
	projects, err := p.projects.List(ctx, principal)
	if err != nil {
		return nil, "", err
	}
	start, size, err := page(pageToken, pageSize, len(projects))
	if err != nil {
		return nil, "", err
	}
	end := min(start+size, len(projects))
	next := ""
	if end < len(projects) {
		next = encodePageToken(end)
	}
	return projects[start:end], next, nil
}

func (p *Platform) GetProject(ctx context.Context, principal domain.Principal, id string) (domain.Project, error) {
	if err := validID(id, "project id"); err != nil {
		return domain.Project{}, err
	}
	return p.projects.Get(ctx, principal, id)
}

func (p *Platform) UpdateProject(ctx context.Context, principal domain.Principal, id string, patch domain.ProjectPatch) (domain.Project, error) {
	if err := validID(id, "project id"); err != nil {
		return domain.Project{}, err
	}
	if patch.Name == nil && patch.IconColor == nil && patch.IconOverride == nil && patch.StartupCommand == nil {
		return domain.Project{}, fmt.Errorf("%w: project update is empty", domain.ErrInvalid)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if err := validLabel(name, 200, "project name"); err != nil {
			return domain.Project{}, err
		}
		patch.Name = &name
	}
	if patch.IconColor != nil && !validColor(*patch.IconColor) {
		return domain.Project{}, fmt.Errorf("%w: icon color must be empty, a supported palette key, or a six-digit hex color", domain.ErrInvalid)
	}
	if patch.IconOverride != nil && !validIconOverride(*patch.IconOverride) {
		return domain.Project{}, fmt.Errorf("%w: icon override must be empty or a bounded image data URL", domain.ErrInvalid)
	}
	if patch.StartupCommand != nil {
		command := strings.TrimSpace(*patch.StartupCommand)
		if len(command) > 64*1024 || strings.ContainsRune(command, '\x00') {
			return domain.Project{}, fmt.Errorf("%w: startup command is invalid", domain.ErrInvalid)
		}
		patch.StartupCommand = &command
	}
	return p.projects.Update(ctx, principal, id, patch)
}

func (p *Platform) CreateSession(ctx context.Context, principal domain.Principal, projectID, runtimeID, externalID, title string) (domain.Session, bool, error) {
	project, err := p.GetProject(ctx, principal, projectID)
	if err != nil {
		return domain.Session{}, false, err
	}
	if err := validID(runtimeID, "runtime id"); err != nil {
		return domain.Session{}, false, err
	}
	if _, ok := p.runtimes.Get(runtimeID); !ok {
		return domain.Session{}, false, fmt.Errorf("%w: runtime", domain.ErrNotFound)
	}
	if err := validID(externalID, "external session id"); err != nil {
		return domain.Session{}, false, err
	}
	title = strings.TrimSpace(title)
	if title != "" {
		if err := validLabel(title, 500, "session title"); err != nil {
			return domain.Session{}, false, err
		}
	}
	return p.sessions.Resolve(ctx, principal, runtimeID, externalID, project.ID, project.WorkspaceID, project.Root, title)
}

func (p *Platform) ListSessions(ctx context.Context, principal domain.Principal, projectID string, includeArchived bool, pageSize int, pageToken string) ([]domain.Session, string, error) {
	if _, err := p.GetProject(ctx, principal, projectID); err != nil {
		return nil, "", err
	}
	sessions, err := p.sessions.List(ctx, principal, projectID, includeArchived)
	if err != nil {
		return nil, "", err
	}
	start, size, err := page(pageToken, pageSize, len(sessions))
	if err != nil {
		return nil, "", err
	}
	end := min(start+size, len(sessions))
	next := ""
	if end < len(sessions) {
		next = encodePageToken(end)
	}
	return sessions[start:end], next, nil
}

func (p *Platform) GetSession(ctx context.Context, principal domain.Principal, id string) (domain.Session, error) {
	if err := validID(id, "session id"); err != nil {
		return domain.Session{}, err
	}
	return p.sessions.Get(ctx, principal, id)
}

func (p *Platform) UpdateSession(ctx context.Context, principal domain.Principal, id string, patch domain.SessionPatch) (domain.Session, error) {
	if err := validID(id, "session id"); err != nil {
		return domain.Session{}, err
	}
	if patch.Title == nil && patch.Archived == nil {
		return domain.Session{}, fmt.Errorf("%w: session update is empty", domain.ErrInvalid)
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title != "" {
			if err := validLabel(title, 500, "session title"); err != nil {
				return domain.Session{}, err
			}
		}
		patch.Title = &title
	}
	return p.sessions.Update(ctx, principal, id, patch)
}

func (p *Platform) DeleteSession(ctx context.Context, principal domain.Principal, id string) error {
	if _, err := p.GetSession(ctx, principal, id); err != nil {
		return err
	}
	if p.orchestrator.Active(ctx, principal, id) {
		return fmt.Errorf("%w: active session cannot be deleted", domain.ErrConflict)
	}
	if err := p.events.DeleteSession(ctx, principal, id); err != nil {
		return err
	}
	if err := p.conversations.DeleteSession(ctx, principal, id); err != nil {
		return err
	}
	if p.interactions != nil {
		if err := p.interactions.DeleteSession(ctx, principal, id); err != nil {
			return err
		}
	}
	return p.sessions.Delete(ctx, principal, id)
}

func (p *Platform) SubmitText(ctx context.Context, principal domain.Principal, id, providerID, modelID, text string) (domain.Session, error) {
	session, err := p.GetSession(ctx, principal, id)
	if err != nil {
		return domain.Session{}, err
	}
	if session.Archived {
		return domain.Session{}, fmt.Errorf("%w: archived session is read-only", domain.ErrConflict)
	}
	text = strings.TrimSpace(text)
	if err := validLabel(text, 1<<20, "prompt text"); err != nil {
		return domain.Session{}, err
	}
	model, err := p.catalog.Resolve(session.RuntimeID, providerID, modelID)
	if err != nil {
		return domain.Session{}, err
	}
	return p.orchestrator.Prompt(ctx, principal, domain.Prompt{
		RuntimeID: session.RuntimeID, ExternalSessionID: session.ExternalSessionID,
		ProjectID: session.ProjectID, Root: session.Root, Text: text, Title: session.Title,
		ProviderID: providerID, ModelID: modelID,
		ModelContextTokens: model.Limits.Context, ModelOutputTokens: model.Limits.Output,
	})
}

func (p *Platform) CancelSession(ctx context.Context, principal domain.Principal, id string) (bool, error) {
	session, err := p.GetSession(ctx, principal, id)
	if err != nil {
		return false, err
	}
	return p.orchestrator.Cancel(ctx, principal, session.RuntimeID, session.ExternalSessionID), nil
}

func (p *Platform) ListMessages(ctx context.Context, principal domain.Principal, id string) ([]domain.Message, error) {
	if _, err := p.GetSession(ctx, principal, id); err != nil {
		return nil, err
	}
	return p.conversations.List(ctx, principal, id)
}

func (p *Platform) SubscribeSessionEvents(ctx context.Context, principal domain.Principal, id string, after uint64) ([]domain.Event, <-chan domain.Event, func(), error) {
	if _, err := p.GetSession(ctx, principal, id); err != nil {
		return nil, nil, nil, err
	}
	return p.events.Subscribe(ctx, principal, id, after)
}

func validID(value, name string) error {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: invalid %s", domain.ErrInvalid, name)
	}
	return nil
}

func validLabel(value string, maximum int, name string) error {
	if value == "" || len(value) > maximum || strings.ContainsAny(value, "\x00") {
		return fmt.Errorf("%w: invalid %s", domain.ErrInvalid, name)
	}
	return nil
}

func validColor(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "pink", "mint", "orange", "purple", "cyan", "lime", "yellow", "green", "red", "blue", "gray":
		return true
	}
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, char := range value[1:] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func validIconOverride(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2*1024*1024 {
		return false
	}
	for _, prefix := range []string{
		"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,",
		"data:image/gif;base64,", "data:image/svg+xml;base64,",
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func page(token string, size, total int) (int, int, error) {
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 200 {
		return 0, 0, fmt.Errorf("%w: page size must be between 1 and 200", domain.ErrInvalid)
	}
	start := 0
	if token != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: invalid page token", domain.ErrInvalid)
		}
		start, err = strconv.Atoi(string(decoded))
		if err != nil || start < 0 || start > total {
			return 0, 0, fmt.Errorf("%w: invalid page token", domain.ErrInvalid)
		}
	}
	return start, size, nil
}

func encodePageToken(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
