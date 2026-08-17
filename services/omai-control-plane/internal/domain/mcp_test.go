package domain

import "testing"

func TestMCPServerValidation(t *testing.T) {
	valid := []MCPServer{
		{ID: "local", WorkspaceID: "workspace", Name: "Local", Transport: "stdio", Command: "mcp-local"},
		{ID: "remote", WorkspaceID: "workspace", Name: "Remote", Transport: "streamable-http", URL: "https://example.com/mcp"},
	}
	for _, server := range valid {
		if err := server.Validate(); err != nil {
			t.Fatalf("valid server %#v: %v", server, err)
		}
	}
	invalid := []MCPServer{
		{ID: "local", WorkspaceID: "workspace", Name: "Local", Transport: "stdio"},
		{ID: "remote", WorkspaceID: "workspace", Name: "Remote", Transport: "sse", URL: "file:///secret"},
		{ID: "remote", WorkspaceID: "workspace", Name: "Remote", Transport: "sse", URL: "https://user@example.com/mcp"},
		{ID: "bad\nname", WorkspaceID: "workspace", Name: "Bad", Transport: "stdio", Command: "mcp"},
	}
	for _, server := range invalid {
		if err := server.Validate(); err == nil {
			t.Fatalf("invalid server was accepted: %#v", server)
		}
	}
}
