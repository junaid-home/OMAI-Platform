package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type processState struct {
	info        domain.ProcessInfo
	tenantID    string
	input       io.WriteCloser
	terminal    *os.File
	cancel      context.CancelFunc
	chunks      []domain.ProcessChunk
	bufferBytes int
	subscribers map[uint64]chan domain.ProcessChunk
	nextWatcher uint64
	removed     bool
	pid         int
}

type Manager struct {
	mu           sync.Mutex
	workspaces   port.WorkspaceRepository
	maxBuffer    int
	maxProcesses int
	processes    map[string]*processState
	closed       bool
}

func New(workspaces port.WorkspaceRepository, maxBuffer, maxProcesses int) *Manager {
	return &Manager{workspaces: workspaces, maxBuffer: maxBuffer, maxProcesses: maxProcesses, processes: make(map[string]*processState)}
}

func (m *Manager) ListShells(_ context.Context, principal domain.Principal) ([]domain.Shell, error) {
	if principal.TenantID == "" {
		return nil, domain.ErrForbidden
	}
	paths := []string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh", "/bin/zsh", "/usr/bin/fish"}
	if data, err := os.ReadFile("/etc/shells"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				paths = append(paths, line)
			}
		}
	}
	seen := make(map[string]struct{}, len(paths))
	result := make([]domain.Shell, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if !filepath.IsAbs(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		info, err := os.Stat(path)
		acceptable := err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
		if !acceptable && err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect terminal shell: %w", err)
		}
		result = append(result, domain.Shell{Path: path, Name: filepath.Base(path), Acceptable: acceptable})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result, nil
}

func (m *Manager) Start(ctx context.Context, principal domain.Principal, spec domain.ProcessSpec) (domain.ProcessInfo, error) {
	if spec.Kind != "terminal" && spec.Kind != "lsp" && spec.Kind != "preview" {
		return domain.ProcessInfo{}, domain.ErrInvalid
	}
	if spec.WorkspaceID == "" || !validCommand(spec.Command) || len(spec.Args) > 256 || len(spec.Env) > 64 {
		return domain.ProcessInfo{}, domain.ErrInvalid
	}
	for _, argument := range spec.Args {
		if len(argument) > 64<<10 || strings.ContainsRune(argument, '\x00') {
			return domain.ProcessInfo{}, domain.ErrInvalid
		}
	}
	workspace, err := m.workspaces.Get(ctx, principal, spec.WorkspaceID)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	cwd, err := processCWD(workspace.Root, spec.CWD)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	id, err := processID()
	if err != nil {
		return domain.ProcessInfo{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return domain.ProcessInfo{}, domain.ErrUnavailable
	}
	running := 0
	for _, candidate := range m.processes {
		if candidate.tenantID == principal.TenantID && candidate.info.Status == "running" && !candidate.removed {
			running++
		}
	}
	if running >= m.maxProcesses {
		m.mu.Unlock()
		return domain.ProcessInfo{}, fmt.Errorf("%w: process limit reached", domain.ErrUnavailable)
	}
	m.mu.Unlock()

	processCtx, cancel := context.WithCancel(context.Background())
	// #nosec G204 -- this is the executor boundary: argv is length/NUL checked above,
	// the working directory is contained, and production runs this adapter inside a sandbox.
	command := exec.CommandContext(processCtx, spec.Command, spec.Args...)
	command.Dir = cwd
	command.Env, err = processEnvironment(spec.Env, workspace.Root)
	if err != nil {
		cancel()
		return domain.ProcessInfo{}, err
	}
	if spec.Kind == "lsp" || spec.Kind == "preview" {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	state := &processState{
		info:     domain.ProcessInfo{ID: id, WorkspaceID: workspace.ID, Kind: spec.Kind, ServerID: spec.ServerID, Title: processTitle(spec), Command: spec.Command, CWD: cwd, Status: "running", StartedAt: time.Now().UTC()},
		tenantID: principal.TenantID, cancel: cancel, subscribers: make(map[uint64]chan domain.ProcessChunk),
	}

	var reader io.Reader
	if spec.Kind == "terminal" {
		terminal, startErr := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 80})
		if startErr != nil {
			cancel()
			return domain.ProcessInfo{}, fmt.Errorf("start terminal: %w", startErr)
		}
		state.input, state.terminal, reader = terminal, terminal, terminal
	} else {
		stdin, pipeErr := command.StdinPipe()
		if pipeErr != nil {
			cancel()
			return domain.ProcessInfo{}, pipeErr
		}
		stdout, pipeErr := command.StdoutPipe()
		if pipeErr != nil {
			_ = stdin.Close()
			cancel()
			return domain.ProcessInfo{}, pipeErr
		}
		if spec.Kind == "preview" {
			// One pipe preserves stdout/stderr order well enough for diagnostics and
			// avoids the classic two-pipe deadlock under a noisy dev server.
			command.Stderr = command.Stdout
		} else {
			command.Stderr = io.Discard
		}
		if startErr := command.Start(); startErr != nil {
			_ = stdin.Close()
			cancel()
			return domain.ProcessInfo{}, fmt.Errorf("start %s process: %w", spec.Kind, startErr)
		}
		state.input, reader = stdin, stdout
	}
	if command.Process != nil {
		state.pid = command.Process.Pid
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		_ = state.input.Close()
		go func() { _ = command.Wait() }()
		return domain.ProcessInfo{}, domain.ErrUnavailable
	}
	running = 0
	for _, candidate := range m.processes {
		if candidate.tenantID == principal.TenantID && candidate.info.Status == "running" && !candidate.removed {
			running++
		}
	}
	if running >= m.maxProcesses {
		m.mu.Unlock()
		cancel()
		_ = state.input.Close()
		go func() { _ = command.Wait() }()
		return domain.ProcessInfo{}, fmt.Errorf("%w: process limit reached", domain.ErrUnavailable)
	}
	m.processes[id] = state
	info := cloneProcessInfo(state.info)
	m.mu.Unlock()
	go m.monitor(state, command, reader)
	return info, nil
}

func (m *Manager) AllocatePreviewPort(ctx context.Context, principal domain.Principal, workspaceID string) (uint32, error) {
	if _, err := m.workspaces.Get(ctx, principal, workspaceID); err != nil {
		return 0, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("allocate preview port: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port < 1 || address.Port > 65535 {
		return 0, fmt.Errorf("%w: executor returned an invalid preview port", domain.ErrUnavailable)
	}
	return uint32(address.Port), nil
}

func (m *Manager) WaitPreviewPort(ctx context.Context, principal domain.Principal, processID string, preferred []uint32) (uint32, error) {
	if len(preferred) == 0 || len(preferred) > 64 {
		return 0, domain.ErrInvalid
	}
	for _, portNumber := range preferred {
		if portNumber == 0 || portNumber > 65535 {
			return 0, domain.ErrInvalid
		}
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		state, err := m.authorized(principal, processID)
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
		pid, running, exitCode := state.pid, state.info.Status == "running", state.info.ExitCode
		m.mu.Unlock()
		if !running {
			return 0, fmt.Errorf("%w: preview process exited with code %d", domain.ErrUnavailable, exitCode)
		}
		ports, err := ownedListenerPorts(pid)
		if err == nil {
			if selected := selectPreviewPort(preferred, ports); selected != 0 {
				return selected, nil
			}
		}
		if selected := fallbackPreviewPort(preferred); selected != 0 {
			return selected, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("%w: preview port readiness timed out: %v", domain.ErrUnavailable, ctx.Err())
		case <-ticker.C:
		}
	}
}

func selectPreviewPort(preferred, owned []uint32) uint32 {
	set := make(map[uint32]struct{}, len(owned))
	for _, value := range owned {
		set[value] = struct{}{}
	}
	for _, value := range preferred {
		if _, ok := set[value]; ok {
			return value
		}
	}
	if len(owned) == 0 {
		return 0
	}
	sort.Slice(owned, func(left, right int) bool { return owned[left] < owned[right] })
	return owned[0]
}

func (m *Manager) Get(_ context.Context, principal domain.Principal, id string) (domain.ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.authorized(principal, id)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	return cloneProcessInfo(state.info), nil
}

func (m *Manager) List(_ context.Context, principal domain.Principal, workspaceID, kind string) ([]domain.ProcessInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.ProcessInfo, 0)
	for _, state := range m.processes {
		if state.removed || state.tenantID != principal.TenantID || state.info.WorkspaceID != workspaceID || (kind != "" && state.info.Kind != kind) {
			continue
		}
		result = append(result, cloneProcessInfo(state.info))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].StartedAt.Before(result[right].StartedAt) })
	return result, nil
}

func (m *Manager) Write(_ context.Context, principal domain.Principal, id string, data []byte) error {
	if len(data) == 0 || len(data) > 64<<10 {
		return domain.ErrInvalid
	}
	m.mu.Lock()
	state, err := m.authorized(principal, id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	input := state.input
	running := state.info.Status == "running"
	m.mu.Unlock()
	if !running {
		return domain.ErrConflict
	}
	_, err = input.Write(data)
	if err != nil {
		return fmt.Errorf("write process input: %w", err)
	}
	return nil
}

func (m *Manager) Resize(_ context.Context, principal domain.Principal, id string, cols, rows uint32) error {
	if cols < 2 || rows < 2 || cols > 1000 || rows > 1000 {
		return domain.ErrInvalid
	}
	m.mu.Lock()
	state, err := m.authorized(principal, id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	terminal := state.terminal
	m.mu.Unlock()
	if terminal == nil {
		return domain.ErrInvalid
	}
	if err := pty.Setsize(terminal, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("resize terminal: %w", err)
	}
	return nil
}

func (m *Manager) Stop(_ context.Context, principal domain.Principal, id string) error {
	m.mu.Lock()
	state, err := m.authorized(principal, id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if state.info.Status != "running" {
		m.mu.Unlock()
		return nil
	}
	cancel := state.cancel
	m.mu.Unlock()
	cancel()
	return nil
}

func (m *Manager) Remove(_ context.Context, principal domain.Principal, id string) error {
	m.mu.Lock()
	state, err := m.authorized(principal, id)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	state.removed = true
	cancel := state.cancel
	running := state.info.Status == "running"
	if !running {
		delete(m.processes, id)
	}
	m.mu.Unlock()
	if running {
		cancel()
	}
	return nil
}

func (m *Manager) Watch(_ context.Context, principal domain.Principal, id string, cursor uint64) ([]domain.ProcessChunk, <-chan domain.ProcessChunk, func(), error) {
	m.mu.Lock()
	state, err := m.authorized(principal, id)
	if err != nil {
		m.mu.Unlock()
		return nil, nil, nil, err
	}
	if cursor > state.info.Cursor {
		m.mu.Unlock()
		return nil, nil, nil, domain.ErrInvalid
	}
	if cursor != 0 && len(state.chunks) > 0 && cursor+1 < state.chunks[0].Cursor {
		m.mu.Unlock()
		return nil, nil, nil, domain.ErrReplayTooOld
	}
	replay := make([]domain.ProcessChunk, 0)
	for _, chunk := range state.chunks {
		if chunk.Cursor > cursor {
			replay = append(replay, cloneProcessChunk(chunk))
		}
	}
	state.nextWatcher++
	watcherID := state.nextWatcher
	updates := make(chan domain.ProcessChunk, 256)
	if state.info.Status == "running" {
		state.subscribers[watcherID] = updates
	} else {
		close(updates)
	}
	m.mu.Unlock()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if current := m.processes[id]; current != nil {
				if subscriber, ok := current.subscribers[watcherID]; ok {
					close(subscriber)
					delete(current.subscribers, watcherID)
				}
			}
		})
	}
	return replay, updates, stop, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	states := make([]*processState, 0, len(m.processes))
	for _, state := range m.processes {
		states = append(states, state)
	}
	m.mu.Unlock()
	for _, state := range states {
		state.cancel()
	}
	return nil
}

func (m *Manager) monitor(state *processState, command *exec.Cmd, reader io.Reader) {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 32<<10)
		for {
			count, err := reader.Read(buffer)
			if count > 0 {
				m.append(state, buffer[:count], false, 0)
			}
			if err != nil {
				return
			}
		}
	}()
	waitErr := command.Wait()
	select {
	case <-readDone:
	case <-time.After(250 * time.Millisecond):
		_ = state.input.Close()
		<-readDone
	}
	_ = state.input.Close()
	exitCode := int32(0)
	if waitErr != nil {
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			code := exitError.ExitCode()
			if code < -1<<31 || code > 1<<31-1 {
				exitCode = -1
			} else {
				exitCode = int32(code)
			}
		} else if errors.Is(waitErr, context.Canceled) {
			exitCode = int32(128 + syscall.SIGKILL)
		} else {
			exitCode = -1
		}
	}
	m.append(state, nil, true, exitCode)
}

func (m *Manager) append(state *processState, data []byte, exited bool, exitCode int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.info.Cursor++
	chunk := domain.ProcessChunk{ProcessID: state.info.ID, Cursor: state.info.Cursor, Data: append([]byte(nil), data...), Exited: exited, ExitCode: exitCode}
	state.chunks = append(state.chunks, chunk)
	state.bufferBytes += len(data)
	for state.bufferBytes > m.maxBuffer && len(state.chunks) > 1 {
		state.bufferBytes -= len(state.chunks[0].Data)
		state.chunks = state.chunks[1:]
	}
	for id, subscriber := range state.subscribers {
		select {
		case subscriber <- cloneProcessChunk(chunk):
		default:
			close(subscriber)
			delete(state.subscribers, id)
		}
	}
	if !exited {
		return
	}
	state.info.Status = "exited"
	state.info.ExitCode = exitCode
	state.info.EndedAt = time.Now().UTC()
	for id, subscriber := range state.subscribers {
		close(subscriber)
		delete(state.subscribers, id)
	}
	if state.removed {
		delete(m.processes, state.info.ID)
	} else {
		m.pruneHistory(state.tenantID)
	}
}

func (m *Manager) pruneHistory(tenantID string) {
	limit := m.maxProcesses * 4
	if limit < 16 {
		limit = 16
	}
	for {
		count := 0
		oldestID := ""
		var oldest time.Time
		for id, state := range m.processes {
			if state.tenantID != tenantID {
				continue
			}
			count++
			if state.info.Status != "running" && (oldestID == "" || state.info.EndedAt.Before(oldest)) {
				oldestID, oldest = id, state.info.EndedAt
			}
		}
		if count <= limit || oldestID == "" {
			return
		}
		delete(m.processes, oldestID)
	}
}

func (m *Manager) authorized(principal domain.Principal, id string) (*processState, error) {
	state := m.processes[id]
	if state == nil || state.removed || state.tenantID != principal.TenantID {
		return nil, domain.ErrNotFound
	}
	return state, nil
}

func processCWD(root, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return root, nil
	}
	if filepath.IsAbs(requested) || strings.ContainsRune(requested, '\x00') {
		return "", domain.ErrInvalid
	}
	candidate := filepath.Join(root, filepath.Clean(requested))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", domain.ErrInvalid
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", domain.ErrForbidden
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", domain.ErrInvalid
	}
	return resolved, nil
}

func processEnvironment(values map[string]string, workspaceRoot string) ([]string, error) {
	base := map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.TempDir(), "LANG": os.Getenv("LANG"), "LC_ALL": os.Getenv("LC_ALL"), "TERM": "xterm-256color", "OMAI_WORKSPACE_ROOT": workspaceRoot}
	for name, value := range values {
		if !validEnvName(name) || len(value) > 64<<10 || strings.ContainsRune(value, '\x00') || strings.HasPrefix(name, "OMAI_") {
			return nil, domain.ErrInvalid
		}
		base[name] = value
	}
	result := make([]string, 0, len(base))
	for name, value := range base {
		if value != "" {
			result = append(result, name+"="+value)
		}
	}
	return result, nil
}

func validEnvName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character == '_' || character >= 'A' && character <= 'Z' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}
func validCommand(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsAny(value, "\r\n\x00")
}
func processTitle(spec domain.ProcessSpec) string {
	if value := strings.TrimSpace(spec.Title); value != "" && len(value) <= 256 {
		return value
	}
	return filepath.Base(spec.Command)
}
func processID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "pro_" + hex.EncodeToString(value[:]), nil
}
func cloneProcessInfo(value domain.ProcessInfo) domain.ProcessInfo { return value }
func cloneProcessChunk(value domain.ProcessChunk) domain.ProcessChunk {
	value.Data = append([]byte(nil), value.Data...)
	return value
}
