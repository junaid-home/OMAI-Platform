import { omaiClient, resolveOMAIWorkspace } from "./client"

export async function moveOMAIWorkspacePath(root: string, sourcePath: string, destinationPath: string): Promise<void> {
  const workspace = await resolveOMAIWorkspace(root)
  await omaiClient.workspaces.movePath(workspace.id, sourcePath, destinationPath)
}

export async function deleteOMAIWorkspacePath(root: string, path: string): Promise<void> {
  const workspace = await resolveOMAIWorkspace(root)
  await omaiClient.workspaces.deletePath(workspace.id, path, { recursive: true })
}

export async function importOMAIWorkspaceArchive(root: string, file: File): Promise<void> {
  const workspace = await resolveOMAIWorkspace(root)
  const data = new Uint8Array(await file.arrayBuffer())
  await omaiClient.workspaces.importArchive(workspace.id, data, { stripSingleRoot: true })
}

export async function downloadOMAIWorkspaceArchive(root: string, name: string): Promise<void> {
  const workspace = await resolveOMAIWorkspace(root)
  const data = await omaiClient.workspaces.exportArchive(workspace.id)
  const copy = new Uint8Array(data.byteLength)
  copy.set(data)
  const blob = new Blob([copy.buffer], { type: "application/zip" })
  const url = URL.createObjectURL(blob)
  try {
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = name.endsWith(".zip") ? name : `${name}.zip`
    anchor.click()
  } finally {
    URL.revokeObjectURL(url)
  }
}
