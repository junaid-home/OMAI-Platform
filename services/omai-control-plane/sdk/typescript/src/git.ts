import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import type { GitService, GitStatus, WorktreeInfo } from "./gen/uab/v1/native_pb.js"
import { OMAIError } from "./errors.js"

export interface OMAIGitFileStatus {
  readonly path: string
  readonly status: string
}

export interface OMAIGitStatus {
  readonly branch: string
  readonly defaultBranch: string
  readonly files: readonly OMAIGitFileStatus[]
}

export interface OMAIGitFileDiff {
  readonly file: string
  readonly patch: string
  readonly additions: number
  readonly deletions: number
  readonly status: "added" | "deleted" | "modified"
}

export interface OMAIWorktree {
  readonly path: string
  readonly branch: string
  readonly head: string
}

export interface OMAIGit {
  init(workspaceId: string, options?: CallOptions): Promise<OMAIGitStatus>
  status(workspaceId: string, options?: CallOptions): Promise<OMAIGitStatus>
  diff(workspaceId: string, path?: string, staged?: boolean, options?: CallOptions): Promise<string>
  diffFiles(
    workspaceId: string,
    mode: "git" | "branch",
    path?: string,
    contextLines?: number,
    options?: CallOptions,
  ): Promise<readonly OMAIGitFileDiff[]>
  stage(workspaceId: string, paths?: readonly string[], all?: boolean, options?: CallOptions): Promise<OMAIGitStatus>
  unstage(workspaceId: string, paths?: readonly string[], all?: boolean, options?: CallOptions): Promise<OMAIGitStatus>
  commit(workspaceId: string, message: string, options?: CallOptions): Promise<string>
  listWorktrees(workspaceId: string, options?: CallOptions): Promise<readonly OMAIWorktree[]>
  createWorktree(workspaceId: string, branch: string, base?: string, options?: CallOptions): Promise<OMAIWorktree>
  removeWorktree(workspaceId: string, path: string, options?: CallOptions): Promise<void>
  merge(workspaceId: string, branch: string, options?: CallOptions): Promise<{ commit: string; fastForward: boolean }>
}

export function createGit(client: Client<typeof GitService>): OMAIGit {
  return Object.freeze({
    async init(workspaceId: string, options?: CallOptions) {
      return gitStatus(
        required((await client.init({ workspaceId: checkedID(workspaceId) }, options)).status, "Git status"),
      )
    },
    async status(workspaceId: string, options?: CallOptions) {
      return gitStatus(
        required((await client.status({ workspaceId: checkedID(workspaceId) }, options)).status, "Git status"),
      )
    },
    async diff(workspaceId: string, path = "", staged = false, options?: CallOptions) {
      return (
        await client.diff({ workspaceId: checkedID(workspaceId), path: checkedPath(path, true), staged }, options)
      ).diff
    },
    async diffFiles(workspaceId: string, mode: "git" | "branch", path = "", contextLines = 3, options?: CallOptions) {
      if (mode !== "git" && mode !== "branch") throw new TypeError("Git diff mode is invalid")
      if (!Number.isSafeInteger(contextLines) || contextLines < 0 || contextLines > 10_000) {
        throw new RangeError("Git diff context must be between 0 and 10000 lines")
      }
      const response = await client.diffFiles(
        {
          workspaceId: checkedID(workspaceId),
          mode,
          path: checkedPath(path, true),
          contextLines,
        },
        options,
      )
      return response.files.map((file) => ({
        file: file.file,
        patch: file.patch,
        additions: file.additions,
        deletions: file.deletions,
        status: gitFileStatus(file.status),
      }))
    },
    async stage(workspaceId: string, paths: readonly string[] = [], all = false, options?: CallOptions) {
      return gitStatus(
        required(
          (await client.stage({ workspaceId: checkedID(workspaceId), paths: checkedPaths(paths), all }, options))
            .status,
          "Git status",
        ),
      )
    },
    async unstage(workspaceId: string, paths: readonly string[] = [], all = false, options?: CallOptions) {
      return gitStatus(
        required(
          (await client.unstage({ workspaceId: checkedID(workspaceId), paths: checkedPaths(paths), all }, options))
            .status,
          "Git status",
        ),
      )
    },
    async commit(workspaceId: string, message: string, options?: CallOptions) {
      return (
        await client.commit(
          { workspaceId: checkedID(workspaceId), message: checkedText(message, "Commit message", 64 * 1024) },
          options,
        )
      ).commit
    },
    async listWorktrees(workspaceId: string, options?: CallOptions) {
      return (await client.listWorktrees({ workspaceId: checkedID(workspaceId) }, options)).worktrees.map(worktree)
    },
    async createWorktree(workspaceId: string, branch: string, base = "", options?: CallOptions) {
      return worktree(
        required(
          (
            await client.createWorktree(
              { workspaceId: checkedID(workspaceId), branch: checkedRef(branch), base: base ? checkedRef(base) : "" },
              options,
            )
          ).worktree,
          "Git worktree",
        ),
      )
    },
    async removeWorktree(workspaceId: string, path: string, options?: CallOptions) {
      await client.removeWorktree({ workspaceId: checkedID(workspaceId), path: checkedWorktreePath(path) }, options)
    },
    async merge(workspaceId: string, branch: string, options?: CallOptions) {
      const response = await client.merge({ workspaceId: checkedID(workspaceId), branch: checkedRef(branch) }, options)
      return { commit: response.commit, fastForward: response.fastForward }
    },
  })
}

function gitStatus(value: GitStatus): OMAIGitStatus {
  return {
    branch: value.branch,
    defaultBranch: value.defaultBranch,
    files: value.files.map((file) => ({ path: file.path, status: file.status })),
  }
}

function worktree(value: WorktreeInfo): OMAIWorktree {
  return { path: value.path, branch: value.branch, head: value.head }
}

function gitFileStatus(value: string): OMAIGitFileDiff["status"] {
  if (value === "added" || value === "deleted" || value === "modified") return value
  throw new OMAIError("OMAI returned an invalid Git file status", { code: Code.DataLoss })
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
  if (trimmed.length === 0 || trimmed.length > limit || value.includes("\0")) throw new TypeError(`${label} is invalid`)
  return trimmed
}

function checkedPath(value: string, allowEmpty: boolean): string {
  if (value.length > 16 * 1024 || /[\0\r\n]/u.test(value)) throw new TypeError("Git path is invalid")
  const normalized = value
    .replaceAll("\\", "/")
    .replace(/^\.\//u, "")
    .replace(/^\/+|\/+$/gu, "")
  if (!allowEmpty && normalized.length === 0) throw new TypeError("Git path is required")
  if (normalized.split("/").some((part) => part === "..")) throw new TypeError("Git path cannot escape its root")
  return normalized
}

function checkedPaths(values: readonly string[]): string[] {
  if (values.length > 1_000) throw new RangeError("Too many Git paths")
  return [...new Set(values.map((value) => checkedPath(value, false)))]
}

function checkedWorktreePath(value: string): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > 16 * 1024 || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError("Git worktree path is invalid")
  }
  return trimmed
}

function checkedRef(value: string): string {
  if (!/^(?!-)(?!.*\.\.)(?!.*\.lock$)[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$/u.test(value)) {
    throw new TypeError("Git reference is invalid")
  }
  return value
}
