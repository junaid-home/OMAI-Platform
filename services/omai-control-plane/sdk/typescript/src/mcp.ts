import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import { create } from "@bufbuild/protobuf"
import { MCPServerInfoSchema, type MCPServerInfo, type MCPService } from "./gen/uab/v1/native_pb.js"
import { OMAIError } from "./errors.js"

export type OMAIMCPTransport = "stdio" | "sse" | "streamable-http"

export interface OMAIMCPServer {
  readonly id: string
  readonly workspaceId: string
  readonly name: string
  readonly transport: OMAIMCPTransport
  readonly command: string
  readonly args: readonly string[]
  readonly url: string
  readonly enabled: boolean
}

export interface OMAIMCPServerInput {
  readonly id: string
  readonly name: string
  readonly transport: OMAIMCPTransport
  readonly command?: string
  readonly args?: readonly string[]
  readonly url?: string
  readonly enabled?: boolean
}

export interface OMAIMCP {
  list(workspaceId: string, options?: CallOptions): Promise<readonly OMAIMCPServer[]>
  upsert(workspaceId: string, input: OMAIMCPServerInput, options?: CallOptions): Promise<OMAIMCPServer>
  delete(workspaceId: string, serverId: string, options?: CallOptions): Promise<boolean>
}

export function createMCP(client: Client<typeof MCPService>): OMAIMCP {
  return Object.freeze({
    async list(workspaceId: string, options?: CallOptions) {
      const response = await client.list({ workspaceId: checkedID(workspaceId, "Workspace ID") }, options)
      return response.servers.map(server)
    },
    async upsert(workspaceId: string, input: OMAIMCPServerInput, options?: CallOptions) {
      const value = checkedInput(input)
      const response = await client.upsert(
        { workspaceId: checkedID(workspaceId, "Workspace ID"), server: value },
        options,
      )
      return server(required(response.server, "MCP server"))
    },
    async delete(workspaceId: string, serverId: string, options?: CallOptions) {
      const response = await client.delete(
        { workspaceId: checkedID(workspaceId, "Workspace ID"), serverId: checkedID(serverId, "MCP server ID") },
        options,
      )
      return response.deleted
    },
  })
}

function checkedInput(input: OMAIMCPServerInput): MCPServerInfo {
  const transport = checkedTransport(input.transport)
  const command = checkedOptional(input.command ?? "", "MCP command", 16 * 1024)
  const url = checkedURL(input.url ?? "", transport)
  if (transport === "stdio" && command === "") throw new TypeError("A stdio MCP server requires a command")
  if (transport !== "stdio" && url === "") throw new TypeError("A remote MCP server requires a URL")
  const args = [...(input.args ?? [])].map((value) => checkedOptional(value, "MCP argument", 16 * 1024))
  if (args.length > 256) throw new RangeError("An MCP command cannot contain more than 256 arguments")
  return create(MCPServerInfoSchema, {
    id: checkedID(input.id, "MCP server ID"),
    workspaceId: "",
    name: checkedID(input.name, "MCP server name"),
    transport,
    command,
    args,
    url,
    enabled: input.enabled ?? false,
  })
}

function server(value: MCPServerInfo): OMAIMCPServer {
  return Object.freeze({
    id: value.id,
    workspaceId: value.workspaceId,
    name: value.name,
    transport: checkedTransport(value.transport),
    command: value.command,
    args: Object.freeze([...value.args]),
    url: value.url,
    enabled: value.enabled,
  })
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function checkedID(value: string, label: string): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > 128 || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError(`${label} is invalid`)
  }
  return trimmed
}

function checkedOptional(value: string, label: string, limit: number): string {
  const trimmed = value.trim()
  if (trimmed.length > limit || /[\0\r\n]/u.test(trimmed)) throw new TypeError(`${label} is invalid`)
  return trimmed
}

function checkedTransport(value: string): OMAIMCPTransport {
  if (value !== "stdio" && value !== "sse" && value !== "streamable-http") {
    throw new TypeError("Unsupported MCP transport")
  }
  return value
}

function checkedURL(value: string, transport: OMAIMCPTransport): string {
  const trimmed = checkedOptional(value, "MCP URL", 2_048)
  if (transport === "stdio" && trimmed === "") return ""
  if (trimmed === "") return ""
  const parsed = new URL(trimmed)
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") throw new TypeError("MCP URL must use HTTPS or HTTP")
  if (parsed.username !== "" || parsed.password !== "" || parsed.hash !== "") {
    throw new TypeError("MCP URL cannot contain credentials or a fragment")
  }
  return parsed.toString()
}
