package domain

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrInvalid         = errors.New("invalid input")
	ErrConflict        = errors.New("conflict")
	ErrStaleRevision   = errors.New("stale revision")
	ErrForbidden       = errors.New("forbidden")
	ErrUnavailable     = errors.New("unavailable")
	ErrReplayTooOld    = errors.New("event replay cursor is too old")
	ErrOutputTruncated = errors.New("command output exceeded its limit")
)

type Principal struct {
	TenantID    string
	ActorID     string
	Permissions []string
	Service     bool
}

func (p Principal) Allows(permission string) bool {
	if permission == "" {
		return true
	}
	for _, candidate := range p.Permissions {
		if candidate == "*" || candidate == permission {
			return true
		}
	}
	return false
}

type Capability struct {
	Name    string
	Enabled bool
}

type RuntimeDescriptor struct {
	ID           string
	Runtime      string
	Label        string
	Version      string
	NodeID       string
	Transport    string
	Capabilities []Capability
	Enabled      bool
}

type RuntimeHealth struct {
	RuntimeID     string
	Available     bool
	Authenticated bool
	Version       string
	Latency       time.Duration
	Reason        string
	CheckedAt     time.Time
}

type Prompt struct {
	RuntimeID          string
	SessionID          string
	ExternalSessionID  string
	ProjectID          string
	WorkspaceID        string
	Root               string
	Text               string
	Title              string
	ProviderID         string
	ModelID            string
	ModelContextTokens int64
	ModelOutputTokens  int64
	Principal          Principal
}

type RuntimeEventKind uint8

const (
	RuntimeEventUnknown RuntimeEventKind = iota
	RuntimeEventAgentMessage
	RuntimeEventAgentThought
	RuntimeEventToolCall
	RuntimeEventToolUpdate
	RuntimeEventStatus
	RuntimeEventError
	RuntimeEventDone
)

type RuntimeEvent struct {
	Kind          RuntimeEventKind
	MessageID     string
	Text          string
	ToolCallID    string
	ToolName      string
	ArgumentsJSON []byte
	OutputJSON    []byte
	Status        string
	At            time.Time
}

type Runtime interface {
	Descriptor() RuntimeDescriptor
	Health(context.Context) RuntimeHealth
	Run(context.Context, Prompt, func(RuntimeEvent) error) error
	Cancel(context.Context, string) bool
}

type Session struct {
	ID                string
	ExternalSessionID string
	ProjectID         string
	WorkspaceID       string
	RuntimeID         string
	ProviderID        string
	ModelID           string
	TenantID          string
	ActorID           string
	Root              string
	Title             string
	Archived          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type SessionPatch struct {
	Title      *string
	Archived   *bool
	ProviderID *string
	ModelID    *string
}

type Project struct {
	ID             string
	WorkspaceID    string
	TenantID       string
	Root           string
	RepoRoot       string
	Name           string
	IconColor      string
	IconOverride   string
	StartupCommand string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ProjectPatch struct {
	Name           *string
	IconColor      *string
	IconOverride   *string
	StartupCommand *string
}

type Event struct {
	Sequence          uint64
	At                time.Time
	Type              string
	WorkspaceID       string
	SessionID         string
	RuntimeID         string
	ExternalSessionID string
	PayloadJSON       []byte
}

type Message struct {
	ID        string
	SessionID string
	Role      string
	Kind      string
	Text      string
	DataJSON  []byte
	CreatedAt time.Time
}

type ToolReference struct {
	MessageID string
	CallID    string
}

type PermissionDecision string

const (
	PermissionDecisionOnce   PermissionDecision = "once"
	PermissionDecisionAlways PermissionDecision = "always"
	PermissionDecisionReject PermissionDecision = "reject"
)

type PermissionRequest struct {
	ID           string
	SessionID    string
	ProjectID    string
	TenantID     string
	Permission   string
	Patterns     []string
	MetadataJSON []byte
	Always       []string
	Tool         *ToolReference
	Decision     PermissionDecision
	CreatedAt    time.Time
	ResolvedAt   time.Time
}

type QuestionOption struct {
	Label       string
	Description string
}

type Question struct {
	Question string
	Header   string
	Options  []QuestionOption
	Multiple bool
	Custom   bool
}

type QuestionRequest struct {
	ID         string
	SessionID  string
	ProjectID  string
	TenantID   string
	Questions  []Question
	Tool       *ToolReference
	Answers    [][]string
	Rejected   bool
	CreatedAt  time.Time
	ResolvedAt time.Time
}

type Workspace struct {
	ID        string
	TenantID  string
	NodeID    string
	Root      string
	RepoRoot  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type FileEntry struct {
	Name       string
	Path       string
	Directory  bool
	Size       int64
	ModifiedAt time.Time
}

// FileContent is the canonical workspace-file snapshot. Revision is a stable
// content digest used by editors for optimistic concurrency; it is deliberately
// independent of timestamps and inode identity.
type FileContent struct {
	Data       []byte
	Revision   string
	Size       int64
	ModifiedAt time.Time
}

type WriteFileOptions struct {
	ExpectedRevision     string
	RequireRevisionMatch bool
}

type MovePathOptions struct {
	Overwrite            bool
	ExpectedRevision     string
	RequireRevisionMatch bool
}

type DeletePathOptions struct {
	Recursive            bool
	ExpectedRevision     string
	RequireRevisionMatch bool
}

type ArchiveImportOptions struct {
	StripSingleRoot bool
}

type ArchiveImportResult struct {
	Files int64
	Dirs  int64
	Bytes int64
}

type FileSearchKind string

const (
	FileSearchFiles       FileSearchKind = "file"
	FileSearchDirectories FileSearchKind = "directory"
	FileSearchAny         FileSearchKind = "any"
)

type FileChangeKind string

const (
	FileChangeAdd    FileChangeKind = "add"
	FileChangeChange FileChangeKind = "change"
	FileChangeUnlink FileChangeKind = "unlink"
	FileChangeResync FileChangeKind = "resync"
)

type FileChange struct {
	Sequence uint64
	Path     string
	Kind     FileChangeKind
}

type SearchMatch struct {
	Path string
	Line int32
	Text string
}

type GitFileStatus struct {
	Path   string
	Status string
}

type GitStatus struct {
	Branch        string
	DefaultBranch string
	Files         []GitFileStatus
}

type GitFileDiff struct {
	File      string
	Patch     string
	Additions int32
	Deletions int32
	Status    string
}

type Worktree struct {
	Path   string
	Branch string
	Head   string
}

type ProcessInfo struct {
	ID          string
	WorkspaceID string
	Kind        string
	ServerID    string
	Title       string
	Command     string
	CWD         string
	Status      string
	Cursor      uint64
	ExitCode    int32
	StartedAt   time.Time
	EndedAt     time.Time
}

type Shell struct {
	Path       string
	Name       string
	Acceptable bool
}

type ProcessChunk struct {
	ProcessID string
	Cursor    uint64
	Data      []byte
	Exited    bool
	ExitCode  int32
}

type ProcessSpec struct {
	WorkspaceID string
	Kind        string
	ServerID    string
	Title       string
	Command     string
	Args        []string
	CWD         string
	Env         map[string]string
}

type CommandSpec struct {
	WorkspaceID    string
	Command        string
	Args           []string
	CWD            string
	Env            map[string]string
	MaxOutputBytes int
}

type CommandResult struct {
	Output        []byte
	ExitCode      int32
	WorkspaceRoot string
}

// RuntimeCommand is a shell-free process specification. Command and arguments
// are always executed directly at the isolated workspace boundary.
type RuntimeCommand struct {
	Command string
	Args    []string
	Env     map[string]string
}

// DetectionEvidence is bounded, non-secret metadata explaining a runtime
// decision. File contents and project-controlled command output never belong
// here because this value crosses the public API boundary.
type DetectionEvidence struct {
	Detector string
	Path     string
	Reason   string
	Score    int32
}

type RuntimeServicePlan struct {
	ID             string
	Name           string
	WorkingDir     string
	Runtime        string
	RuntimeVersion string
	Framework      string
	PackageManager string
	Install        *RuntimeCommand
	Run            RuntimeCommand
	Preview        bool
	ExpectedPorts  []uint32
	DependsOn      []string
}

const (
	RuntimePlanSourceExplicit = "explicit"
	RuntimePlanSourceDetected = "detected"
)

// RuntimePlan is deterministic for the runtime-relevant manifests represented
// by Fingerprint. Source files do not invalidate it unless startup metadata
// changes.
type RuntimePlan struct {
	Version     int32
	WorkspaceID string
	Fingerprint string
	Source      string
	Primary     string
	Services    []RuntimeServicePlan
	Evidence    []DetectionEvidence
	AnalyzedAt  time.Time
}

func (p RuntimePlan) PrimaryService() (RuntimeServicePlan, bool) {
	for _, service := range p.Services {
		if service.ID == p.Primary {
			return service, true
		}
	}
	return RuntimeServicePlan{}, false
}

type PreviewProcessRef struct {
	ServiceID string
	ProcessID string
	Port      uint32
	Status    string
}

// PreviewInstance is the public state of a Go-owned development server.
// RuntimeURL intentionally remains internal to the application service.
type PreviewInstance struct {
	ID              string
	WorkspaceID     string
	ProcessID       string
	ServiceID       string
	Framework       string
	PlanFingerprint string
	Port            uint32
	Status          string
	PublicURL       string
	Processes       []PreviewProcessRef
	StartedAt       time.Time
	UpdatedAt       time.Time
	LastError       string
}

type LSPServer struct {
	ID        string
	Name      string
	Command   string
	Args      []string
	Available bool
	Version   string
}

type MCPServer struct {
	ID          string
	WorkspaceID string
	Name        string
	Transport   string
	Command     string
	Args        []string
	URL         string
	Enabled     bool
}

func (server MCPServer) Validate() error {
	if err := ValidateMCPIdentity(server.WorkspaceID, server.ID); err != nil {
		return err
	}
	if server.Name == "" || len(server.Name) > 128 || strings.ContainsAny(server.Name, "\r\n\x00") {
		return fmt.Errorf("%w: invalid MCP name", ErrInvalid)
	}
	if len(server.Args) > 256 || len(server.Command) > 16*1024 || strings.ContainsAny(server.Command, "\r\n\x00") {
		return fmt.Errorf("%w: invalid MCP command", ErrInvalid)
	}
	for _, argument := range server.Args {
		if len(argument) > 16*1024 || strings.ContainsAny(argument, "\r\n\x00") {
			return fmt.Errorf("%w: invalid MCP argument", ErrInvalid)
		}
	}
	switch server.Transport {
	case "stdio":
		if strings.TrimSpace(server.Command) == "" || server.URL != "" {
			return fmt.Errorf("%w: stdio MCP requires a command and no URL", ErrInvalid)
		}
	case "sse", "streamable-http":
		if server.Command != "" || len(server.URL) > 2048 {
			return fmt.Errorf("%w: remote MCP requires a URL and no command", ErrInvalid)
		}
		parsed, err := url.ParseRequestURI(server.URL)
		if err != nil || parsed == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%w: invalid MCP URL", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported MCP transport", ErrInvalid)
	}
	return nil
}

func ValidateMCPIdentity(workspaceID, serverID string) error {
	if workspaceID == "" || len(workspaceID) > 256 || strings.ContainsAny(workspaceID, "\r\n\x00") || serverID == "" || len(serverID) > 128 || strings.ContainsAny(serverID, "\r\n\x00") {
		return fmt.Errorf("%w: invalid MCP identity", ErrInvalid)
	}
	return nil
}

type Provider struct {
	ID         string   `json:"id"`
	SourceID   string   `json:"source_id,omitempty"`
	Name       string   `json:"name"`
	API        string   `json:"api,omitempty"`
	NPM        string   `json:"npm,omitempty"`
	Doc        string   `json:"doc,omitempty"`
	Env        []string `json:"env,omitempty"`
	ModelCount int      `json:"model_count"`
	Connected  bool     `json:"connected"`
	RuntimeID  string   `json:"runtime_id,omitempty"`
	RuntimeIDs []string `json:"runtime_ids,omitempty"`
}

type Model struct {
	ID                string                 `json:"id"`
	Name              string                 `json:"name"`
	Description       string                 `json:"description,omitempty"`
	Family            string                 `json:"family,omitempty"`
	Knowledge         string                 `json:"knowledge,omitempty"`
	ProviderID        string                 `json:"provider_id"`
	SourceProviderID  string                 `json:"source_provider_id,omitempty"`
	RuntimeID         string                 `json:"runtime_id,omitempty"`
	RuntimeIDs        []string               `json:"runtime_ids,omitempty"`
	Ready             bool                   `json:"ready"`
	Free              bool                   `json:"free,omitempty"`
	UnavailableReason string                 `json:"unavailable_reason,omitempty"`
	Status            string                 `json:"status,omitempty"`
	Mode              string                 `json:"mode,omitempty"`
	ReleaseDate       string                 `json:"release_date,omitempty"`
	LastUpdated       string                 `json:"last_updated,omitempty"`
	Attachment        bool                   `json:"attachment"`
	Reasoning         bool                   `json:"reasoning"`
	Temperature       bool                   `json:"temperature"`
	ToolCall          bool                   `json:"tool_call"`
	StructuredOutput  bool                   `json:"structured_output"`
	OpenWeights       bool                   `json:"open_weights"`
	Interleaved       any                    `json:"interleaved,omitempty"`
	ReasoningOptions  []ModelReasoningOption `json:"reasoning_options,omitempty"`
	Modalities        ModelModalities        `json:"modalities,omitempty"`
	Cost              *ModelCost             `json:"cost,omitempty"`
	Provider          *ModelProviderOverride `json:"provider,omitempty"`
	Experimental      *ModelExperimental     `json:"experimental,omitempty"`
	Options           map[string]any         `json:"options,omitempty"`
	Headers           map[string]string      `json:"headers,omitempty"`
	Limits            ModelLimits            `json:"limits,omitempty"`
}

type ModelLimits struct {
	Context int64 `json:"context,omitempty"`
	Input   int64 `json:"input,omitempty"`
	Output  int64 `json:"output,omitempty"`
}

type ModelReasoningOption struct {
	Type   string    `json:"type"`
	Values []*string `json:"values,omitempty"`
	Min    *float64  `json:"min,omitempty"`
	Max    *float64  `json:"max,omitempty"`
}

type ModelModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type ModelCost struct {
	Input           float64            `json:"input"`
	Output          float64            `json:"output"`
	CacheRead       *float64           `json:"cache_read,omitempty"`
	CacheWrite      *float64           `json:"cache_write,omitempty"`
	InputAudio      *float64           `json:"input_audio,omitempty"`
	OutputAudio     *float64           `json:"output_audio,omitempty"`
	Reasoning       *float64           `json:"reasoning,omitempty"`
	Tiers           []ModelCostTier    `json:"tiers,omitempty"`
	ContextOver200K *ModelExtendedCost `json:"context_over_200k,omitempty"`
}

type ModelCostTier struct {
	Input       float64       `json:"input"`
	Output      float64       `json:"output"`
	CacheRead   *float64      `json:"cache_read,omitempty"`
	CacheWrite  *float64      `json:"cache_write,omitempty"`
	InputAudio  *float64      `json:"input_audio,omitempty"`
	OutputAudio *float64      `json:"output_audio,omitempty"`
	Reasoning   *float64      `json:"reasoning,omitempty"`
	Tier        ModelCostBand `json:"tier"`
}

type ModelCostBand struct {
	Type string  `json:"type"`
	Size float64 `json:"size"`
}

type ModelExtendedCost struct {
	Input       float64  `json:"input"`
	Output      float64  `json:"output"`
	CacheRead   *float64 `json:"cache_read,omitempty"`
	CacheWrite  *float64 `json:"cache_write,omitempty"`
	InputAudio  *float64 `json:"input_audio,omitempty"`
	OutputAudio *float64 `json:"output_audio,omitempty"`
	Reasoning   *float64 `json:"reasoning,omitempty"`
}

type ModelProviderOverride struct {
	NPM string `json:"npm,omitempty"`
	API string `json:"api,omitempty"`
}

type ModelExperimental struct {
	Modes map[string]ModelExperimentalMode `json:"modes,omitempty"`
}

type ModelExperimentalMode struct {
	Cost     *ModelCost                 `json:"cost,omitempty"`
	Provider *ModelExperimentalProvider `json:"provider,omitempty"`
}

type ModelExperimentalProvider struct {
	Body    map[string]any    `json:"body,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type VoiceAdmission struct {
	TenantID      string
	ActorID       string
	SubjectKey    string
	WorkspaceID   string
	WorkspaceRoot string
	Permissions   []string
	Locale        string
	Voice         string
	ExpiresAt     time.Time
}

type VoiceLease struct {
	Token     string
	SessionID string
	Admission VoiceAdmission
	ExpiresAt time.Time
}
