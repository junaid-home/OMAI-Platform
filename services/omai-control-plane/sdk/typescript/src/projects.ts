import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import type { Project, ProjectService } from "./gen/omai/platform/v1/platform_pb.js"
import { OMAIError } from "./errors.js"

export interface OMAIProject {
  readonly id: string
  readonly workspaceId: string
  readonly root: string
  readonly repoRoot: string
  readonly name: string
  readonly iconColor: string
  readonly iconOverride: string
  readonly startupCommand: string
  readonly createdUnixMillis: bigint
  readonly updatedUnixMillis: bigint
}

export interface OMAIProjectPatch {
  readonly name?: string
  readonly iconColor?: string
  readonly iconOverride?: string
  readonly startupCommand?: string
}

export interface OMAIProjects {
  resolve(root: string, name?: string, options?: CallOptions): Promise<OMAIProject>
  list(options?: CallOptions): Promise<readonly OMAIProject[]>
  get(projectId: string, options?: CallOptions): Promise<OMAIProject>
  update(projectId: string, patch: OMAIProjectPatch, options?: CallOptions): Promise<OMAIProject>
}

export function createProjects(client: Client<typeof ProjectService>): OMAIProjects {
  return Object.freeze({
    async resolve(root: string, name = "", options?: CallOptions) {
      const response = await client.resolveProject(
        { root: checkedText(root, "Project root", 16 * 1024), name: optionalText(name, "Project name", 200) },
        options,
      )
      return project(required(response.project, "project"))
    },
    async list(options?: CallOptions) {
      const result: OMAIProject[] = []
      let pageToken = ""
      for (let page = 0; page < 10_000; page++) {
        const response = await client.listProjects({ pageSize: 200, pageToken }, options)
        result.push(...response.projects.map(project))
        if (!response.nextPageToken) return result
        if (response.nextPageToken === pageToken) {
          throw new OMAIError("OMAI returned a repeated project page token", { code: Code.DataLoss })
        }
        pageToken = response.nextPageToken
      }
      throw new OMAIError("OMAI project pagination exceeded its safety limit", { code: Code.ResourceExhausted })
    },
    async get(projectId: string, options?: CallOptions) {
      const response = await client.getProject({ projectId: checkedID(projectId, "Project ID") }, options)
      return project(required(response.project, "project"))
    },
    async update(projectId: string, patch: OMAIProjectPatch, options?: CallOptions) {
      if (
        patch.name === undefined &&
        patch.iconColor === undefined &&
        patch.iconOverride === undefined &&
        patch.startupCommand === undefined
      ) {
        throw new TypeError("Project update cannot be empty")
      }
      const response = await client.updateProject(
        {
          projectId: checkedID(projectId, "Project ID"),
          ...(patch.name === undefined ? {} : { name: optionalText(patch.name, "Project name", 200) }),
          ...(patch.iconColor === undefined ? {} : { iconColor: checkedColor(patch.iconColor) }),
          ...(patch.iconOverride === undefined ? {} : { iconOverride: checkedIconOverride(patch.iconOverride) }),
          ...(patch.startupCommand === undefined
            ? {}
            : { startupCommand: optionalText(patch.startupCommand, "Project startup command", 64 * 1024) }),
        },
        options,
      )
      return project(required(response.project, "project"))
    },
  })
}

function project(value: Project): OMAIProject {
  return {
    id: value.id,
    workspaceId: value.workspaceId,
    root: value.root,
    repoRoot: value.repoRoot,
    name: value.name,
    iconColor: value.iconColor,
    iconOverride: value.iconOverride,
    startupCommand: value.startupCommand,
    createdUnixMillis: value.createdUnixMillis,
    updatedUnixMillis: value.updatedUnixMillis,
  }
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function checkedID(value: string, label: string): string {
  return checkedText(value, label, 512)
}

function checkedText(value: string, label: string, limit: number): string {
  const trimmed = value.trim()
  if (trimmed.length === 0 || trimmed.length > limit || /[\0\r\n]/u.test(trimmed)) {
    throw new TypeError(`${label} is invalid`)
  }
  return trimmed
}

function optionalText(value: string, label: string, limit: number): string {
  if (value === "") return ""
  return checkedText(value, label, limit)
}

function checkedColor(value: string): string {
  if (
    value === "" ||
    /^#[0-9a-f]{6}$/iu.test(value) ||
    ["pink", "mint", "orange", "purple", "cyan", "lime", "yellow", "green", "red", "blue", "gray"].includes(value)
  )
    return value
  throw new TypeError("Project icon color is invalid")
}

function checkedIconOverride(value: string): string {
  if (value === "") return value
  if (value.length > 2 * 1024 * 1024 || !/^data:image\/(png|jpeg|webp|gif|svg\+xml);base64,/u.test(value)) {
    throw new TypeError("Project icon override is invalid")
  }
  return value
}
