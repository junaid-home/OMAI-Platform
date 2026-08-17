package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	uabv1 "github.com/omai/backend/gen/go/uab/v1"
	"github.com/omai/backend/gen/go/uab/v1/uabv1connect"
	"github.com/omai/backend/internal/domain"
)

type FileConfig struct {
	Runtimes []RemoteConfig `json:"runtimes"`
}

type RemoteConfig struct {
	ID            string        `json:"id"`
	Runtime       string        `json:"runtime"`
	Label         string        `json:"label"`
	Version       string        `json:"version"`
	NodeID        string        `json:"node_id"`
	Endpoint      string        `json:"endpoint"`
	TokenEnv      string        `json:"token_env"`
	Transport     string        `json:"transport"`
	Enabled       bool          `json:"enabled"`
	Capabilities  []string      `json:"capabilities"`
	Models        []ModelConfig `json:"models"`
	CACert        string        `json:"ca_cert"`
	ClientCert    string        `json:"client_cert"`
	ClientKey     string        `json:"client_key"`
	TLSServerName string        `json:"tls_server_name"`
}

type ModelConfig struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Context      int64  `json:"context"`
	Free         bool   `json:"free"`
}

func (c RemoteConfig) ValidateProduction() error {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "https" {
		return fmt.Errorf("runtime %s must use HTTPS in production", c.ID)
	}
	if c.CACert == "" || c.ClientCert == "" || c.ClientKey == "" {
		return fmt.Errorf("runtime %s requires mutual TLS in production", c.ID)
	}
	return nil
}

func LoadFile(path string) ([]RemoteConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	// #nosec G304 -- path is operator-owned startup configuration, never request data.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runtime config: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var config FileConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode runtime config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode runtime config: unexpected trailing JSON value")
	}
	seen := make(map[string]struct{})
	for index := range config.Runtimes {
		runtime := &config.Runtimes[index]
		if runtime.ID == "" || runtime.Label == "" || runtime.Endpoint == "" {
			return nil, fmt.Errorf("runtime %d requires id, label, and endpoint", index)
		}
		if _, exists := seen[runtime.ID]; exists {
			return nil, fmt.Errorf("duplicate runtime id %q", runtime.ID)
		}
		seen[runtime.ID] = struct{}{}
		endpoint, err := url.Parse(runtime.Endpoint)
		if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
			return nil, fmt.Errorf("runtime %s has invalid endpoint", runtime.ID)
		}
		if runtime.Transport == "" {
			runtime.Transport = "connect"
		}
		if runtime.Transport != "connect" && runtime.Transport != "grpc" {
			return nil, fmt.Errorf("runtime %s transport must be connect or grpc", runtime.ID)
		}
		if runtime.Runtime == "" {
			runtime.Runtime = "remote"
		}
		if runtime.NodeID == "" {
			runtime.NodeID = endpoint.Hostname()
		}
		if runtime.TokenEnv == "" {
			return nil, fmt.Errorf("runtime %s requires token_env", runtime.ID)
		}
		if (runtime.ClientCert == "") != (runtime.ClientKey == "") {
			return nil, fmt.Errorf("runtime %s client certificate and key must be set together", runtime.ID)
		}
		if endpoint.Scheme != "https" && (runtime.CACert != "" || runtime.ClientCert != "" || runtime.TLSServerName != "") {
			return nil, fmt.Errorf("runtime %s TLS options require an HTTPS endpoint", runtime.ID)
		}
	}
	return config.Runtimes, nil
}

type Remote struct {
	descriptor domain.RuntimeDescriptor
	client     uabv1connect.AgentRuntimeServiceClient
	timeout    time.Duration
}

func NewRemote(config RemoteConfig, timeout time.Duration) (*Remote, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, fmt.Errorf("runtime %s has invalid endpoint", config.ID)
	}
	token := strings.TrimSpace(os.Getenv(config.TokenEnv))
	if len(token) < 32 {
		return nil, fmt.Errorf("runtime %s token from %s must contain at least 32 characters", config.ID, config.TokenEnv)
	}
	capabilities := make([]domain.Capability, 0, len(config.Capabilities))
	for _, capability := range config.Capabilities {
		capabilities = append(capabilities, domain.Capability{Name: capability, Enabled: true})
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("runtime %s cannot configure HTTP transport %T", config.ID, http.DefaultTransport)
	}
	httpTransport := defaultTransport.Clone()
	var baseTransport http.RoundTripper = httpTransport
	if config.Transport == "grpc" && endpoint.Scheme == "http" {
		httpTransport.Protocols = new(http.Protocols)
		httpTransport.Protocols.SetUnencryptedHTTP2(true)
	}
	if endpoint.Scheme == "https" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: config.TLSServerName}
		if config.CACert != "" {
			// #nosec G304 -- certificate path is operator-owned runtime configuration.
			data, err := os.ReadFile(config.CACert)
			if err != nil {
				return nil, fmt.Errorf("read runtime %s CA: %w", config.ID, err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(data) {
				return nil, fmt.Errorf("runtime %s CA contains no certificates", config.ID)
			}
			tlsConfig.RootCAs = roots
		}
		if config.ClientCert != "" {
			certificate, err := tls.LoadX509KeyPair(config.ClientCert, config.ClientKey)
			if err != nil {
				return nil, fmt.Errorf("load runtime %s client certificate: %w", config.ID, err)
			}
			tlsConfig.Certificates = []tls.Certificate{certificate}
		}
		httpTransport.TLSClientConfig = tlsConfig
	}
	transport := &tokenTransport{base: baseTransport, token: token}
	httpClient := &http.Client{Transport: transport}
	options := make([]connect.ClientOption, 0, 1)
	if config.Transport == "grpc" {
		options = append(options, connect.WithGRPC())
	}
	return &Remote{
		descriptor: domain.RuntimeDescriptor{
			ID: config.ID, Runtime: config.Runtime, Label: config.Label, Version: config.Version,
			NodeID: config.NodeID, Transport: config.Transport, Capabilities: capabilities, Enabled: config.Enabled,
		},
		client:  uabv1connect.NewAgentRuntimeServiceClient(httpClient, strings.TrimRight(config.Endpoint, "/"), options...),
		timeout: timeout,
	}, nil
}

func (r *Remote) Descriptor() domain.RuntimeDescriptor { return r.descriptor }

func (r *Remote) Health(ctx context.Context) domain.RuntimeHealth {
	started := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	response, err := r.client.RuntimeHealth(checkCtx, connect.NewRequest(&uabv1.AgentRuntimeHealthRequest{}))
	result := domain.RuntimeHealth{RuntimeID: r.descriptor.ID, Latency: time.Since(started), CheckedAt: time.Now().UTC()}
	if err != nil {
		result.Reason = connect.CodeOf(err).String() + ": " + err.Error()
		return result
	}
	result.Available = response.Msg.GetAvailable()
	result.Authenticated = response.Msg.GetAuthenticated()
	result.Version = response.Msg.GetVersion()
	result.Reason = response.Msg.GetReason()
	return result
}

func (r *Remote) Run(ctx context.Context, prompt domain.Prompt, emit func(domain.RuntimeEvent) error) error {
	request := connect.NewRequest(&uabv1.RuntimePrompt{
		SessionId: prompt.SessionID, ExternalSessionId: prompt.ExternalSessionID,
		WorkspaceId: prompt.WorkspaceID, Root: prompt.Root, Text: prompt.Text,
		Title: prompt.Title, ProviderId: prompt.ProviderID, ModelId: prompt.ModelID, ActorId: prompt.Principal.ActorID,
		TenantId: prompt.Principal.TenantID, ModelContextTokens: prompt.ModelContextTokens,
		ModelOutputTokens: prompt.ModelOutputTokens,
	})
	stream, err := r.client.Run(ctx, request)
	if err != nil {
		return fmt.Errorf("open runtime stream: %w", err)
	}
	for stream.Receive() {
		message := stream.Msg()
		if err := emit(domain.RuntimeEvent{
			Kind:          runtimeEventKind(message.GetKind()),
			MessageID:     message.GetMessageId(),
			Text:          message.GetText(),
			ToolCallID:    message.GetToolCallId(),
			ToolName:      message.GetToolName(),
			ArgumentsJSON: append([]byte(nil), message.GetArgumentsJson()...),
			OutputJSON:    append([]byte(nil), message.GetOutputJson()...),
			Status:        message.GetStatus(),
			At:            time.UnixMilli(message.GetUnixMillis()).UTC(),
		}); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("runtime stream: %w", err)
	}
	return stream.Err()
}

func (r *Remote) Cancel(ctx context.Context, sessionID string) bool {
	response, err := r.client.Cancel(ctx, connect.NewRequest(&uabv1.RuntimeCancelRequest{SessionId: sessionID}))
	return err == nil && response.Msg.GetCancelled()
}

func runtimeEventKind(kind uabv1.RuntimeEventKind) domain.RuntimeEventKind {
	switch kind {
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_MESSAGE_CHUNK:
		return domain.RuntimeEventAgentMessage
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_AGENT_THOUGHT_CHUNK:
		return domain.RuntimeEventAgentThought
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL:
		return domain.RuntimeEventToolCall
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_TOOL_CALL_UPDATE:
		return domain.RuntimeEventToolUpdate
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_STATUS:
		return domain.RuntimeEventStatus
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_ERROR:
		return domain.RuntimeEventError
	case uabv1.RuntimeEventKind_RUNTIME_EVENT_KIND_DONE:
		return domain.RuntimeEventDone
	default:
		return domain.RuntimeEventUnknown
	}
}

type tokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	clone.Header.Set("X-OMAI-Tenant-ID", "system")
	clone.Header.Set("X-OMAI-Actor-ID", "control-plane")
	return t.base.RoundTrip(clone)
}
