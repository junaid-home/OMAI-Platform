import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { MCPService, type MCPServerInfo } from "../src/gen/uab/v1/native_pb.js"

describe("OMAI MCP facade", () => {
  it("owns durable configuration without claiming runtime connectivity", async () => {
    const stored = new Map<string, MCPServerInfo>()
    const transport = createRouterTransport((router) => {
      router.service(MCPService, {
        list: () => ({ servers: [...stored.values()] }),
        upsert: (request) => {
          const server = { ...request.server!, workspaceId: request.workspaceId }
          stored.set(server.id, server)
          return { server }
        },
        delete: (request) => ({ deleted: stored.delete(request.serverId) }),
      })
    })
    const mcp = createOMAIClientFromTransport(transport).mcp

    await expect(
      mcp.upsert("workspace-1", {
        id: "docs",
        name: "Docs",
        transport: "stdio",
        command: "mcp-docs",
        enabled: false,
      }),
    ).resolves.toMatchObject({ id: "docs", workspaceId: "workspace-1", enabled: false })
    await expect(mcp.list("workspace-1")).resolves.toHaveLength(1)
    await expect(mcp.delete("workspace-1", "docs")).resolves.toBe(true)
    await expect(mcp.delete("workspace-1", "docs")).resolves.toBe(false)
  })

  it("rejects incomplete commands and credential-bearing URLs", async () => {
    const transport = createRouterTransport((router) => router.service(MCPService, {}))
    const mcp = createOMAIClientFromTransport(transport).mcp

    await expect(mcp.upsert("workspace-1", { id: "bad", name: "Bad", transport: "stdio" })).rejects.toThrow(TypeError)
    await expect(
      mcp.upsert("workspace-1", {
        id: "remote",
        name: "Remote",
        transport: "streamable-http",
        url: "https://secret@example.com/mcp",
      }),
    ).rejects.toThrow(TypeError)
  })
})
