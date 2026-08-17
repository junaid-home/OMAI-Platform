package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omai/backend/internal/domain"
)

type OpenCodeConfig struct {
	ID          string
	Command     string
	CommandArgs []string
	Workspace   string
	Home        string
	Version     string
	AutoApprove bool
}

type OpenCode struct {
	config OpenCodeConfig
}

func NewOpenCode(config OpenCodeConfig) (*OpenCode, error) {
	if config.ID == "" {
		config.ID = "opencode"
	}
	if config.Version == "" {
		config.Version = "unknown"
	}
	if config.Command == "" || strings.ContainsRune(config.Command, '\x00') {
		return nil, errors.New("OpenCode harness command is required")
	}
	if len(config.CommandArgs) > 32 {
		return nil, errors.New("OpenCode harness command prefix exceeds 32 arguments")
	}
	for _, argument := range config.CommandArgs {
		if len(argument) > 64<<10 || strings.ContainsRune(argument, '\x00') {
			return nil, errors.New("OpenCode harness command prefix is invalid")
		}
	}
	for name, path := range map[string]string{"workspace": config.Workspace, "home": config.Home} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("OpenCode %s must be absolute", name)
		}
	}
	if err := os.MkdirAll(config.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create OpenCode harness home: %w", err)
	}
	return &OpenCode{config: config}, nil
}

func (o *OpenCode) Descriptor() domain.RuntimeDescriptor {
	return domain.RuntimeDescriptor{
		ID: o.config.ID, Runtime: "opencode", Label: "OpenCode via OMAI Go harness driver",
		Version: o.config.Version, NodeID: "workspace-executor", Transport: "grpc", Enabled: true,
		Capabilities: []domain.Capability{
			{Name: "chat", Enabled: true}, {Name: "streaming", Enabled: true},
			{Name: "cancel", Enabled: true}, {Name: "model-gateway", Enabled: true},
			{Name: "workspace-tools", Enabled: o.config.AutoApprove},
		},
	}
}

func (o *OpenCode) Command() string { return o.config.Command }

func (o *OpenCode) Invocation(prompt domain.Prompt, harnessSessionID string, lease ModelLease) (Invocation, error) {
	if prompt.Root != o.config.Workspace || prompt.WorkspaceID == "" {
		return Invocation{}, fmt.Errorf("%w: prompt is not assigned to this workspace", domain.ErrForbidden)
	}
	if len(prompt.Text) == 0 || len(prompt.Text) > 4<<20 {
		return Invocation{}, fmt.Errorf("%w: prompt must contain between 1 and %d bytes", domain.ErrInvalid, 4<<20)
	}
	if lease.Token == "" || lease.RouteID == "" || lease.BaseURL == "" {
		return Invocation{}, errors.New("model capability is incomplete")
	}
	configuration, err := o.providerConfiguration(prompt, lease)
	if err != nil {
		return Invocation{}, err
	}
	args := append([]string(nil), o.config.CommandArgs...)
	args = append(args, "run", "--format", "json", "--thinking", "--model", "omai/"+lease.RouteID)
	if o.config.AutoApprove {
		args = append(args, "--auto")
	}
	if harnessSessionID != "" {
		args = append(args, "--session", harnessSessionID)
	}
	if title := strings.TrimSpace(prompt.Title); title != "" {
		if len(title) > 1024 || strings.ContainsRune(title, '\x00') {
			return Invocation{}, fmt.Errorf("%w: harness title is invalid", domain.ErrInvalid)
		}
		args = append(args, "--title", title)
	}
	stdin := append([]byte(nil), prompt.Text...)
	if !bytes.HasSuffix(stdin, []byte("\n")) {
		stdin = append(stdin, '\n')
	}
	return Invocation{
		Command: o.config.Command,
		Args:    args,
		Dir:     o.config.Workspace,
		Stdin:   stdin,
		Env:     o.environment(string(configuration)),
	}, nil
}

func (o *OpenCode) NewDecoder() Decoder { return &openCodeDecoder{} }

func (o *OpenCode) providerConfiguration(prompt domain.Prompt, lease ModelLease) ([]byte, error) {
	contextTokens, outputTokens, err := openCodeModelLimits(prompt)
	if err != nil {
		return nil, err
	}
	document := map[string]any{
		"autoupdate":        false,
		"enabled_providers": []string{"omai"},
		"model":             "omai/" + lease.RouteID,
		"permission":        map[string]any{"external_directory": "deny"},
		"provider": map[string]any{
			"omai": map[string]any{
				"name": "OMAI Go Model Gateway",
				"npm":  "@ai-sdk/openai-compatible",
				"env":  []string{},
				"options": map[string]any{
					"apiKey":  lease.Token,
					"baseURL": strings.TrimRight(lease.BaseURL, "/"),
				},
				"models": map[string]any{
					lease.RouteID: map[string]any{
						"name":      prompt.ProviderID + "/" + prompt.ModelID,
						"tool_call": true,
						"limit":     map[string]any{"context": contextTokens, "output": outputTokens},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode OpenCode provider configuration: %w", err)
	}
	return encoded, nil
}

func openCodeModelLimits(prompt domain.Prompt) (int64, int64, error) {
	contextTokens := prompt.ModelContextTokens
	if contextTokens == 0 {
		contextTokens = 128_000
	}
	if contextTokens < 1_024 || contextTokens > 16_777_216 {
		return 0, 0, fmt.Errorf("%w: model context limit is outside the supported range", domain.ErrInvalid)
	}
	outputTokens := prompt.ModelOutputTokens
	if outputTokens == 0 {
		outputTokens = 32_768
	}
	if outputTokens < 1 || outputTokens > 1_048_576 {
		return 0, 0, fmt.Errorf("%w: model output limit is outside the supported range", domain.ErrInvalid)
	}
	if outputTokens > 131_072 {
		outputTokens = 131_072
	}
	if outputTokens > contextTokens {
		outputTokens = contextTokens
	}
	return contextTokens, outputTokens, nil
}

func (o *OpenCode) environment(configuration string) []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return []string{
		"PATH=" + path,
		"HOME=" + o.config.Home,
		"XDG_CONFIG_HOME=" + filepath.Join(o.config.Home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(o.config.Home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(o.config.Home, ".cache"),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TERM=dumb",
		"NO_COLOR=1",
		"CI=true",
		"PWD=" + o.config.Workspace,
		"OPENCODE_CONFIG_CONTENT=" + configuration,
		"OPENCODE_DISABLE_AUTOUPDATE=true",
		"OPENCODE_DISABLE_MODELS_FETCH=true",
		"OPENCODE_DISABLE_PROJECT_CONFIG=true",
		"OPENCODE_DISABLE_SHARE=true",
		"OPENCODE_DISABLE_TERMINAL_TITLE=true",
	}
}

type openCodeDecoder struct{}

type openCodeEnvelope struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
	Error     json.RawMessage `json:"error"`
}

type openCodePart struct {
	ID    string            `json:"id"`
	Type  string            `json:"type"`
	Text  string            `json:"text"`
	Tool  string            `json:"tool"`
	State openCodeToolState `json:"state"`
}

type openCodeToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  string          `json:"error"`
}

func (*openCodeDecoder) Decode(line []byte) (DecodedEvent, error) {
	var envelope openCodeEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return DecodedEvent{}, fmt.Errorf("%w: decode OpenCode event: %v", ErrInvalidEvent, err)
	}
	if envelope.Type == "" || !validSessionIdentifier(envelope.SessionID) {
		return DecodedEvent{}, fmt.Errorf("%w: event type and sessionID are required", ErrInvalidEvent)
	}
	at := time.Now().UTC()
	if envelope.Timestamp > 0 {
		at = time.UnixMilli(envelope.Timestamp).UTC()
	}
	result := DecodedEvent{HarnessSessionID: envelope.SessionID}
	switch envelope.Type {
	case "text", "reasoning", "tool_use", "step_start", "step_finish":
		var part openCodePart
		if len(envelope.Part) == 0 || json.Unmarshal(envelope.Part, &part) != nil {
			return DecodedEvent{}, fmt.Errorf("%w: OpenCode event part is invalid", ErrInvalidEvent)
		}
		switch envelope.Type {
		case "text":
			result.Events = append(result.Events, domain.RuntimeEvent{Kind: domain.RuntimeEventAgentMessage, MessageID: part.ID, Text: part.Text, At: at})
		case "reasoning":
			result.Events = append(result.Events, domain.RuntimeEvent{Kind: domain.RuntimeEventAgentThought, MessageID: part.ID, Text: part.Text, At: at})
		case "tool_use":
			arguments := canonicalJSON(part.State.Input, []byte("{}"))
			result.Events = append(result.Events, domain.RuntimeEvent{
				Kind: domain.RuntimeEventToolCall, ToolCallID: part.ID, ToolName: part.Tool,
				ArgumentsJSON: arguments, Status: "running", At: at,
			})
			output := canonicalJSON(part.State.Output, nil)
			if len(output) == 0 && part.State.Error != "" {
				output, _ = json.Marshal(map[string]string{"error": part.State.Error})
			}
			result.Events = append(result.Events, domain.RuntimeEvent{
				Kind: domain.RuntimeEventToolUpdate, ToolCallID: part.ID, ToolName: part.Tool,
				ArgumentsJSON: arguments, OutputJSON: output, Status: part.State.Status, At: at,
			})
		case "step_start":
			result.Events = append(result.Events, domain.RuntimeEvent{Kind: domain.RuntimeEventStatus, MessageID: part.ID, Status: "step_started", At: at})
		case "step_finish":
			result.Events = append(result.Events, domain.RuntimeEvent{Kind: domain.RuntimeEventStatus, MessageID: part.ID, Status: "step_finished", At: at})
		}
	case "error":
		text := "OpenCode harness error"
		if len(envelope.Error) != 0 {
			text = string(envelope.Error)
			var structured struct {
				Name string `json:"name"`
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			}
			if json.Unmarshal(envelope.Error, &structured) == nil {
				if structured.Data.Message != "" {
					text = structured.Data.Message
				} else if structured.Name != "" {
					text = structured.Name
				}
			}
		}
		result.Events = append(result.Events, domain.RuntimeEvent{Kind: domain.RuntimeEventError, Text: text, Status: "error", At: at})
	default:
		// Forward compatibility: unknown OpenCode event kinds remain observable at
		// the raw harness boundary but do not become invented platform semantics.
	}
	return result, nil
}

func canonicalJSON(raw, fallback []byte) []byte {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return append([]byte(nil), fallback...)
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return append([]byte(nil), fallback...)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return append([]byte(nil), fallback...)
	}
	return encoded
}
