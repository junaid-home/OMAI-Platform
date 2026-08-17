import { onCleanup, onMount } from "solid-js"
import { omaiClient } from "@/omai/client"

const activeSessions = new Map<string, number>()
const previewUrls = new Map<string, string>()
const previewWorkspaces = new Map<string, string>()
const pendingStarts = new Map<string, Promise<string>>()
const HEARTBEAT_INTERVAL_MS = 60_000

export function getPreviewUrl(directory: string): string | undefined {
  return previewUrls.get(directory)
}

export function setPreviewUrl(directory: string, url: string): void {
  previewUrls.set(directory, url)
}

export async function startManagedPreview(directory: string, restart = false): Promise<string> {
  const dir = directory.trim()
  if (!dir) throw new TypeError("Preview directory is required")
  const existing = pendingStarts.get(dir)
  if (existing && !restart) return existing
  const operation = (restart ? omaiClient.preview.restart(dir) : omaiClient.preview.start(dir)).then((preview) => {
    if (!preview.publicUrl || !preview.workspaceId) throw new Error("OMAI returned an incomplete preview")
    previewUrls.set(dir, preview.publicUrl)
    previewWorkspaces.set(dir, preview.workspaceId)
    return preview.publicUrl
  })
  pendingStarts.set(dir, operation)
  try {
    return await operation
  } finally {
    if (pendingStarts.get(dir) === operation) pendingStarts.delete(dir)
  }
}

export function usePreviewLifecycle(directory: () => string) {
  let released = false
  const stopServer = async (dir: string) => {
    if (!dir) return
    const refs = (activeSessions.get(dir) ?? 1) - 1
    activeSessions.set(dir, refs)
    if (refs > 0) return
    activeSessions.delete(dir)
    try {
      await pendingStarts.get(dir)
    } catch {}
    previewUrls.delete(dir)
    const workspaceID = previewWorkspaces.get(dir)
    previewWorkspaces.delete(dir)
    if (!workspaceID) return
    try {
      await omaiClient.preview.stop(workspaceID)
    } catch {}
  }

  const startServer = async (dir: string) => {
    if (!dir) return
    const refs = (activeSessions.get(dir) ?? 0) + 1
    activeSessions.set(dir, refs)
    if (refs > 1) return
    try {
      await startManagedPreview(dir)
    } catch {}
  }

  onMount(() => {
    startServer(directory())
    const dir = directory()
    if (!dir) return
    const heartbeat = setInterval(() => {
      const workspaceID = previewWorkspaces.get(dir)
      if (workspaceID) omaiClient.preview.get(workspaceID).catch(() => {})
    }, HEARTBEAT_INTERVAL_MS)
    onCleanup(() => clearInterval(heartbeat))
  })

  onCleanup(() => {
    if (released) return
    released = true
    void stopServer(directory())
  })

  if (typeof window !== "undefined") {
    const dir = directory()
    const handler = () => {
      if (!dir || released) return
      released = true
      void stopServer(dir)
    }
    window.addEventListener("pagehide", handler)
    window.addEventListener("beforeunload", handler)
    onCleanup(() => {
      window.removeEventListener("pagehide", handler)
      window.removeEventListener("beforeunload", handler)
    })
  }

  return {
    start: () => startServer(directory()),
    stop: () => {
      if (released) return Promise.resolve()
      released = true
      return stopServer(directory())
    },
  }
}
