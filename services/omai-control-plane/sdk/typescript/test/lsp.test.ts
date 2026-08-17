import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { LSPService } from "../src/gen/uab/v1/native_pb.js"

describe("OMAI LSP facade", () => {
  it("lists, starts, writes and streams language servers through Go", async () => {
    const writes: string[] = []
    const transport = createRouterTransport((router) => {
      router.service(LSPService, {
        list: () => ({ servers: [{ id: "gopls", name: "gopls", command: "gopls", available: true }] }),
        start: (request) => ({
          instance: { id: "lsp-1", workspaceId: request.workspaceId, serverId: request.serverId, status: "running" },
        }),
        listInstances: () => ({ instances: [{ id: "lsp-1", serverId: "gopls", status: "running" }] }),
        write: (request) => {
          writes.push(new TextDecoder().decode(request.data))
          return {}
        },
        watch: async function* () {
          yield { instanceId: "lsp-1", cursor: 1n, data: new TextEncoder().encode("response") }
        },
      })
    })
    const lsp = createOMAIClientFromTransport(transport).lsp

    await expect(lsp.servers("workspace-1")).resolves.toEqual([
      { id: "gopls", name: "gopls", command: "gopls", available: true, version: "" },
    ])
    await expect(lsp.start("workspace-1", "gopls")).resolves.toMatchObject({ id: "lsp-1", status: "running" })
    await lsp.write("lsp-1", new TextEncoder().encode("request"))
    const chunks = []
    for await (const chunk of lsp.watch("lsp-1")) chunks.push(new TextDecoder().decode(chunk.data))
    expect(writes).toEqual(["request"])
    expect(chunks).toEqual(["response"])
  })
})
