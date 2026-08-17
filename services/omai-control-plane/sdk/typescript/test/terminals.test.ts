import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { TerminalService } from "../src/gen/uab/v1/native_pb.js"

describe("OMAI terminal facade", () => {
  it("creates, writes, resizes and streams through Go", async () => {
    const calls: string[] = []
    const transport = createRouterTransport((router) => {
      router.service(TerminalService, {
        listShells: () => ({ shells: [{ path: "/bin/sh", name: "sh", acceptable: true }] }),
        create: (request) => ({ terminal: { id: "terminal-1", workspaceId: request.workspaceId } }),
        write: (request) => {
          calls.push(new TextDecoder().decode(request.data))
          return {}
        },
        resize: (request) => {
          calls.push(`${request.cols}x${request.rows}`)
          return {}
        },
        watch: async function* () {
          yield { terminalId: "terminal-1", cursor: 3n, data: new TextEncoder().encode("ok\n") }
        },
      })
    })
    const terminals = createOMAIClientFromTransport(transport).terminals
    await expect(terminals.shells()).resolves.toEqual([{ path: "/bin/sh", name: "sh", acceptable: true }])
    const terminal = await terminals.create({ workspaceId: "workspace-1" })
    await terminals.write(terminal.id, new TextEncoder().encode("pwd\n"))
    await terminals.resize(terminal.id, 120, 40)

    const chunks = []
    for await (const chunk of terminals.watch(terminal.id)) chunks.push(new TextDecoder().decode(chunk.data))
    expect(calls).toEqual(["pwd\n", "120x40"])
    expect(chunks).toEqual(["ok\n"])
  })
})
