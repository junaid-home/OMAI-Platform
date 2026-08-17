import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import type { LSPChunk, LSPInstanceInfo, LSPService } from "./gen/uab/v1/native_pb.js"
import { OMAIError } from "./errors.js"

export interface OMAILanguageServer {
  readonly id: string
  readonly name: string
  readonly command: string
  readonly available: boolean
  readonly version: string
}

export interface OMAILSPInstance {
  readonly id: string
  readonly workspaceId: string
  readonly serverId: string
  readonly command: string
  readonly status: string
  readonly cursor: bigint
  readonly exitCode: number
  readonly startedUnixMillis: bigint
  readonly endedUnixMillis: bigint
}

export interface OMAILSP {
  servers(workspaceId: string, options?: CallOptions): Promise<readonly OMAILanguageServer[]>
  start(workspaceId: string, serverId: string, options?: CallOptions): Promise<OMAILSPInstance>
  instances(workspaceId: string, options?: CallOptions): Promise<readonly OMAILSPInstance[]>
  write(instanceId: string, data: Uint8Array, options?: CallOptions): Promise<void>
  stop(instanceId: string, options?: CallOptions): Promise<void>
  watch(instanceId: string, cursor?: bigint, options?: CallOptions): AsyncIterable<LSPChunk>
}

export function createLSP(client: Client<typeof LSPService>): OMAILSP {
  return Object.freeze({
    async servers(workspaceId: string, options?: CallOptions) {
      const response = await client.list({ workspaceId: checkedID(workspaceId, "Workspace ID") }, options)
      return response.servers.map((server) => ({
        id: server.id,
        name: server.name,
        command: server.command,
        available: server.available,
        version: server.version,
      }))
    },
    async start(workspaceId: string, serverId: string, options?: CallOptions) {
      const response = await client.start(
        { workspaceId: checkedID(workspaceId, "Workspace ID"), serverId: checkedID(serverId, "LSP server ID") },
        options,
      )
      return instance(required(response.instance, "LSP instance"))
    },
    async instances(workspaceId: string, options?: CallOptions) {
      const response = await client.listInstances({ workspaceId: checkedID(workspaceId, "Workspace ID") }, options)
      return response.instances.map(instance)
    },
    async write(instanceId: string, data: Uint8Array, options?: CallOptions) {
      if (!(data instanceof Uint8Array) || data.length === 0 || data.length > 4 * 1024 * 1024) {
        throw new TypeError("LSP input must contain between 1 and 4194304 bytes")
      }
      await client.write({ instanceId: checkedID(instanceId, "LSP instance ID"), data }, options)
    },
    async stop(instanceId: string, options?: CallOptions) {
      await client.stop({ instanceId: checkedID(instanceId, "LSP instance ID") }, options)
    },
    watch(instanceId: string, cursor = 0n, options?: CallOptions) {
      if (cursor < 0n) throw new RangeError("LSP cursor cannot be negative")
      return client.watch({ instanceId: checkedID(instanceId, "LSP instance ID"), cursor }, options)
    },
  })
}

function instance(value: LSPInstanceInfo): OMAILSPInstance {
  return {
    id: value.id,
    workspaceId: value.workspaceId,
    serverId: value.serverId,
    command: value.command,
    status: value.status,
    cursor: value.cursor,
    exitCode: value.exitCode,
    startedUnixMillis: value.startedUnixMillis,
    endedUnixMillis: value.endedUnixMillis,
  }
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function checkedID(value: string, label: string): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > 256 || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError(`${label} is invalid`)
  }
  return trimmed
}
