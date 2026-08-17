import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import type { TerminalChunk, TerminalInfo, TerminalService } from "./gen/uab/v1/native_pb.js"
import { OMAIError } from "./errors.js"

export interface OMAIShell {
  readonly path: string
  readonly name: string
  readonly acceptable: boolean
}

export interface OMAITerminalCreateInput {
  readonly workspaceId: string
  readonly command?: string
  readonly args?: readonly string[]
  readonly cwd?: string
  readonly env?: Readonly<Record<string, string>>
}

export interface OMAITerminals {
  shells(options?: CallOptions): Promise<readonly OMAIShell[]>
  create(input: OMAITerminalCreateInput, options?: CallOptions): Promise<TerminalInfo>
  list(workspaceId: string, options?: CallOptions): Promise<readonly TerminalInfo[]>
  write(terminalId: string, data: Uint8Array, options?: CallOptions): Promise<void>
  resize(terminalId: string, cols: number, rows: number, options?: CallOptions): Promise<void>
  remove(terminalId: string, options?: CallOptions): Promise<void>
  watch(terminalId: string, cursor?: bigint, options?: CallOptions): AsyncIterable<TerminalChunk>
}

export function createTerminals(client: Client<typeof TerminalService>): OMAITerminals {
  return Object.freeze({
    async shells(options?: CallOptions) {
      return (await client.listShells({}, options)).shells.map((shell) => ({
        path: shell.path,
        name: shell.name,
        acceptable: shell.acceptable,
      }))
    },
    async create(input: OMAITerminalCreateInput, options?: CallOptions) {
      const response = await client.create(
        {
          workspaceId: checkedID(input.workspaceId, "Workspace ID"),
          command: optionalText(input.command, "Terminal command", 4 * 1024),
          args: [...(input.args ?? [])].map((value) => checkedArgument(value)),
          cwd: optionalPath(input.cwd),
          env: Object.entries(input.env ?? {}).map(([key, value]) => ({
            key: checkedEnvironmentKey(key),
            value: checkedEnvironmentValue(value),
          })),
        },
        options,
      )
      return required(response.terminal, "terminal")
    },
    async list(workspaceId: string, options?: CallOptions) {
      return (await client.list({ workspaceId: checkedID(workspaceId, "Workspace ID") }, options)).terminals
    },
    async write(terminalId: string, data: Uint8Array, options?: CallOptions) {
      if (!(data instanceof Uint8Array) || data.length === 0 || data.length > 64 * 1024) {
        throw new TypeError("Terminal input must contain between 1 and 65536 bytes")
      }
      await client.write({ terminalId: checkedID(terminalId, "Terminal ID"), data }, options)
    },
    async resize(terminalId: string, cols: number, rows: number, options?: CallOptions) {
      await client.resize(
        {
          terminalId: checkedID(terminalId, "Terminal ID"),
          cols: checkedDimension(cols),
          rows: checkedDimension(rows),
        },
        options,
      )
    },
    async remove(terminalId: string, options?: CallOptions) {
      await client.remove({ terminalId: checkedID(terminalId, "Terminal ID") }, options)
    },
    watch(terminalId: string, cursor = 0n, options?: CallOptions) {
      if (cursor < 0n) throw new RangeError("Terminal cursor cannot be negative")
      return client.watch({ terminalId: checkedID(terminalId, "Terminal ID"), cursor }, options)
    },
  })
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function checkedID(value: string, label: string): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > 256 || /[\0\r\n]/u.test(trimmed))
    throw new TypeError(`${label} is invalid`)
  return trimmed
}

function optionalText(value: string | undefined, label: string, limit: number): string {
  if (value === undefined || value === "") return ""
  if (value.length > limit || /[\0\r\n]/u.test(value)) throw new TypeError(`${label} is invalid`)
  return value
}

function checkedArgument(value: string): string {
  if (value.length > 16 * 1024 || value.includes("\0")) throw new TypeError("Terminal argument is invalid")
  return value
}

function optionalPath(value: string | undefined): string {
  if (value === undefined || value === "") return ""
  if (value.length > 16 * 1024 || /[\0\r\n]/u.test(value)) throw new TypeError("Terminal working directory is invalid")
  return value
}

function checkedEnvironmentKey(value: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/u.test(value)) throw new TypeError("Terminal environment key is invalid")
  return value
}

function checkedEnvironmentValue(value: string): string {
  if (value.length > 64 * 1024 || value.includes("\0")) throw new TypeError("Terminal environment value is invalid")
  return value
}

function checkedDimension(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > 1000)
    throw new RangeError("Terminal dimension must be between 1 and 1000")
  return value
}
