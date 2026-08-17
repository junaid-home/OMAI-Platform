package port

import (
	"context"
	"io"
	"time"

	"github.com/omai/backend/internal/domain"
)

type RuntimeRegistry interface {
	Get(string) (domain.Runtime, bool)
	List() []domain.Runtime
}

type SessionRepository interface {
	Resolve(context.Context, domain.Principal, string, string, string, string, string, string) (domain.Session, bool, error)
	Get(context.Context, domain.Principal, string) (domain.Session, error)
	Find(context.Context, domain.Principal, string, string) (domain.Session, error)
	List(context.Context, domain.Principal, string, bool) ([]domain.Session, error)
	Update(context.Context, domain.Principal, string, domain.SessionPatch) (domain.Session, error)
	Delete(context.Context, domain.Principal, string) error
	Touch(context.Context, domain.Principal, string) error
}

type ProjectRepository interface {
	Resolve(context.Context, domain.Principal, domain.Workspace, string) (domain.Project, bool, error)
	Get(context.Context, domain.Principal, string) (domain.Project, error)
	List(context.Context, domain.Principal) ([]domain.Project, error)
	Update(context.Context, domain.Principal, string, domain.ProjectPatch) (domain.Project, error)
}

type ConversationRepository interface {
	Append(context.Context, domain.Principal, domain.Message) error
	AppendText(context.Context, domain.Principal, string, string, string, string, string) error
	List(context.Context, domain.Principal, string) ([]domain.Message, error)
	DeleteSession(context.Context, domain.Principal, string) error
}

type EventRepository interface {
	Publish(context.Context, domain.Principal, domain.Event) (domain.Event, error)
	Subscribe(context.Context, domain.Principal, string, uint64) ([]domain.Event, <-chan domain.Event, func(), error)
	DeleteSession(context.Context, domain.Principal, string) error
}

type PermissionRepository interface {
	Create(context.Context, domain.Principal, domain.PermissionRequest) (domain.PermissionRequest, bool, error)
	ListPending(context.Context, domain.Principal, string, string) ([]domain.PermissionRequest, error)
	Respond(context.Context, domain.Principal, string, string, domain.PermissionDecision) (domain.PermissionRequest, bool, error)
	DeleteSession(context.Context, domain.Principal, string) error
}

type QuestionRepository interface {
	Create(context.Context, domain.Principal, domain.QuestionRequest) (domain.QuestionRequest, bool, error)
	ListPending(context.Context, domain.Principal, string, string) ([]domain.QuestionRequest, error)
	Reply(context.Context, domain.Principal, string, string, [][]string, bool) (domain.QuestionRequest, bool, error)
	DeleteSession(context.Context, domain.Principal, string) error
}

type TurnLeaseStore interface {
	Acquire(context.Context, domain.Principal, string, string, time.Duration) (bool, error)
	Renew(context.Context, domain.Principal, string, string, time.Duration) (bool, error)
	Release(context.Context, domain.Principal, string, string) error
	Active(context.Context, domain.Principal, string) (bool, error)
}

type WorkspaceRepository interface {
	Resolve(context.Context, domain.Principal, string) (domain.Workspace, error)
	Get(context.Context, domain.Principal, string) (domain.Workspace, error)
	List(context.Context, domain.Principal) ([]domain.Workspace, error)
	ListFiles(context.Context, domain.Principal, string, string) ([]domain.FileEntry, error)
	WatchFiles(context.Context, domain.Principal, string, []string) (<-chan domain.FileChange, error)
	CreateDirectory(context.Context, domain.Principal, string, string) error
	ReadFile(context.Context, domain.Principal, string, string) (domain.FileContent, error)
	WriteFile(context.Context, domain.Principal, string, string, []byte, domain.WriteFileOptions) (domain.FileContent, error)
	MovePath(context.Context, domain.Principal, string, string, string, domain.MovePathOptions) (domain.FileContent, error)
	DeletePath(context.Context, domain.Principal, string, string, domain.DeletePathOptions) error
	ImportArchive(context.Context, domain.Principal, string, []byte, domain.ArchiveImportOptions) (domain.ArchiveImportResult, error)
	ExportArchive(context.Context, domain.Principal, string) (io.ReadCloser, error)
	SearchFiles(context.Context, domain.Principal, string, string, domain.FileSearchKind, int) ([]string, error)
	SearchText(context.Context, domain.Principal, string, string, int) ([]domain.SearchMatch, error)
}

type GitRepository interface {
	Init(context.Context, domain.Principal, string) (domain.GitStatus, error)
	Status(context.Context, domain.Principal, string) (domain.GitStatus, error)
	Diff(context.Context, domain.Principal, string, string, bool) (string, error)
	DiffFiles(context.Context, domain.Principal, string, string, string, int) ([]domain.GitFileDiff, error)
	Stage(context.Context, domain.Principal, string, []string, bool) (domain.GitStatus, error)
	Unstage(context.Context, domain.Principal, string, []string, bool) (domain.GitStatus, error)
	Commit(context.Context, domain.Principal, string, string) (string, error)
	ListWorktrees(context.Context, domain.Principal, string) ([]domain.Worktree, error)
	CreateWorktree(context.Context, domain.Principal, string, string, string) (domain.Worktree, error)
	RemoveWorktree(context.Context, domain.Principal, string, string) error
	Merge(context.Context, domain.Principal, string, string) (string, bool, error)
}

type ProcessManager interface {
	ListShells(context.Context, domain.Principal) ([]domain.Shell, error)
	Start(context.Context, domain.Principal, domain.ProcessSpec) (domain.ProcessInfo, error)
	Get(context.Context, domain.Principal, string) (domain.ProcessInfo, error)
	List(context.Context, domain.Principal, string, string) ([]domain.ProcessInfo, error)
	Write(context.Context, domain.Principal, string, []byte) error
	Resize(context.Context, domain.Principal, string, uint32, uint32) error
	Stop(context.Context, domain.Principal, string) error
	Remove(context.Context, domain.Principal, string) error
	Watch(context.Context, domain.Principal, string, uint64) ([]domain.ProcessChunk, <-chan domain.ProcessChunk, func(), error)
	// AllocatePreviewPort reserves an executor-local port selection. The
	// process is still required to bind it; readiness is checked separately.
	AllocatePreviewPort(context.Context, domain.Principal, string) (uint32, error)
	// WaitPreviewPort returns only a listener owned by the preview process
	// group. This prevents routing to an unrelated service on a shared host.
	WaitPreviewPort(context.Context, domain.Principal, string, []uint32) (uint32, error)
}

type WorkspaceCommandRunner interface {
	Run(context.Context, domain.Principal, domain.CommandSpec) (domain.CommandResult, error)
}

type LSPRegistry interface {
	List(context.Context) []domain.LSPServer
	Get(string) (domain.LSPServer, bool)
}

type MCPRepository interface {
	List(context.Context, domain.Principal, string) ([]domain.MCPServer, error)
	Upsert(context.Context, domain.Principal, domain.MCPServer) (domain.MCPServer, error)
	Delete(context.Context, domain.Principal, string, string) (bool, error)
}

type PreviewGateway interface {
	Fetch(context.Context, string, string, map[string]string, io.Reader) (*PreviewResponse, error)
}

type ProjectDetector interface {
	Analyze(context.Context, domain.Workspace) (domain.RuntimePlan, error)
}

type PreviewPublisher interface {
	Publish(context.Context, string, string) (string, error)
	Unpublish(context.Context, string) error
}

type PreviewResponse struct {
	Status int
	Header map[string][]string
	Body   io.ReadCloser
}

type VoiceLeaseStore interface {
	Issue(context.Context, string, domain.VoiceAdmission) error
	Redeem(context.Context, string, string, int, time.Duration) (domain.VoiceLease, error)
	Get(context.Context, string) (domain.VoiceLease, error)
	Heartbeat(context.Context, string, time.Duration) (domain.VoiceLease, error)
	Release(context.Context, string) error
}

type VoiceDispatchState uint8

const (
	VoiceDispatchStarted VoiceDispatchState = iota + 1
	VoiceDispatchRunning
	VoiceDispatchCached
)

type VoiceDispatchRecord struct {
	Fingerprint string
	Result      []byte
}

// VoiceDispatchStore makes one model tool-call key atomic across gateway and
// control-plane retries. Production adapters must be shared by every replica.
type VoiceDispatchStore interface {
	Begin(context.Context, string, string, time.Duration) (VoiceDispatchRecord, VoiceDispatchState, error)
	Complete(context.Context, string, string, []byte, time.Duration) error
	Abort(context.Context, string, string) error
}

type ToolInvoker interface {
	Invoke(context.Context, domain.Principal, string, []byte) ([]byte, error)
}
