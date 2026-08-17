package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/omai/backend/internal/adapter/harness"
	"github.com/omai/backend/internal/domain"
)

const (
	probeSentinel    = "OMAI_DEEPSEEK_ACP_OK"
	maxACPMessage    = 16 << 20
	maxCollectedText = 1 << 20
)

type probeConfig struct {
	OpenCodeCommand string
	OpenCodeEntry   string
	Workspace       string
	Home            string
	GatewayURL      string
	GatewayToken    string
	ProviderID      string
	ModelID         string
	Timeout         time.Duration
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "OpenCode ACP probe failed:", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := loadProbeConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for Go model edge: %w", err)
	}
	edgeBaseURL := "http://" + listener.Addr().String()
	leases, err := harness.NewLeaseStore(edgeBaseURL, config.Timeout, 4)
	if err != nil {
		_ = listener.Close()
		return err
	}
	edge, err := harness.NewModelEdge(leases, harness.ModelGatewayConfig{
		Endpoint: config.GatewayURL, Token: config.GatewayToken, Transport: "connect",
	}, "deepseek-acp-live-probe")
	if err != nil {
		_ = listener.Close()
		return err
	}
	edgeServer := &http.Server{
		Handler:           edge.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	edgeErrors := make(chan error, 1)
	go func() {
		edgeErrors <- edgeServer.Serve(listener)
	}()
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = edgeServer.Shutdown(shutdown)
	}()

	prompt := domain.Prompt{
		SessionID: "deepseek-acp-live-probe", ExternalSessionID: "deepseek-acp-live-probe",
		WorkspaceID: "deepseek-acp-live-workspace", Root: config.Workspace,
		Text:  "Return exactly " + probeSentinel + ". Do not call tools and do not explain.",
		Title: "OMAI DeepSeek ACP live probe", ProviderID: config.ProviderID, ModelID: config.ModelID,
		ModelContextTokens: 128_000, ModelOutputTokens: 256,
		Principal: domain.Principal{TenantID: "omai-live-audit", ActorID: "omai-acp-probe"},
	}
	lease, err := leases.Issue(prompt)
	if err != nil {
		return err
	}
	defer leases.Revoke(lease.Token)
	driver, err := harness.NewOpenCode(harness.OpenCodeConfig{
		ID: "opencode-acp-live", Command: config.OpenCodeCommand,
		CommandArgs: []string{"--conditions=browser", config.OpenCodeEntry},
		Workspace:   config.Workspace, Home: config.Home, Version: "source", AutoApprove: false,
	})
	if err != nil {
		return err
	}
	invocation, err := driver.Invocation(prompt, "", lease)
	if err != nil {
		return err
	}
	if environmentValue(invocation.Env, "DEEPSEEK_API_KEY") != "" {
		return errors.New("provider credential was exposed to the OpenCode environment")
	}

	client, err := startACPClient(ctx, config, invocation.Env)
	if err != nil {
		return err
	}
	defer client.Close()

	var initialized struct {
		ProtocolVersion int `json:"protocolVersion"`
		AgentInfo       struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := client.Request(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"_meta": map[string]any{"terminal-auth": false},
		},
		"clientInfo": map[string]string{"name": "omai-go-acp-probe", "version": "1.0.0"},
	}, &initialized); err != nil {
		return err
	}
	if initialized.ProtocolVersion != 1 || initialized.AgentInfo.Name != "OpenCode" {
		return fmt.Errorf("unexpected ACP initialization response from %q", initialized.AgentInfo.Name)
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if err := client.Request(ctx, "session/new", map[string]any{
		"cwd": config.Workspace, "mcpServers": []any{},
	}, &session); err != nil {
		return err
	}
	if session.SessionID == "" {
		return errors.New("OpenCode ACP returned an empty session id")
	}
	var completed struct {
		StopReason string `json:"stopReason"`
	}
	if err := client.Request(ctx, "session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt": []any{map[string]string{
			"type": "text", "text": prompt.Text,
		}},
	}, &completed); err != nil {
		return err
	}
	if completed.StopReason != "end_turn" {
		return fmt.Errorf("OpenCode ACP turn stopped with %q", completed.StopReason)
	}
	notificationCtx, stopNotifications := context.WithTimeout(ctx, 10*time.Second)
	defer stopNotifications()
	if err := client.WaitForAgentText(notificationCtx, probeSentinel); err != nil {
		return err
	}
	select {
	case serveErr := <-edgeErrors:
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("go model edge stopped: %w", serveErr)
		}
	default:
	}

	report := struct {
		ACPTransport             string `json:"acp_transport"`
		Agent                    string `json:"agent"`
		AgentVersion             string `json:"agent_version"`
		OpenCodeProvider         string `json:"opencode_provider"`
		GoProviderRoute          string `json:"go_provider_route"`
		Model                    string `json:"model"`
		StopReason               string `json:"stop_reason"`
		SentinelObserved         bool   `json:"sentinel_observed"`
		GoOwnsProviderCredential bool   `json:"go_owns_provider_credential"`
		CredentialInHarness      bool   `json:"credential_in_harness"`
	}{
		ACPTransport: "stdio-jsonrpc", Agent: initialized.AgentInfo.Name,
		AgentVersion: initialized.AgentInfo.Version, OpenCodeProvider: "omai",
		GoProviderRoute: config.ProviderID, Model: config.ModelID,
		StopReason: completed.StopReason, SentinelObserved: true,
		GoOwnsProviderCredential: true, CredentialInHarness: false,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func loadProbeConfig() (probeConfig, error) {
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != "" {
		return probeConfig{}, errors.New("DEEPSEEK_API_KEY must be scoped to the Go ADK runtime, not the ACP probe")
	}
	config := probeConfig{
		OpenCodeCommand: requiredEnvironment("OMAI_TEST_OPENCODE_COMMAND"),
		OpenCodeEntry:   requiredEnvironment("OMAI_TEST_OPENCODE_ENTRY"),
		Workspace:       requiredEnvironment("OMAI_TEST_WORKSPACE"),
		Home:            requiredEnvironment("OMAI_TEST_OPENCODE_HOME"),
		GatewayURL:      requiredEnvironment("OMAI_TEST_MODEL_GATEWAY_URL"),
		GatewayToken:    requiredEnvironment("OMAI_TEST_MODEL_GATEWAY_TOKEN"),
		ProviderID:      requiredEnvironment("OMAI_TEST_PROVIDER_ID"),
		ModelID:         requiredEnvironment("OMAI_TEST_MODEL_ID"),
		Timeout:         3 * time.Minute,
	}
	for name, value := range map[string]string{
		"OMAI_TEST_OPENCODE_COMMAND":    config.OpenCodeCommand,
		"OMAI_TEST_OPENCODE_ENTRY":      config.OpenCodeEntry,
		"OMAI_TEST_WORKSPACE":           config.Workspace,
		"OMAI_TEST_OPENCODE_HOME":       config.Home,
		"OMAI_TEST_MODEL_GATEWAY_URL":   config.GatewayURL,
		"OMAI_TEST_MODEL_GATEWAY_TOKEN": config.GatewayToken,
		"OMAI_TEST_PROVIDER_ID":         config.ProviderID,
		"OMAI_TEST_MODEL_ID":            config.ModelID,
	} {
		if value == "" {
			return probeConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("OMAI_TEST_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 10*time.Second || parsed > 10*time.Minute {
			return probeConfig{}, errors.New("OMAI_TEST_TIMEOUT must be between 10s and 10m")
		}
		config.Timeout = parsed
	}
	for name, path := range map[string]string{
		"OMAI_TEST_OPENCODE_COMMAND": config.OpenCodeCommand,
		"OMAI_TEST_OPENCODE_ENTRY":   config.OpenCodeEntry,
		"OMAI_TEST_WORKSPACE":        config.Workspace,
		"OMAI_TEST_OPENCODE_HOME":    config.Home,
	} {
		if !filepath.IsAbs(path) {
			return probeConfig{}, fmt.Errorf("%s must be absolute", name)
		}
	}
	executable, err := os.Stat(config.OpenCodeCommand)
	if err != nil {
		return probeConfig{}, fmt.Errorf("inspect OMAI_TEST_OPENCODE_COMMAND: %w", err)
	}
	if !executable.Mode().IsRegular() || executable.Mode().Perm()&0o111 == 0 {
		return probeConfig{}, errors.New("OMAI_TEST_OPENCODE_COMMAND must be an executable regular file")
	}
	if len(config.GatewayToken) < 32 {
		return probeConfig{}, errors.New("OMAI_TEST_MODEL_GATEWAY_TOKEN must contain at least 32 characters")
	}
	return config, nil
}

func requiredEnvironment(name string) string { return strings.TrimSpace(os.Getenv(name)) }

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type acpRead struct {
	message rpcMessage
	err     error
}

type acpClient struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	messages  <-chan acpRead
	wait      <-chan error
	nextID    int64
	agentText strings.Builder
}

func startACPClient(ctx context.Context, config probeConfig, environment []string) (*acpClient, error) {
	args := []string{"--conditions=browser", config.OpenCodeEntry, "acp", "--cwd", config.Workspace}
	// #nosec G204 -- loadProbeConfig requires an absolute executable regular file;
	// exec.CommandContext receives an argv vector and never invokes a shell.
	command := exec.CommandContext(ctx, config.OpenCodeCommand, args...)
	command.Dir = config.Workspace
	command.Env = append([]string(nil), environment...)
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	command.WaitDelay = 5 * time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open ACP stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open ACP stdout: %w", err)
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start OpenCode ACP: %w", err)
	}
	messages := make(chan acpRead, 16)
	go readACP(stdout, messages)
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	return &acpClient{command: command, stdin: stdin, messages: messages, wait: wait}, nil
}

func readACP(reader io.Reader, destination chan<- acpRead) {
	defer close(destination)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxACPMessage)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			destination <- acpRead{err: errors.New("OpenCode ACP emitted invalid JSON-RPC")}
			return
		}
		destination <- acpRead{message: message}
	}
	if err := scanner.Err(); err != nil {
		destination <- acpRead{err: fmt.Errorf("read OpenCode ACP: %w", err)}
	}
}

func (c *acpClient) Request(ctx context.Context, method string, params any, result any) error {
	c.nextID++
	id := c.nextID
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := json.NewEncoder(c.stdin).Encode(request); err != nil {
		return fmt.Errorf("send ACP %s request: %w", method, err)
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("ACP %s request: %w", method, ctx.Err())
		case read, ok := <-c.messages:
			if !ok {
				return fmt.Errorf("OpenCode ACP stopped during %s", method)
			}
			if read.err != nil {
				return read.err
			}
			message := read.message
			if message.Method != "" && len(message.ID) != 0 {
				if err := c.rejectServerRequest(message); err != nil {
					return err
				}
				continue
			}
			if message.Method != "" {
				c.observeNotification(message)
				continue
			}
			var responseID int64
			if json.Unmarshal(message.ID, &responseID) != nil || responseID != id {
				continue
			}
			if presentJSON(message.Error) {
				var rpcError struct {
					Code int `json:"code"`
				}
				_ = json.Unmarshal(message.Error, &rpcError)
				return fmt.Errorf("ACP %s failed with JSON-RPC code %d", method, rpcError.Code)
			}
			if result == nil {
				return nil
			}
			if len(message.Result) == 0 || json.Unmarshal(message.Result, result) != nil {
				return fmt.Errorf("ACP %s returned an invalid result", method)
			}
			return nil
		}
	}
}

func (c *acpClient) rejectServerRequest(message rpcMessage) error {
	response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID)}
	if message.Method == "session/request_permission" {
		response["result"] = map[string]any{
			"outcome": map[string]string{"outcome": "selected", "optionId": "reject"},
		}
	} else {
		response["error"] = map[string]any{"code": -32601, "message": "client method unavailable"}
	}
	if err := json.NewEncoder(c.stdin).Encode(response); err != nil {
		return fmt.Errorf("reject ACP server request: %w", err)
	}
	return nil
}

func (c *acpClient) observeNotification(message rpcMessage) {
	if message.Method != "session/update" || c.agentText.Len() >= maxCollectedText {
		return
	}
	var params struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.Update.SessionUpdate != "agent_message_chunk" || params.Update.Content.Type != "text" {
		return
	}
	remaining := maxCollectedText - c.agentText.Len()
	text := params.Update.Content.Text
	if len(text) > remaining {
		text = text[:remaining]
	}
	c.agentText.WriteString(text)
}

func (c *acpClient) AgentText() string { return c.agentText.String() }

func (c *acpClient) WaitForAgentText(ctx context.Context, expected string) error {
	if strings.Contains(c.agentText.String(), expected) {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return errors.New("real provider response did not contain the live-test sentinel")
		case read, ok := <-c.messages:
			if !ok {
				return errors.New("OpenCode ACP stopped before delivering the agent response")
			}
			if read.err != nil {
				return read.err
			}
			if read.message.Method != "" && len(read.message.ID) != 0 {
				if err := c.rejectServerRequest(read.message); err != nil {
					return err
				}
				continue
			}
			c.observeNotification(read.message)
			if strings.Contains(c.agentText.String(), expected) {
				return nil
			}
		}
	}
}

func (c *acpClient) Close() {
	_ = c.stdin.Close()
	select {
	case <-c.wait:
		return
	case <-time.After(5 * time.Second):
		if c.command.Process != nil {
			_ = syscall.Kill(-c.command.Process.Pid, syscall.SIGKILL)
		}
		<-c.wait
	}
}

func presentJSON(value json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(value))
	return trimmed != "" && trimmed != "null"
}
