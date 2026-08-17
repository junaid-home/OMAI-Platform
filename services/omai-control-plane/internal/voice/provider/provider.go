package provider

import (
	"context"

	"github.com/omai/backend/internal/voice/protocol"
)

type Transcript struct {
	Role string
	Text string
}
type Event struct {
	Audio            [][]byte
	Transcripts      []Transcript
	TurnComplete     bool
	Interrupted      bool
	Calls            []protocol.ToolCall
	CancelledCallIDs []string
	Err              error
}
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}
type Session interface {
	Name() string
	Model() string
	InputSampleRate() uint32
	OutputSampleRate() uint32
	Events() <-chan Event
	SendAudio(context.Context, []byte) error
	SendToolResult(context.Context, protocol.ToolResult) error
	Interrupt(context.Context) error
	Close() error
}
type Factory interface {
	Connect(context.Context, []Tool, string) (Session, error)
}
