import type { McpStatus } from "@opencode-ai/sdk/v2/client"
import { omaiClient, resolveOMAIWorkspace } from "./client"

const runtimePending = "Configured by OMAI Go; the MCP execution adapter is not active yet"

export async function listOMAIMCP(directory: string): Promise<Record<string, McpStatus>> {
  const workspace = await resolveOMAIWorkspace(directory)
  const servers = await omaiClient.mcp.list(workspace.id)
  return Object.fromEntries(
    servers.map((server) => [
      server.name,
      server.enabled ? ({ status: "failed", error: runtimePending } as const) : ({ status: "disabled" } as const),
    ]),
  )
}

export async function addOMAIMCP(
  directory: string,
  input:
    | { readonly name: string; readonly type: "local"; readonly command: readonly string[] }
    | { readonly name: string; readonly type: "remote"; readonly url: string },
): Promise<void> {
  const workspace = await resolveOMAIWorkspace(directory)
  const name = input.name.trim()
  if (input.type === "local") {
    const [command, ...args] = input.command
    if (!command) throw new TypeError("A local MCP server requires a command")
    await omaiClient.mcp.upsert(workspace.id, {
      id: name,
      name,
      transport: "stdio",
      command,
      args,
      enabled: true,
    })
    return
  }
  await omaiClient.mcp.upsert(workspace.id, {
    id: name,
    name,
    transport: "streamable-http",
    url: input.url,
    enabled: true,
  })
}

export async function deleteOMAIMCP(directory: string, name: string): Promise<boolean> {
  const workspace = await resolveOMAIWorkspace(directory)
  const server = await findServer(workspace.id, name)
  return omaiClient.mcp.delete(workspace.id, server?.id ?? name)
}

export async function toggleOMAIMCP(directory: string, name: string): Promise<void> {
  const workspace = await resolveOMAIWorkspace(directory)
  const server = await findServer(workspace.id, name)
  if (!server) throw new Error(`MCP server ${name} was not found`)
  await omaiClient.mcp.upsert(workspace.id, {
    id: server.id,
    name: server.name,
    transport: server.transport,
    command: server.command,
    args: server.args,
    url: server.url,
    enabled: !server.enabled,
  })
}

async function findServer(workspaceId: string, name: string) {
  const servers = await omaiClient.mcp.list(workspaceId)
  return servers.find((server) => server.id === name || server.name === name)
}
