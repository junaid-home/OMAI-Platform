import { Code, createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { PreviewService } from "../src/gen/uab/v1/preview_pb.js"

describe("OMAI preview facade", () => {
  it("starts, restarts and stops through the typed service", async () => {
    let starts = 0
    const transport = createRouterTransport((router) => {
      router.service(PreviewService, {
        start: (request) => {
          expect(request.root).toBe("/workspace/app")
          starts += 1
          return { preview: instance(`preview-${starts}`) }
        },
        restart: (request) => {
          expect(request.root).toBe("/workspace/app")
          return { preview: instance("preview-restarted") }
        },
        stop: (request) => {
          expect(request.workspaceId).toBe("workspace-1")
          return { stopped: true }
        },
      })
    })
    const preview = createOMAIClientFromTransport(transport).preview

    await expect(preview.start("/workspace/app")).resolves.toMatchObject({ id: "preview-1", status: "ready" })
    await expect(preview.restart("/workspace/app")).resolves.toMatchObject({ id: "preview-restarted" })
    await expect(preview.stop("workspace-1")).resolves.toBe(true)
  })

  it("rejects unsafe input and incomplete server responses", async () => {
    const transport = createRouterTransport((router) => {
      router.service(PreviewService, { start: () => ({}) })
    })
    const preview = createOMAIClientFromTransport(transport).preview

    await expect(preview.start("/workspace\nattacker")).rejects.toThrow(TypeError)
    await expect(preview.start("/workspace/app")).rejects.toMatchObject({ code: Code.Internal })
  })
})

function instance(id: string) {
  return {
    id,
    workspaceId: "workspace-1",
    processId: "process-1",
    serviceId: "web",
    framework: "vite",
    planFingerprint: "sha256:test",
    port: 4173,
    status: "ready",
    publicUrl: "https://preview.example/",
    startedUnixMillis: 1n,
    updatedUnixMillis: 1n,
  }
}
