import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import type { PreviewInstance, PreviewLogChunk, PreviewRuntimePlan, PreviewService } from "./gen/uab/v1/preview_pb.js"
import { OMAIError } from "./errors.js"

export interface OMAIPreview {
  analyze(root: string, options?: CallOptions): Promise<PreviewRuntimePlan>
  start(root: string, options?: CallOptions): Promise<PreviewInstance>
  restart(root: string, options?: CallOptions): Promise<PreviewInstance>
  get(workspaceId: string, options?: CallOptions): Promise<PreviewInstance>
  stop(workspaceId: string, options?: CallOptions): Promise<boolean>
  watchLogs(workspaceId: string, cursor?: bigint, options?: CallOptions): AsyncIterable<PreviewLogChunk>
}

export function createPreview(client: Client<typeof PreviewService>): OMAIPreview {
  return Object.freeze({
    async analyze(root: string, options?: CallOptions): Promise<PreviewRuntimePlan> {
      const response = await client.analyze({ root: checkedText(root, "Preview root", 16 * 1024) }, options)
      return required(response.plan, "runtime plan")
    },
    async start(root: string, options?: CallOptions): Promise<PreviewInstance> {
      const response = await client.start(
        { root: checkedText(root, "Preview root", 16 * 1024) },
        previewStartOptions(options),
      )
      return required(response.preview, "preview instance")
    },
    async restart(root: string, options?: CallOptions): Promise<PreviewInstance> {
      const response = await client.restart(
        { root: checkedText(root, "Preview root", 16 * 1024) },
        previewStartOptions(options),
      )
      return required(response.preview, "preview instance")
    },
    async get(workspaceId: string, options?: CallOptions): Promise<PreviewInstance> {
      const response = await client.get({ workspaceId: checkedText(workspaceId, "Workspace ID", 256) }, options)
      return required(response.preview, "preview instance")
    },
    async stop(workspaceId: string, options?: CallOptions): Promise<boolean> {
      return (await client.stop({ workspaceId: checkedText(workspaceId, "Workspace ID", 256) }, options)).stopped
    },
    watchLogs(workspaceId: string, cursor = 0n, options?: CallOptions): AsyncIterable<PreviewLogChunk> {
      if (cursor < 0n) {
        throw new RangeError("Preview log cursor cannot be negative")
      }
      return client.watchLogs({ workspaceId: checkedText(workspaceId, "Workspace ID", 256), cursor }, options)
    },
  })
}

function previewStartOptions(options: CallOptions | undefined): CallOptions {
  return { ...options, timeoutMs: options?.timeoutMs ?? 360_000 }
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) {
    throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  }
  return value
}

function checkedText(value: string, label: string, limit: number): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > limit || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError(`${label} is invalid`)
  }
  return trimmed
}
