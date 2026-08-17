package lsp

import (
	"context"
	"os/exec"
	"sort"

	"github.com/omai/backend/internal/domain"
)

type Registry struct{ servers map[string]domain.LSPServer }

func NewRegistry() *Registry {
	definitions := []domain.LSPServer{
		{ID: "go", Name: "Go", Command: "gopls"},
		{ID: "typescript", Name: "TypeScript / JavaScript", Command: "typescript-language-server", Args: []string{"--stdio"}},
		{ID: "rust", Name: "Rust", Command: "rust-analyzer"},
		{ID: "python", Name: "Python", Command: "pyright-langserver", Args: []string{"--stdio"}},
		{ID: "clang", Name: "C / C++", Command: "clangd"},
		{ID: "html", Name: "HTML", Command: "vscode-html-language-server", Args: []string{"--stdio"}},
		{ID: "css", Name: "CSS", Command: "vscode-css-language-server", Args: []string{"--stdio"}},
		{ID: "json", Name: "JSON", Command: "vscode-json-language-server", Args: []string{"--stdio"}},
	}
	servers := make(map[string]domain.LSPServer, len(definitions))
	for _, server := range definitions {
		path, err := exec.LookPath(server.Command)
		server.Available = err == nil
		if err == nil {
			server.Command = path
		}
		servers[server.ID] = server
	}
	return &Registry{servers: servers}
}

func (r *Registry) List(context.Context) []domain.LSPServer {
	result := make([]domain.LSPServer, 0, len(r.servers))
	for _, server := range r.servers {
		server.Args = append([]string(nil), server.Args...)
		result = append(result, server)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func (r *Registry) Get(id string) (domain.LSPServer, bool) {
	server, ok := r.servers[id]
	server.Args = append([]string(nil), server.Args...)
	return server, ok
}
