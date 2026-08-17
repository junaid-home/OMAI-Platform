import { omaiClient, resolveOMAIWorkspace } from "./client"

export interface OMAIMonacoDocument {
  readonly workspaceId: string
  readonly path: string
  readonly value: string
  readonly revision: string
  readonly modifiedUnixMillis: number
}

export async function openOMAIMonacoDocument(root: string, path: string): Promise<OMAIMonacoDocument> {
  const workspace = await resolveOMAIWorkspace(root)
  const content = await omaiClient.workspaces.readFile(workspace.id, path)
  if (content.type !== "text") throw new TypeError("Monaco can open only UTF-8 workspace files")
  return {
    workspaceId: workspace.id,
    path,
    value: content.content,
    revision: content.revision,
    modifiedUnixMillis: content.modifiedUnixMillis,
  }
}

export async function saveOMAIMonacoDocument(document: OMAIMonacoDocument, value: string): Promise<OMAIMonacoDocument> {
  const metadata = await omaiClient.workspaces.writeFile(
    document.workspaceId,
    document.path,
    new TextEncoder().encode(value),
    { expectedRevision: document.revision },
  )
  return {
    ...document,
    value,
    revision: metadata.revision,
    modifiedUnixMillis: metadata.modifiedUnixMillis,
  }
}
