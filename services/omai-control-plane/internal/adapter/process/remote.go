package process

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	executorv1 "github.com/omai/backend/gen/go/omai/executor/v1"
	"github.com/omai/backend/gen/go/omai/executor/v1/executorv1connect"
	"github.com/omai/backend/internal/domain"
	"github.com/omai/backend/internal/port"
)

type RemoteConfig struct {
	Endpoint      string
	ControlRoot   string
	Token         string
	Transport     string
	CACert        string
	ClientCert    string
	ClientKey     string
	TLSServerName string
}

// Remote is the control-plane adapter for the private executor protocol. All
// public authorization remains in the control plane; the executor repeats
// tenant and workspace checks at the sandbox boundary.
type Remote struct {
	workspaces  port.WorkspaceRepository
	client      executorv1connect.WorkspaceExecutorServiceClient
	controlRoot string

	mu        sync.RWMutex
	processes map[string]string
}

func NewRemote(workspaces port.WorkspaceRepository, config RemoteConfig) (*Remote, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("executor endpoint must be an absolute http or https URL")
	}
	if len(strings.TrimSpace(config.Token)) < 32 {
		return nil, errors.New("executor token must contain at least 32 characters")
	}
	if (config.ClientCert == "") != (config.ClientKey == "") {
		return nil, errors.New("executor client certificate and key must be set together")
	}
	controlRoot := strings.TrimSpace(config.ControlRoot)
	if controlRoot != "" && !filepath.IsAbs(controlRoot) {
		return nil, errors.New("executor control root must be absolute")
	}

	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("configure executor transport: unsupported default transport %T", http.DefaultTransport)
	}
	base := defaultTransport.Clone()
	base.ForceAttemptHTTP2 = true
	if endpoint.Scheme == "http" && config.Transport == "grpc" {
		base.Protocols = new(http.Protocols)
		base.Protocols.SetUnencryptedHTTP2(true)
	}
	if endpoint.Scheme == "https" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: strings.TrimSpace(config.TLSServerName)}
		if config.CACert != "" {
			certificate, err := os.ReadFile(config.CACert)
			if err != nil {
				return nil, fmt.Errorf("read executor CA: %w", err)
			}
			roots, err := x509.SystemCertPool()
			if err != nil || roots == nil {
				roots = x509.NewCertPool()
			}
			if !roots.AppendCertsFromPEM(certificate) {
				return nil, errors.New("executor CA contains no certificates")
			}
			tlsConfig.RootCAs = roots
		}
		if config.ClientCert != "" {
			certificate, err := tls.LoadX509KeyPair(config.ClientCert, config.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load executor client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{certificate}
		}
		base.TLSClientConfig = tlsConfig
	}

	transport := strings.TrimSpace(config.Transport)
	if transport == "" {
		transport = "grpc"
	}
	if transport != "connect" && transport != "grpc" {
		return nil, errors.New("executor transport must be connect or grpc")
	}
	options := make([]connect.ClientOption, 0, 1)
	if transport == "grpc" {
		options = append(options, connect.WithGRPC())
	}
	httpClient := &http.Client{Transport: &executorTokenTransport{base: base, token: strings.TrimSpace(config.Token)}}
	return &Remote{
		workspaces:  workspaces,
		controlRoot: filepath.Clean(controlRoot),
		client: executorv1connect.NewWorkspaceExecutorServiceClient(
			httpClient,
			strings.TrimRight(config.Endpoint, "/"),
			options...,
		),
		processes: make(map[string]string),
	}, nil
}

func (r *Remote) Start(ctx context.Context, principal domain.Principal, spec domain.ProcessSpec) (domain.ProcessInfo, error) {
	workspace, err := r.workspaces.Get(ctx, principal, spec.WorkspaceID)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	identity, err := r.executorIdentity(principal, workspace)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	response, err := r.client.StartProcess(ctx, connect.NewRequest(&executorv1.StartProcessRequest{
		Identity: identity,
		Kind:     spec.Kind, ServerId: spec.ServerID, Title: spec.Title, Command: spec.Command,
		Args: append([]string(nil), spec.Args...), Cwd: spec.CWD, Env: copyEnvironment(spec.Env),
	}))
	if err != nil {
		return domain.ProcessInfo{}, remoteError(err)
	}
	info, err := processDomain(response.Msg.GetProcess(), workspace)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	r.remember(info.ID, workspace.ID)
	return info, nil
}

func (r *Remote) ListShells(ctx context.Context, principal domain.Principal) ([]domain.Shell, error) {
	response, err := r.client.ListShells(ctx, connect.NewRequest(&executorv1.ListShellsRequest{TenantId: principal.TenantID}))
	if err != nil {
		return nil, remoteError(err)
	}
	result := make([]domain.Shell, 0, len(response.Msg.GetShells()))
	for _, shell := range response.Msg.GetShells() {
		if shell == nil || !filepath.IsAbs(shell.GetPath()) || strings.TrimSpace(shell.GetName()) == "" {
			return nil, fmt.Errorf("%w: executor returned an invalid shell", domain.ErrUnavailable)
		}
		result = append(result, domain.Shell{Path: shell.GetPath(), Name: shell.GetName(), Acceptable: shell.GetAcceptable()})
	}
	return result, nil
}

func (r *Remote) Get(ctx context.Context, principal domain.Principal, id string) (domain.ProcessInfo, error) {
	response, err := r.client.GetProcess(ctx, connect.NewRequest(&executorv1.GetProcessRequest{TenantId: principal.TenantID, ProcessId: id}))
	if err != nil {
		return domain.ProcessInfo{}, remoteError(err)
	}
	workspaceID := r.workspaceID(id)
	if workspaceID == "" {
		workspaceID = response.Msg.GetProcess().GetWorkspaceId()
	}
	workspace, err := r.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	info, err := processDomain(response.Msg.GetProcess(), workspace)
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	r.remember(info.ID, workspace.ID)
	return info, nil
}

func (r *Remote) List(ctx context.Context, principal domain.Principal, workspaceID, kind string) ([]domain.ProcessInfo, error) {
	workspace, err := r.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return nil, err
	}
	identity, err := r.executorIdentity(principal, workspace)
	if err != nil {
		return nil, err
	}
	response, err := r.client.ListProcesses(ctx, connect.NewRequest(&executorv1.ListProcessesRequest{
		Identity: identity,
		Kind:     kind,
	}))
	if err != nil {
		return nil, remoteError(err)
	}
	result := make([]domain.ProcessInfo, 0, len(response.Msg.GetProcesses()))
	for _, value := range response.Msg.GetProcesses() {
		info, err := processDomain(value, workspace)
		if err != nil {
			return nil, err
		}
		r.remember(info.ID, workspace.ID)
		result = append(result, info)
	}
	return result, nil
}

func (r *Remote) Write(ctx context.Context, principal domain.Principal, id string, data []byte) error {
	_, err := r.client.WriteProcess(ctx, connect.NewRequest(&executorv1.WriteProcessRequest{TenantId: principal.TenantID, ProcessId: id, Data: append([]byte(nil), data...)}))
	return remoteError(err)
}

func (r *Remote) Resize(ctx context.Context, principal domain.Principal, id string, cols, rows uint32) error {
	_, err := r.client.ResizeProcess(ctx, connect.NewRequest(&executorv1.ResizeProcessRequest{TenantId: principal.TenantID, ProcessId: id, Cols: cols, Rows: rows}))
	return remoteError(err)
}

func (r *Remote) Stop(ctx context.Context, principal domain.Principal, id string) error {
	_, err := r.client.StopProcess(ctx, connect.NewRequest(&executorv1.StopProcessRequest{TenantId: principal.TenantID, ProcessId: id}))
	return remoteError(err)
}

func (r *Remote) Remove(ctx context.Context, principal domain.Principal, id string) error {
	_, err := r.client.RemoveProcess(ctx, connect.NewRequest(&executorv1.RemoveProcessRequest{TenantId: principal.TenantID, ProcessId: id}))
	if err == nil {
		r.mu.Lock()
		delete(r.processes, id)
		r.mu.Unlock()
	}
	return remoteError(err)
}

func (r *Remote) Watch(ctx context.Context, principal domain.Principal, id string, cursor uint64) ([]domain.ProcessChunk, <-chan domain.ProcessChunk, func(), error) {
	info, err := r.Get(ctx, principal, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if cursor > info.Cursor {
		return nil, nil, nil, domain.ErrInvalid
	}
	watchCtx, cancel := context.WithCancel(ctx)
	stream, err := r.client.WatchProcess(watchCtx, connect.NewRequest(&executorv1.WatchProcessRequest{TenantId: principal.TenantID, ProcessId: id, Cursor: cursor}))
	if err != nil {
		cancel()
		return nil, nil, nil, remoteError(err)
	}
	updates := make(chan domain.ProcessChunk, 256)
	go func() {
		defer close(updates)
		defer cancel()
		for stream.Receive() {
			value := stream.Msg()
			chunk := domain.ProcessChunk{ProcessID: value.GetProcessId(), Cursor: value.GetCursor(), Data: append([]byte(nil), value.GetData()...), Exited: value.GetExited(), ExitCode: value.GetExitCode()}
			select {
			case updates <- chunk:
			case <-watchCtx.Done():
				return
			default:
				return
			}
		}
	}()
	var once sync.Once
	return nil, updates, func() { once.Do(cancel) }, nil
}

func (r *Remote) Run(ctx context.Context, principal domain.Principal, spec domain.CommandSpec) (domain.CommandResult, error) {
	workspace, err := r.workspaces.Get(ctx, principal, spec.WorkspaceID)
	if err != nil {
		return domain.CommandResult{}, err
	}
	identity, err := r.executorIdentity(principal, workspace)
	if err != nil {
		return domain.CommandResult{}, err
	}
	response, err := r.client.RunCommand(ctx, connect.NewRequest(&executorv1.RunCommandRequest{
		Identity: identity,
		Command:  spec.Command, Args: append([]string(nil), spec.Args...), Cwd: spec.CWD,
		Env: copyEnvironment(spec.Env), MaxOutputBytes: int64(spec.MaxOutputBytes),
	}))
	if err != nil {
		return domain.CommandResult{}, remoteError(err)
	}
	if response.Msg.GetWorkspaceRoot() == "" {
		return domain.CommandResult{}, fmt.Errorf("%w: executor returned an empty workspace root", domain.ErrUnavailable)
	}
	return domain.CommandResult{
		Output: append([]byte(nil), response.Msg.GetOutput()...), ExitCode: response.Msg.GetExitCode(), WorkspaceRoot: workspace.Root,
	}, nil
}

func (r *Remote) AllocatePreviewPort(ctx context.Context, principal domain.Principal, workspaceID string) (uint32, error) {
	workspace, err := r.workspaces.Get(ctx, principal, workspaceID)
	if err != nil {
		return 0, err
	}
	identity, err := r.executorIdentity(principal, workspace)
	if err != nil {
		return 0, err
	}
	response, err := r.client.AllocatePreviewPort(ctx, connect.NewRequest(&executorv1.AllocatePreviewPortRequest{
		Identity: identity,
	}))
	if err != nil {
		return 0, remoteError(err)
	}
	port := response.Msg.GetPort()
	if port == 0 || port > 65535 {
		return 0, fmt.Errorf("%w: executor returned an invalid preview port", domain.ErrUnavailable)
	}
	return port, nil
}

func (r *Remote) executorIdentity(principal domain.Principal, workspace domain.Workspace) (*executorv1.ProcessIdentity, error) {
	relative := "."
	if r.controlRoot != "" && r.controlRoot != "." {
		value, err := filepath.Rel(r.controlRoot, workspace.Root)
		if err != nil || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%w: workspace is outside the executor control root", domain.ErrForbidden)
		}
		relative = filepath.ToSlash(value)
	}
	return &executorv1.ProcessIdentity{TenantId: principal.TenantID, WorkspaceId: workspace.ID, RelativeRoot: relative}, nil
}

func (r *Remote) WaitPreviewPort(ctx context.Context, principal domain.Principal, processID string, preferred []uint32) (uint32, error) {
	response, err := r.client.WaitPreviewPort(ctx, connect.NewRequest(&executorv1.WaitPreviewPortRequest{
		TenantId: principal.TenantID, ProcessId: processID, PreferredPorts: append([]uint32(nil), preferred...),
	}))
	if err != nil {
		return 0, remoteError(err)
	}
	portNumber := response.Msg.GetPort()
	if portNumber == 0 || portNumber > 65535 {
		return 0, fmt.Errorf("%w: executor returned an invalid preview listener", domain.ErrUnavailable)
	}
	return portNumber, nil
}

func (r *Remote) remember(processID, workspaceID string) {
	r.mu.Lock()
	r.processes[processID] = workspaceID
	r.mu.Unlock()
}

func (r *Remote) workspaceID(processID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.processes[processID]
}

func processDomain(value *executorv1.ProcessInfo, workspace domain.Workspace) (domain.ProcessInfo, error) {
	if value == nil || value.GetId() == "" {
		return domain.ProcessInfo{}, fmt.Errorf("%w: executor returned an empty process", domain.ErrUnavailable)
	}
	cwd, err := remoteCWD(workspace.Root, value.GetCwd())
	if err != nil {
		return domain.ProcessInfo{}, err
	}
	return domain.ProcessInfo{
		ID: value.GetId(), WorkspaceID: workspace.ID, Kind: value.GetKind(), ServerID: value.GetServerId(),
		Title: value.GetTitle(), Command: value.GetCommand(), CWD: cwd, Status: value.GetStatus(), Cursor: value.GetCursor(),
		ExitCode: value.GetExitCode(), StartedAt: unixTime(value.GetStartedUnixMillis()), EndedAt: unixTime(value.GetEndedUnixMillis()),
	}, nil
}

func remoteCWD(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.ContainsRune(relative, '\x00') {
		return "", fmt.Errorf("%w: executor returned an invalid working directory", domain.ErrUnavailable)
	}
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: executor working directory escaped the workspace", domain.ErrUnavailable)
	}
	return filepath.Join(root, cleaned), nil
}

func unixTime(milliseconds int64) time.Time {
	if milliseconds == 0 {
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}

func copyEnvironment(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func remoteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var target error
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		target = domain.ErrInvalid
	case connect.CodeNotFound:
		target = domain.ErrNotFound
	case connect.CodeAborted:
		target = domain.ErrStaleRevision
	case connect.CodeAlreadyExists, connect.CodeFailedPrecondition:
		target = domain.ErrConflict
	case connect.CodePermissionDenied:
		target = domain.ErrForbidden
	case connect.CodeOutOfRange:
		target = domain.ErrReplayTooOld
	case connect.CodeResourceExhausted:
		target = domain.ErrOutputTruncated
	default:
		target = domain.ErrUnavailable
	}
	return fmt.Errorf("%w: remote executor: %v", target, err)
}

type executorTokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *executorTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
