// The legacy OpenCode PTY WebSocket adapter was removed after the terminal
// surface moved to the Go-owned ConnectRPC stream. This file intentionally
// remains as a migration tombstone so packaging overlays cannot restore the
// old endpoint without producing a source-boundary diff.
export {}
