package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type PreviewPreparationPolicy string

const (
	PreviewPreparationNever  PreviewPreparationPolicy = "never"
	PreviewPreparationAuto   PreviewPreparationPolicy = "auto"
	PreviewPreparationAlways PreviewPreparationPolicy = "always"
)

type PreviewConfig struct {
	BindHost           string
	RuntimeHost        string
	Preparation        PreviewPreparationPolicy
	PreparationTimeout time.Duration
	StartupTimeout     time.Duration
	IdleTimeout        time.Duration
}

type previewState struct {
	instance   domain.PreviewInstance
	principal  domain.Principal
	runtimeURL string
	lastAccess time.Time
}

// PreviewManager is the single lifecycle owner for workspace dev servers. It
// composes deterministic analysis, isolated argv execution, readiness, an
// unguessable publisher route, crash supervision, and idle cleanup.
type PreviewManager struct {
	workspaces port.WorkspaceRepository
	processes  port.ProcessManager
	commands   port.WorkspaceCommandRunner
	detector   port.ProjectDetector
	publisher  port.PreviewPublisher
	events     port.EventRepository
	config     PreviewConfig

	mu        sync.RWMutex
	instances map[string]*previewState
	locks     map[string]*sync.Mutex
}

func NewPreviewManager(workspaces port.WorkspaceRepository, processes port.ProcessManager, commands port.WorkspaceCommandRunner, detector port.ProjectDetector, publisher port.PreviewPublisher, events port.EventRepository, config PreviewConfig) (*PreviewManager, error) {
	if workspaces == nil || processes == nil || commands == nil || detector == nil || publisher == nil {
		return nil, fmt.Errorf("%w: preview dependencies are required", domain.ErrInvalid)
	}
	config.BindHost = strings.TrimSpace(config.BindHost)
	if config.BindHost == "" {
		config.BindHost = "127.0.0.1"
	}
	config.RuntimeHost = strings.TrimSpace(config.RuntimeHost)
	if config.RuntimeHost == "" {
		config.RuntimeHost = "127.0.0.1"
	}
	if err := validatePreviewHost(config.BindHost); err != nil {
		return nil, fmt.Errorf("preview bind host: %w", err)
	}
	if err := validatePreviewHost(config.RuntimeHost); err != nil {
		return nil, fmt.Errorf("preview runtime host: %w", err)
	}
	if config.Preparation == "" {
		config.Preparation = PreviewPreparationNever
	}
	switch config.Preparation {
	case PreviewPreparationNever, PreviewPreparationAuto, PreviewPreparationAlways:
	default:
		return nil, fmt.Errorf("%w: preview preparation must be never, auto, or always", domain.ErrInvalid)
	}
	if config.PreparationTimeout <= 0 {
		config.PreparationTimeout = 10 * time.Minute
	}
	if config.StartupTimeout <= 0 {
		config.StartupTimeout = 45 * time.Second
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = 30 * time.Minute
	}
	return &PreviewManager{
		workspaces: workspaces, processes: processes, commands: commands, detector: detector, publisher: publisher, events: events, config: config,
		instances: make(map[string]*previewState), locks: make(map[string]*sync.Mutex),
	}, nil
}

func (m *PreviewManager) Analyze(ctx context.Context, principal domain.Principal, root string) (domain.RuntimePlan, error) {
	workspace, err := m.workspaces.Resolve(ctx, principal, root)
	if err != nil {
		return domain.RuntimePlan{}, err
	}
	return m.detector.Analyze(ctx, workspace)
}

func (m *PreviewManager) Start(ctx context.Context, principal domain.Principal, root string, force bool) (domain.PreviewInstance, error) {
	workspace, err := m.workspaces.Resolve(ctx, principal, root)
	if err != nil {
		return domain.PreviewInstance{}, err
	}
	return m.startWorkspace(ctx, principal, workspace, force)
}

func (m *PreviewManager) startWorkspace(ctx context.Context, principal domain.Principal, workspace domain.Workspace, force bool) (domain.PreviewInstance, error) {
	key := previewKey(principal.TenantID, workspace.ID)
	unlock := m.lock(key)
	defer unlock()

	plan, err := m.detector.Analyze(ctx, workspace)
	if err != nil {
		return domain.PreviewInstance{}, err
	}
	m.mu.RLock()
	current := clonePreviewState(m.instances[key])
	m.mu.RUnlock()
	if current != nil && current.instance.Status == "ready" && current.instance.PlanFingerprint == plan.Fingerprint && !force {
		m.touch(key)
		return clonePreviewInstance(current.instance), nil
	}
	order, err := previewDependencyOrder(plan)
	if err != nil {
		return domain.PreviewInstance{}, err
	}
	primary, ok := plan.PrimaryService()
	if !ok || !primary.Preview {
		return domain.PreviewInstance{}, fmt.Errorf("%w: primary service is not previewable", domain.ErrInvalid)
	}

	started := make([]domain.PreviewProcessRef, 0, len(order))
	cleanup := func() {
		m.retireProcesses(principal, started)
	}
	var primaryProcess domain.ProcessInfo
	var primaryPort uint32
	for _, service := range order {
		if err := m.prepare(ctx, principal, workspace, service); err != nil {
			cleanup()
			return domain.PreviewInstance{}, fmt.Errorf("prepare preview service %s: %w", service.ID, err)
		}
		portNumber := uint32(0)
		if service.Preview || len(service.ExpectedPorts) > 0 {
			portNumber, err = m.processes.AllocatePreviewPort(ctx, principal, workspace.ID)
			if err != nil {
				cleanup()
				return domain.PreviewInstance{}, fmt.Errorf("allocate preview port for %s: %w", service.ID, err)
			}
		}
		command := materializeCommand(service.Run, m.config.BindHost, portNumber)
		if (service.Framework == "vite" || service.Framework == "astro") && command.Env != nil {
			if provider, ok := m.publisher.(interface{ AdditionalAllowedHost() string }); ok {
				if allowed := provider.AdditionalAllowedHost(); allowed != "" {
					command.Env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"] = allowed
				}
			}
		}
		processInfo, startErr := m.processes.Start(ctx, principal, domain.ProcessSpec{
			WorkspaceID: workspace.ID, Kind: "preview", ServerID: service.ID, Title: "Preview: " + service.Name,
			Command: command.Command, Args: command.Args, CWD: service.WorkingDir, Env: command.Env,
		})
		if startErr != nil {
			cleanup()
			return domain.PreviewInstance{}, fmt.Errorf("start preview service %s: %w", service.ID, startErr)
		}
		ref := domain.PreviewProcessRef{ServiceID: service.ID, ProcessID: processInfo.ID, Port: portNumber, Status: "running"}
		started = append(started, ref)
		if !service.Preview && len(service.ExpectedPorts) == 0 {
			continue
		}
		actualPort, waitErr := m.waitPort(ctx, principal, processInfo, append([]uint32{portNumber}, service.ExpectedPorts...))
		if waitErr != nil {
			cleanup()
			return domain.PreviewInstance{}, fmt.Errorf("preview service %s: %w", service.ID, waitErr)
		}
		started[len(started)-1].Port = actualPort
		if service.ID == primary.ID {
			primaryProcess, primaryPort = processInfo, actualPort
		}
	}
	if primaryProcess.ID == "" || primaryPort == 0 {
		cleanup()
		return domain.PreviewInstance{}, fmt.Errorf("%w: primary preview did not expose a port", domain.ErrUnavailable)
	}
	runtimeURL := "http://" + net.JoinHostPort(m.config.RuntimeHost, strconv.Itoa(int(primaryPort)))
	if err := m.waitHTTP(ctx, runtimeURL); err != nil {
		cleanup()
		return domain.PreviewInstance{}, err
	}
	publicURL, err := m.publisher.Publish(ctx, key, runtimeURL)
	if err != nil {
		cleanup()
		return domain.PreviewInstance{}, err
	}
	now := time.Now().UTC()
	identifier, err := previewID()
	if err != nil {
		_ = m.publisher.Unpublish(context.Background(), key)
		cleanup()
		return domain.PreviewInstance{}, err
	}
	instance := domain.PreviewInstance{
		ID: identifier, WorkspaceID: workspace.ID, ProcessID: primaryProcess.ID, ServiceID: primary.ID,
		Framework: primary.Framework, PlanFingerprint: plan.Fingerprint, Port: primaryPort, Status: "ready", PublicURL: publicURL,
		Processes: append([]domain.PreviewProcessRef(nil), started...), StartedAt: now, UpdatedAt: now,
	}
	m.mu.Lock()
	previous := m.instances[key]
	m.instances[key] = &previewState{instance: instance, principal: principal, runtimeURL: runtimeURL, lastAccess: now}
	m.mu.Unlock()
	if previous != nil {
		m.retireProcesses(principal, previous.instance.Processes)
	}
	m.publish(ctx, principal, workspace.ID, "preview.ready", map[string]any{"preview_id": instance.ID, "framework": instance.Framework, "url": instance.PublicURL})
	// The process lifetime intentionally outlives the initiating RPC while the
	// request values (tenant and process IDs) remain immutable and bounded.
	go m.supervise(context.WithoutCancel(ctx), key, principal, primaryProcess.ID)
	return clonePreviewInstance(instance), nil
}

func (m *PreviewManager) Get(ctx context.Context, principal domain.Principal, workspaceID string) (domain.PreviewInstance, error) {
	if _, err := m.workspaces.Get(ctx, principal, workspaceID); err != nil {
		return domain.PreviewInstance{}, err
	}
	key := previewKey(principal.TenantID, workspaceID)
	m.mu.Lock()
	state := clonePreviewState(m.instances[key])
	if state != nil {
		m.instances[key].lastAccess = time.Now().UTC()
	}
	m.mu.Unlock()
	if state == nil {
		return domain.PreviewInstance{}, domain.ErrNotFound
	}
	return clonePreviewInstance(state.instance), nil
}

func (m *PreviewManager) Stop(ctx context.Context, principal domain.Principal, workspaceID string) error {
	if _, err := m.workspaces.Get(ctx, principal, workspaceID); err != nil {
		return err
	}
	key := previewKey(principal.TenantID, workspaceID)
	unlock := m.lock(key)
	defer unlock()
	m.mu.Lock()
	state := m.instances[key]
	delete(m.instances, key)
	m.mu.Unlock()
	if state == nil {
		return nil
	}
	_ = m.publisher.Unpublish(ctx, key)
	m.retireProcesses(principal, state.instance.Processes)
	m.publish(ctx, principal, workspaceID, "preview.stopped", map[string]any{"preview_id": state.instance.ID})
	return nil
}

func (m *PreviewManager) Watch(ctx context.Context, principal domain.Principal, workspaceID string, cursor uint64) ([]domain.ProcessChunk, <-chan domain.ProcessChunk, func(), error) {
	instance, err := m.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	return m.processes.Watch(ctx, principal, instance.ProcessID, cursor)
}

func (m *PreviewManager) StartReaper(ctx context.Context) {
	go func() {
		interval := m.config.IdleTimeout / 4
		if interval < time.Minute {
			interval = time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				m.closeAll()
				return
			case now := <-ticker.C:
				m.reapIdle(now)
			}
		}
	}()
}

func (m *PreviewManager) prepare(ctx context.Context, principal domain.Principal, workspace domain.Workspace, service domain.RuntimeServicePlan) error {
	if service.Install == nil || m.config.Preparation == PreviewPreparationNever {
		return nil
	}
	needed := m.config.Preparation == PreviewPreparationAlways
	if m.config.Preparation == PreviewPreparationAuto {
		artifact := ""
		switch service.Runtime {
		case "node":
			artifact = "node_modules"
		case "python":
			artifact = ".omai-preview/python"
		case "php":
			artifact = "vendor"
		}
		if artifact == "" {
			needed = true
		} else {
			for _, path := range artifactPathPrefixes(artifact) {
				link, linkErr := m.commands.Run(ctx, principal, domain.CommandSpec{WorkspaceID: workspace.ID, Command: "test", Args: []string{"-L", path}, CWD: service.WorkingDir, MaxOutputBytes: 4096})
				if linkErr == nil && link.ExitCode == 0 {
					return fmt.Errorf("%w: dependency directory must not contain symlinks", domain.ErrForbidden)
				}
			}
			check, err := m.commands.Run(ctx, principal, domain.CommandSpec{WorkspaceID: workspace.ID, Command: "test", Args: []string{"-d", artifact}, CWD: service.WorkingDir, MaxOutputBytes: 4096})
			needed = err != nil || check.ExitCode != 0
		}
	}
	if !needed {
		return nil
	}
	prepareCtx, cancel := context.WithTimeout(ctx, m.config.PreparationTimeout)
	defer cancel()
	command := materializeCommand(*service.Install, m.config.BindHost, 0)
	result, err := m.commands.Run(prepareCtx, principal, domain.CommandSpec{WorkspaceID: workspace.ID, Command: command.Command, Args: command.Args, CWD: service.WorkingDir, Env: command.Env, MaxOutputBytes: 16 << 20})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%w: dependency preparation exited with code %d", domain.ErrUnavailable, result.ExitCode)
	}
	return nil
}

func artifactPathPrefixes(value string) []string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	result := make([]string, 0, len(parts))
	current := ""
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		result = append(result, current)
	}
	return result
}

func (m *PreviewManager) waitPort(ctx context.Context, principal domain.Principal, processInfo domain.ProcessInfo, candidates []uint32) (uint32, error) {
	waitCtx, cancel := context.WithTimeout(ctx, m.config.StartupTimeout)
	defer cancel()
	return m.processes.WaitPreviewPort(waitCtx, principal, processInfo.ID, uniquePorts(candidates))
}

func (m *PreviewManager) waitHTTP(ctx context.Context, runtimeURL string) error {
	waitCtx, cancel := context.WithTimeout(ctx, m.config.StartupTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(waitCtx, http.MethodGet, runtimeURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 100 && response.StatusCode <= 599 {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("%w: preview HTTP readiness timed out", domain.ErrUnavailable)
		case <-ticker.C:
		}
	}
}

func (m *PreviewManager) supervise(parent context.Context, key string, principal domain.Principal, processID string) {
	watchCtx, cancel := context.WithCancel(parent)
	defer cancel()
	replay, updates, stop, err := m.processes.Watch(watchCtx, principal, processID, 0)
	if err != nil {
		return
	}
	defer stop()
	for _, chunk := range replay {
		if chunk.Exited {
			m.markExited(key, processID, chunk.ExitCode)
			return
		}
	}
	for chunk := range updates {
		if chunk.Exited {
			m.markExited(key, processID, chunk.ExitCode)
			return
		}
	}
}

func (m *PreviewManager) markExited(key, processID string, exitCode int32) {
	m.mu.Lock()
	state := m.instances[key]
	if state == nil || state.instance.ProcessID != processID {
		m.mu.Unlock()
		return
	}
	state.instance.Status = "failed"
	state.instance.UpdatedAt = time.Now().UTC()
	state.instance.LastError = fmt.Sprintf("preview process exited with code %d", exitCode)
	workspaceID, principal := state.instance.WorkspaceID, state.principal
	refs := append([]domain.PreviewProcessRef(nil), state.instance.Processes...)
	m.mu.Unlock()
	_ = m.publisher.Unpublish(context.Background(), key)
	m.retireProcesses(principal, refs)
	m.publish(context.Background(), principal, workspaceID, "preview.failed", map[string]any{"exit_code": exitCode})
}

func (m *PreviewManager) reapIdle(now time.Time) {
	type expired struct {
		key       string
		principal domain.Principal
		instance  domain.PreviewInstance
	}
	var values []expired
	m.mu.Lock()
	for key, state := range m.instances {
		if now.Sub(state.lastAccess) < m.config.IdleTimeout {
			continue
		}
		values = append(values, expired{key: key, principal: state.principal, instance: clonePreviewInstance(state.instance)})
		delete(m.instances, key)
	}
	m.mu.Unlock()
	for _, value := range values {
		_ = m.publisher.Unpublish(context.Background(), value.key)
		m.retireProcesses(value.principal, value.instance.Processes)
	}
}

func (m *PreviewManager) closeAll() {
	m.mu.Lock()
	values := make(map[string]*previewState, len(m.instances))
	for key, state := range m.instances {
		values[key] = clonePreviewState(state)
	}
	m.instances = make(map[string]*previewState)
	m.mu.Unlock()
	for key, state := range values {
		_ = m.publisher.Unpublish(context.Background(), key)
		m.retireProcesses(state.principal, state.instance.Processes)
	}
}

func (m *PreviewManager) retireProcesses(principal domain.Principal, processes []domain.PreviewProcessRef) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for index := len(processes) - 1; index >= 0; index-- {
		_ = m.processes.Stop(cleanupCtx, principal, processes[index].ProcessID)
		_ = m.processes.Remove(cleanupCtx, principal, processes[index].ProcessID)
	}
}

func (m *PreviewManager) lock(key string) func() {
	m.mu.Lock()
	lock := m.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[key] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (m *PreviewManager) touch(key string) {
	m.mu.Lock()
	if state := m.instances[key]; state != nil {
		state.lastAccess = time.Now().UTC()
	}
	m.mu.Unlock()
}

func (m *PreviewManager) publish(ctx context.Context, principal domain.Principal, workspaceID, eventType string, payload any) {
	if m.events == nil {
		return
	}
	body := []byte("{}")
	if encoded, err := json.Marshal(map[string]any{"payload": payload}); err == nil {
		body = encoded
	}
	_, _ = m.events.Publish(ctx, principal, domain.Event{At: time.Now().UTC(), Type: eventType, WorkspaceID: workspaceID, PayloadJSON: body})
}

func previewDependencyOrder(plan domain.RuntimePlan) ([]domain.RuntimeServicePlan, error) {
	byID := make(map[string]domain.RuntimeServicePlan, len(plan.Services))
	for _, service := range plan.Services {
		byID[service.ID] = service
	}
	state := map[string]uint8{}
	result := make([]domain.RuntimeServicePlan, 0, len(plan.Services))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("%w: runtime dependency cycle", domain.ErrConflict)
		}
		if state[id] == 2 {
			return nil
		}
		service, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: runtime dependency %s", domain.ErrInvalid, id)
		}
		state[id] = 1
		for _, dependency := range service.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		result = append(result, service)
		return nil
	}
	if err := visit(plan.Primary); err != nil {
		return nil, err
	}
	return result, nil
}

func materializeCommand(command domain.RuntimeCommand, host string, portNumber uint32) domain.RuntimeCommand {
	portText := strconv.FormatUint(uint64(portNumber), 10)
	replace := func(value string) string {
		value = strings.ReplaceAll(value, "{{host}}", host)
		value = strings.ReplaceAll(value, "{host}", host)
		value = strings.ReplaceAll(value, "{{port}}", portText)
		return strings.ReplaceAll(value, "{port}", portText)
	}
	result := domain.RuntimeCommand{Command: replace(command.Command), Env: make(map[string]string, len(command.Env))}
	for _, argument := range command.Args {
		result.Args = append(result.Args, replace(argument))
	}
	for name, value := range command.Env {
		result.Env[name] = replace(value)
	}
	return result
}

func uniquePorts(values []uint32) []uint32 {
	seen := make(map[uint32]struct{}, len(values))
	result := make([]uint32, 0, len(values))
	for _, value := range values {
		if value == 0 || value > 65535 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validatePreviewHost(value string) error {
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return domain.ErrInvalid
	}
	if strings.Contains(value, ":") && net.ParseIP(value) == nil {
		return domain.ErrInvalid
	}
	return nil
}

func previewKey(tenantID, workspaceID string) string { return tenantID + "\x00" + workspaceID }

func previewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "prev_" + hex.EncodeToString(value[:]), nil
}

func clonePreviewState(value *previewState) *previewState {
	if value == nil {
		return nil
	}
	copy := *value
	copy.instance = clonePreviewInstance(value.instance)
	copy.principal.Permissions = append([]string(nil), value.principal.Permissions...)
	return &copy
}

func clonePreviewInstance(value domain.PreviewInstance) domain.PreviewInstance {
	value.Processes = append([]domain.PreviewProcessRef(nil), value.Processes...)
	return value
}
