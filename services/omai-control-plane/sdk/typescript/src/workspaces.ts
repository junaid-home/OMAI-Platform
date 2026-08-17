import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import {
  FileChangeKind,
  FileSearchKind,
  type FileEntry,
  type SearchMatch,
  type WorkspaceInfo,
  type WorkspaceService,
} from "./gen/uab/v1/native_pb.js"
import { OMAIError } from "./errors.js"

export type OMAIFileContent = (
  | { readonly type: "text"; readonly content: string }
  | { readonly type: "binary"; readonly content: string; readonly encoding: "base64" }
) &
  OMAIFileMetadata & { readonly mimeType?: string }

export interface OMAIFileMetadata {
  readonly revision: string
  readonly size: number
  readonly modifiedUnixMillis: number
}

export interface OMAIFileChange {
  readonly sequence: bigint
  readonly path: string
  readonly kind: "add" | "change" | "unlink" | "resync"
}

export interface OMAIArchiveImportResult {
  readonly files: number
  readonly directories: number
  readonly uncompressedBytes: number
}

export interface OMAIWorkspaces {
  resolve(root: string, options?: CallOptions): Promise<WorkspaceInfo>
  list(options?: CallOptions): Promise<readonly WorkspaceInfo[]>
  listFiles(workspaceId: string, path?: string, options?: CallOptions): Promise<readonly FileEntry[]>
  watchFiles(workspaceId: string, paths: readonly string[], options?: CallOptions): AsyncIterable<OMAIFileChange>
  createDirectory(workspaceId: string, path: string, options?: CallOptions): Promise<void>
  readFile(workspaceId: string, path: string, options?: CallOptions): Promise<OMAIFileContent>
  writeFile(
    workspaceId: string,
    path: string,
    data: Uint8Array,
    input?: { readonly expectedRevision?: string; readonly createOnly?: boolean },
    options?: CallOptions,
  ): Promise<OMAIFileMetadata>
  movePath(
    workspaceId: string,
    sourcePath: string,
    destinationPath: string,
    input?: { readonly overwrite?: boolean; readonly expectedRevision?: string },
    options?: CallOptions,
  ): Promise<void>
  deletePath(
    workspaceId: string,
    path: string,
    input?: { readonly recursive?: boolean; readonly expectedRevision?: string },
    options?: CallOptions,
  ): Promise<void>
  importArchive(
    workspaceId: string,
    data: Uint8Array,
    input?: { readonly stripSingleRoot?: boolean },
    options?: CallOptions,
  ): Promise<OMAIArchiveImportResult>
  exportArchive(workspaceId: string, options?: CallOptions): Promise<Uint8Array>
  searchFiles(
    workspaceId: string,
    query: string,
    input?: { readonly kind?: "file" | "directory" | "any"; readonly limit?: number },
    options?: CallOptions,
  ): Promise<readonly string[]>
  searchText(workspaceId: string, query: string, limit?: number, options?: CallOptions): Promise<readonly SearchMatch[]>
}

export function createWorkspaces(client: Client<typeof WorkspaceService>): OMAIWorkspaces {
  return Object.freeze({
    async resolve(root: string, options?: CallOptions) {
      const response = await client.resolveWorkspace({ root: checkedText(root, "Workspace root", 16 * 1024) }, options)
      return required(response.workspace, "workspace")
    },
    async list(options?: CallOptions) {
      return (await client.listWorkspaces({}, options)).workspaces
    },
    async listFiles(workspaceId: string, path = "", options?: CallOptions) {
      return (await client.listFiles({ workspaceId: checkedID(workspaceId), path: checkedPath(path, true) }, options))
        .entries
    },
    watchFiles(workspaceId: string, paths: readonly string[], options?: CallOptions) {
      const watched = checkedWatchPaths(paths)
      const stream = client.watchFiles({ workspaceId: checkedID(workspaceId), paths: watched }, options)
      return {
        async *[Symbol.asyncIterator]() {
          for await (const change of stream) {
            yield {
              sequence: change.sequence,
              path: change.kind === FileChangeKind.RESYNC ? "" : checkedPath(change.path, false),
              kind: fileChangeKind(change.kind),
            }
          }
        },
      }
    },
    async createDirectory(workspaceId: string, path: string, options?: CallOptions) {
      await client.createDirectory({ workspaceId: checkedID(workspaceId), path: checkedPath(path, false) }, options)
    },
    async readFile(workspaceId: string, path: string, options?: CallOptions) {
      const response = await client.readFile(
        { workspaceId: checkedID(workspaceId), path: checkedPath(path, false) },
        options,
      )
      return decodeFile(response.data, fileMetadata(response))
    },
    async writeFile(
      workspaceId: string,
      path: string,
      data: Uint8Array,
      input: { readonly expectedRevision?: string; readonly createOnly?: boolean } = {},
      options?: CallOptions,
    ) {
      if (!(data instanceof Uint8Array)) throw new TypeError("File data must be a Uint8Array")
      if (input.createOnly && input.expectedRevision !== undefined) {
        throw new TypeError("createOnly and expectedRevision cannot be combined")
      }
      const expectedRevision = input.expectedRevision === undefined ? "" : checkedRevision(input.expectedRevision)
      const response = await client.writeFile(
        {
          workspaceId: checkedID(workspaceId),
          path: checkedPath(path, false),
          data,
          expectedRevision,
          requireRevisionMatch: input.createOnly === true || input.expectedRevision !== undefined,
        },
        options,
      )
      return fileMetadata(response)
    },
    async movePath(
      workspaceId: string,
      sourcePath: string,
      destinationPath: string,
      input: { readonly overwrite?: boolean; readonly expectedRevision?: string } = {},
      options?: CallOptions,
    ) {
      await client.movePath(
        {
          workspaceId: checkedID(workspaceId),
          sourcePath: checkedPath(sourcePath, false),
          destinationPath: checkedPath(destinationPath, false),
          overwrite: input.overwrite === true,
          expectedRevision: input.expectedRevision === undefined ? "" : checkedRevision(input.expectedRevision),
          requireRevisionMatch: input.expectedRevision !== undefined,
        },
        options,
      )
    },
    async deletePath(
      workspaceId: string,
      path: string,
      input: { readonly recursive?: boolean; readonly expectedRevision?: string } = {},
      options?: CallOptions,
    ) {
      await client.deletePath(
        {
          workspaceId: checkedID(workspaceId),
          path: checkedPath(path, false),
          recursive: input.recursive === true,
          expectedRevision: input.expectedRevision === undefined ? "" : checkedRevision(input.expectedRevision),
          requireRevisionMatch: input.expectedRevision !== undefined,
        },
        options,
      )
    },
    async importArchive(
      workspaceId: string,
      data: Uint8Array,
      input: { readonly stripSingleRoot?: boolean } = {},
      options?: CallOptions,
    ) {
      if (!(data instanceof Uint8Array) || data.length === 0) throw new TypeError("ZIP archive data is invalid")
      const response = await client.importArchive(
        { workspaceId: checkedID(workspaceId), data, stripSingleRoot: input.stripSingleRoot === true },
        options,
      )
      return {
        files: checkedInteger(response.files, "Imported file count"),
        directories: checkedInteger(response.directories, "Imported directory count"),
        uncompressedBytes: checkedInteger(response.uncompressedBytes, "Imported byte count"),
      }
    },
    async exportArchive(workspaceId: string, options?: CallOptions) {
      const chunks: Uint8Array[] = []
      let total = 0
      for await (const chunk of client.exportArchive({ workspaceId: checkedID(workspaceId) }, options)) {
        if (chunk.data.length === 0) continue
        total += chunk.data.length
        if (!Number.isSafeInteger(total) || total > 2 * 1024 * 1024 * 1024) {
          throw new RangeError("Workspace archive exceeds the SDK download limit")
        }
        chunks.push(chunk.data)
      }
      const archive = new Uint8Array(total)
      let offset = 0
      for (const chunk of chunks) {
        archive.set(chunk, offset)
        offset += chunk.length
      }
      return archive
    },
    async searchFiles(
      workspaceId: string,
      query: string,
      input: { readonly kind?: "file" | "directory" | "any"; readonly limit?: number } = {},
      options?: CallOptions,
    ) {
      return (
        await client.searchFiles(
          {
            workspaceId: checkedID(workspaceId),
            query: checkedText(query, "Search query", 4 * 1024),
            kind: fileSearchKind(input.kind),
            limit: checkedLimit(input.limit ?? 100),
          },
          options,
        )
      ).paths
    },
    async searchText(workspaceId: string, query: string, limit = 100, options?: CallOptions) {
      return (
        await client.searchText(
          {
            workspaceId: checkedID(workspaceId),
            query: checkedText(query, "Search query", 4 * 1024),
            limit: checkedLimit(limit),
          },
          options,
        )
      ).matches
    },
  })
}

function fileSearchKind(value: "file" | "directory" | "any" | undefined): FileSearchKind {
  switch (value) {
    case "directory":
      return FileSearchKind.DIRECTORY
    case "any":
      return FileSearchKind.ANY
    default:
      return FileSearchKind.FILE
  }
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function checkedID(value: string): string {
  return checkedText(value, "Workspace ID", 256)
}

function checkedText(value: string, label: string, limit: number): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > limit || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError(`${label} is invalid`)
  }
  return trimmed
}

function checkedPath(value: string, allowEmpty: boolean): string {
  if (typeof value !== "string" || value.length > 16 * 1024 || /[\0\r\n]/u.test(value)) {
    throw new TypeError("Workspace path is invalid")
  }
  const normalized = value
    .replaceAll("\\", "/")
    .replace(/^\.\//u, "")
    .replace(/^\/+|\/+$/gu, "")
  if (!allowEmpty && normalized.length === 0) throw new TypeError("Workspace path is required")
  if (normalized.split("/").some((part) => part === "..")) throw new TypeError("Workspace path cannot escape its root")
  return normalized
}

function checkedLimit(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > 500)
    throw new RangeError("Search limit must be between 1 and 500")
  return value
}

function checkedWatchPaths(values: readonly string[]): string[] {
  if (!Array.isArray(values)) throw new TypeError("Workspace watch paths must be an array")
  const paths = [...new Set(values.map((value) => checkedPath(value, true)))].sort()
  if (paths.length < 1 || paths.length > 256) throw new RangeError("Workspace watch requires between 1 and 256 paths")
  return paths
}

function fileChangeKind(value: FileChangeKind): OMAIFileChange["kind"] {
  switch (value) {
    case FileChangeKind.ADD:
      return "add"
    case FileChangeKind.CHANGE:
      return "change"
    case FileChangeKind.UNLINK:
      return "unlink"
    default:
      return "resync"
  }
}

function decodeFile(data: Uint8Array, metadata: OMAIFileMetadata): OMAIFileContent {
  if (data.includes(0)) return { type: "binary", content: encodeBase64(data), encoding: "base64", ...metadata }
  try {
    return { type: "text", content: new TextDecoder("utf-8", { fatal: true }).decode(data), ...metadata }
  } catch {
    return { type: "binary", content: encodeBase64(data), encoding: "base64", ...metadata }
  }
}

function fileMetadata(value: {
  readonly revision: string
  readonly size: bigint
  readonly modifiedUnixMillis: bigint
}): OMAIFileMetadata {
  const revision = checkedRevision(value.revision)
  const size = checkedInteger(value.size, "File size")
  const modifiedUnixMillis = checkedInteger(value.modifiedUnixMillis, "File modification time")
  return { revision, size, modifiedUnixMillis }
}

function checkedRevision(value: string): string {
  if (!/^sha256:[0-9a-f]{64}$/u.test(value)) throw new TypeError("File revision is invalid")
  return value
}

function checkedInteger(value: bigint, label: string): number {
  const numeric = Number(value)
  if (!Number.isSafeInteger(numeric) || numeric < 0) throw new TypeError(`${label} is invalid`)
  return numeric
}

function encodeBase64(data: Uint8Array): string {
  let binary = ""
  const chunkSize = 32 * 1024
  for (let offset = 0; offset < data.length; offset += chunkSize) {
    binary += String.fromCharCode(...data.subarray(offset, Math.min(offset + chunkSize, data.length)))
  }
  return btoa(binary)
}
