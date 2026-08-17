package connectapi

import (
	"context"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/internal/domain"
	"google.golang.org/protobuf/types/known/structpb"
)

type PortalService struct{ Core *Services }

func (s *PortalService) Navigate(_ context.Context, request *connect.Request[uabv1.NavigateRequest]) (*connect.Response[uabv1.NavigateResponse], error) {
	view, ok := portalView(request.Msg.GetView())
	if !ok {
		return nil, connectError(domain.ErrInvalid)
	}
	command, err := portalCommand("navigate", map[string]any{"view": view})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.NavigateResponse{Command: command}), nil
}

func (s *PortalService) OpenWorkspace(ctx context.Context, request *connect.Request[uabv1.OpenWorkspaceRequest]) (*connect.Response[uabv1.OpenWorkspaceResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	workspace, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connectError(err)
	}
	command, err := portalCommand("open_workspace", map[string]any{"workspace_id": workspace.ID})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.OpenWorkspaceResponse{Command: command}), nil
}

func (s *PortalService) OpenProjectDialog(context.Context, *connect.Request[uabv1.OpenProjectDialogRequest]) (*connect.Response[uabv1.OpenProjectDialogResponse], error) {
	command, err := portalCommand("open_project_dialog", map[string]any{})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.OpenProjectDialogResponse{Command: command}), nil
}

func (s *PortalService) OpenFile(ctx context.Context, request *connect.Request[uabv1.OpenFileRequest]) (*connect.Response[uabv1.OpenFileResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(request.Msg.GetPath())))
	if !safePortalPath(path) || request.Msg.GetLine() > 10_000_000 || request.Msg.GetColumn() > 1_000_000 {
		return nil, connectError(domain.ErrInvalid)
	}
	command, err := portalCommand("open_file", map[string]any{"workspace_id": request.Msg.GetWorkspaceId(), "path": path, "line": request.Msg.GetLine(), "column": request.Msg.GetColumn()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.OpenFileResponse{Command: command}), nil
}

func (s *PortalService) SetPanel(_ context.Context, request *connect.Request[uabv1.SetPanelRequest]) (*connect.Response[uabv1.SetPanelResponse], error) {
	panel, ok := portalPanel(request.Msg.GetPanel())
	if !ok {
		return nil, connectError(domain.ErrInvalid)
	}
	mode, ok := portalPanelMode(request.Msg.GetMode())
	if !ok {
		return nil, connectError(domain.ErrInvalid)
	}
	command, err := portalCommand("set_panel", map[string]any{"panel": panel, "mode": mode})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.SetPanelResponse{Command: command}), nil
}

func (s *PortalService) ShowPreview(ctx context.Context, request *connect.Request[uabv1.ShowPreviewRequest]) (*connect.Response[uabv1.ShowPreviewResponse], error) {
	principal, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.Core.Workspaces.Get(ctx, principal, request.Msg.GetWorkspaceId()); err != nil {
		return nil, connectError(err)
	}
	path := strings.TrimSpace(request.Msg.GetPath())
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n\x00") || strings.Contains(path, "://") || len(path) > 2048 {
		return nil, connectError(domain.ErrInvalid)
	}
	command, err := portalCommand("show_preview", map[string]any{"workspace_id": request.Msg.GetWorkspaceId(), "path": path, "reload": request.Msg.GetReload()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.ShowPreviewResponse{Command: command}), nil
}

func (s *PortalService) SelectRuntime(_ context.Context, request *connect.Request[uabv1.SelectRuntimeRequest]) (*connect.Response[uabv1.SelectRuntimeResponse], error) {
	runtimeAdapter, ok := s.Core.Runtimes.Get(request.Msg.GetRuntimeId())
	if !ok || !runtimeAdapter.Descriptor().Enabled {
		return nil, connectError(domain.ErrNotFound)
	}
	command, err := portalCommand("select_runtime", map[string]any{"runtime_id": request.Msg.GetRuntimeId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.SelectRuntimeResponse{Command: command}), nil
}

func (s *PortalService) OpenCommandPalette(context.Context, *connect.Request[uabv1.OpenCommandPaletteRequest]) (*connect.Response[uabv1.OpenCommandPaletteResponse], error) {
	command, err := portalCommand("open_command_palette", map[string]any{})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&uabv1.OpenCommandPaletteResponse{Command: command}), nil
}

func portalCommand(action string, payload map[string]any) (*uabv1.PortalCommand, error) {
	value, err := structpb.NewStruct(payload)
	if err != nil {
		return nil, connectError(err)
	}
	return &uabv1.PortalCommand{Action: action, Payload: value, TimeoutMs: 5000}, nil
}

func portalView(value string) (string, bool) {
	aliases := map[string]string{
		"home": "home", "start": "home", "startseite": "home",
		"projects": "projects", "projekte": "projects",
		"workspace": "workspace", "arbeitsbereich": "workspace",
		"editor": "editor", "dateien": "editor",
		"agents": "agents", "agenten": "agents", "agent_store": "agents",
		"models": "models", "modelle": "models",
		"mcp": "mcp", "settings": "settings", "einstellungen": "settings",
	}
	result, ok := aliases[strings.ToLower(strings.TrimSpace(value))]
	return result, ok
}

func portalPanel(value string) (string, bool) {
	aliases := map[string]string{
		"files": "files", "filetree": "files", "dateien": "files",
		"chat": "chat", "maestro": "chat",
		"terminal": "terminal", "preview": "preview", "vorschau": "preview",
		"git": "git", "observer": "observer", "neo": "neo",
		"agents": "agents", "agenten": "agents", "projects": "projects", "projekte": "projects",
	}
	result, ok := aliases[strings.ToLower(strings.TrimSpace(value))]
	return result, ok
}

func portalPanelMode(value string) (string, bool) {
	aliases := map[string]string{"show": "show", "open": "show", "öffnen": "show", "anzeigen": "show", "hide": "hide", "close": "hide", "schließen": "hide", "ausblenden": "hide", "toggle": "toggle", "umschalten": "toggle", "focus": "focus", "fokussieren": "focus"}
	result, ok := aliases[strings.ToLower(strings.TrimSpace(value))]
	return result, ok
}

func safePortalPath(value string) bool {
	return value != "" && value != "." && !filepath.IsAbs(value) && value != ".." && !strings.HasPrefix(value, "../") && !strings.ContainsAny(value, "\r\n\x00") && len(value) <= 4096
}
