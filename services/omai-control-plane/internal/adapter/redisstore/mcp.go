package redisstore

import (
	"context"
	"sort"

	"github.com/omai/backend/internal/domain"
	"github.com/redis/go-redis/v9"
)

type MCP struct {
	client redis.UniversalClient
	prefix string
}

func NewMCP(client redis.UniversalClient, prefix string) *MCP {
	return &MCP{client: client, prefix: platformPrefix(prefix)}
}

func (m *MCP) List(ctx context.Context, principal domain.Principal, workspaceID string) ([]domain.MCPServer, error) {
	values, err := m.client.HVals(ctx, m.key(principal.TenantID, workspaceID)).Result()
	if err != nil {
		return nil, redisStoreError("list MCP servers", err)
	}
	result := make([]domain.MCPServer, 0, len(values))
	for _, raw := range values {
		server, err := decodeValue[domain.MCPServer](raw)
		if err != nil {
			return nil, err
		}
		server.Args = append([]string(nil), server.Args...)
		result = append(result, server)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (m *MCP) Upsert(ctx context.Context, principal domain.Principal, server domain.MCPServer) (domain.MCPServer, error) {
	if err := server.Validate(); err != nil {
		return domain.MCPServer{}, err
	}
	server.Args = append([]string(nil), server.Args...)
	raw, err := encodeValue(server)
	if err != nil {
		return domain.MCPServer{}, err
	}
	if err := m.client.HSet(ctx, m.key(principal.TenantID, server.WorkspaceID), server.ID, raw).Err(); err != nil {
		return domain.MCPServer{}, redisStoreError("upsert MCP server", err)
	}
	return server, nil
}

func (m *MCP) Delete(ctx context.Context, principal domain.Principal, workspaceID, serverID string) (bool, error) {
	if err := domain.ValidateMCPIdentity(workspaceID, serverID); err != nil {
		return false, err
	}
	deleted, err := m.client.HDel(ctx, m.key(principal.TenantID, workspaceID), serverID).Result()
	if err != nil {
		return false, redisStoreError("delete MCP server", err)
	}
	return deleted > 0, nil
}

func (m *MCP) key(tenant, workspaceID string) string {
	return redisKey(m.prefix, "mcp", tenant, workspaceID)
}
