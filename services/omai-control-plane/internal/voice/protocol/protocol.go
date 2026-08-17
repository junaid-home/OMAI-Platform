package protocol

import "encoding/json"

type ClientMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Confirmed bool            `json:"confirmed,omitempty"`
	Success   bool            `json:"success,omitempty"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}
type ServerMessage struct {
	Type             string          `json:"type"`
	SessionID        string          `json:"session_id,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	Model            string          `json:"model,omitempty"`
	RegistryETag     string          `json:"registry_etag,omitempty"`
	InputSampleRate  uint32          `json:"input_sample_rate_hz,omitempty"`
	OutputSampleRate uint32          `json:"output_sample_rate_hz,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	Tool             string          `json:"tool,omitempty"`
	Action           string          `json:"action,omitempty"`
	TimeoutMS        uint32          `json:"timeout_ms,omitempty"`
	Message          string          `json:"message,omitempty"`
	Role             string          `json:"role,omitempty"`
	Transcript       string          `json:"transcript,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
}
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}
type ToolResult struct {
	ID       string
	Name     string
	Response map[string]any
}
