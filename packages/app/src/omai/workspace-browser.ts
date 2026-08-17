import { Code } from "@connectrpc/connect"
import { asOMAIError } from "@omai/sdk-web"
import { omaiClient, resolveOMAIWorkspace } from "./client"

export type WorkspaceBrowserEntry = {
  readonly name: string
  readonly absolute: string
  readonly type: "file" | "directory"
}

export interface OMAIWorkspaceBrowser {
  defaultRoot(): Promise<string | undefined>
  list(directory: string): Promise<readonly WorkspaceBrowserEntry[]>
  search(
    directory: string,
    query: string,
    kind: "file" | "directory" | "any",
    limit: number,
  ): Promise<readonly string[]>
}

export const omaiWorkspaceBrowser: OMAIWorkspaceBrowser = Object.freeze({
  async defaultRoot() {
    const workspaces = await omaiClient.workspaces.list()
    return workspaces.at(0)?.root
  },
  async list(directory: string) {
    const workspace = await resolveOMAIWorkspace(directory)
    const entries = await omaiClient.workspaces.listFiles(workspace.id)
    return entries.map((entry) => ({
      name: entry.name,
      absolute: joinAbsolute(directory, entry.path),
      type: entry.directory ? ("directory" as const) : ("file" as const),
    }))
  },
  async search(directory: string, query: string, kind: "file" | "directory" | "any", limit: number) {
    const workspace = await resolveOMAIWorkspace(directory)
    return omaiClient.workspaces.searchFiles(workspace.id, query, { kind, limit })
  },
})

export async function createOMAIWorkspaceDirectory(absolute: string): Promise<void> {
  const target = absolute.replaceAll("\\", "/").replace(/\/+$/u, "")
  if (!target.startsWith("/") || target === "/" || /[\0\r\n]/u.test(target)) {
    throw new TypeError("Project directory must be an absolute Linux path")
  }
  let ancestor = target.slice(0, target.lastIndexOf("/")) || "/"
  while (ancestor !== "/") {
    try {
      const workspace = await resolveOMAIWorkspace(ancestor)
      const relative = target.slice(ancestor.length).replace(/^\/+|\/+$/gu, "")
      await omaiClient.workspaces.createDirectory(workspace.id, relative)
      return
    } catch (error) {
      if (!isMissingWorkspace(error)) throw error
    }
    ancestor = ancestor.slice(0, ancestor.lastIndexOf("/")) || "/"
  }
  const workspace = await resolveOMAIWorkspace(ancestor)
  await omaiClient.workspaces.createDirectory(workspace.id, target.replace(/^\/+|\/+$/gu, ""))
}

export async function deleteOMAIWorkspaceDirectory(absolute: string): Promise<void> {
  const target = absolute.replaceAll("\\", "/").replace(/\/+$/u, "")
  if (!target.startsWith("/") || target === "/" || /[\0\r\n]/u.test(target)) {
    throw new TypeError("Project directory must be an absolute Linux path")
  }
  let ancestor = target.slice(0, target.lastIndexOf("/")) || "/"
  while (true) {
    try {
      const workspace = await resolveOMAIWorkspace(ancestor)
      const relative = target.slice(ancestor.length).replace(/^\/+|\/+$/gu, "")
      await omaiClient.workspaces.deletePath(workspace.id, relative, { recursive: true })
      return
    } catch (error) {
      if (!isMissingWorkspace(error)) throw error
    }
    if (ancestor === "/") break
    ancestor = ancestor.slice(0, ancestor.lastIndexOf("/")) || "/"
  }
  throw new Error("No OMAI workspace owns the project directory")
}

function isMissingWorkspace(error: unknown): boolean {
  return asOMAIError(error).code === Code.NotFound
}

function joinAbsolute(root: string, relative: string): string {
  const base = root.replaceAll("\\", "/").replace(/\/+$/u, "")
  const path = relative.replaceAll("\\", "/").replace(/^\/+|\/+$/gu, "")
  if (!path) return base || "/"
  if (!base || base === "/") return `/${path}`
  return `${base}/${path}`
}
