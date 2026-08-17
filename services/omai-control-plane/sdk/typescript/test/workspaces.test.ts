import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { FileChangeKind, FileSearchKind, WorkspaceService } from "../src/gen/uab/v1/native_pb.js"

const revision = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

describe("OMAI workspace facade", () => {
  it("resolves, lists and decodes text through the typed Go service", async () => {
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        resolveWorkspace: (request) => ({ workspace: workspace(request.root) }),
        listFiles: () => ({ entries: [{ name: "main.go", path: "main.go", size: 12n }] }),
        readFile: () => ({
          data: new TextEncoder().encode("package main\n"),
          revision,
          size: 13n,
          modifiedUnixMillis: 1_000n,
        }),
        searchFiles: (request) => ({ paths: request.kind === FileSearchKind.DIRECTORY ? [] : ["main.go"] }),
      })
    })
    const client = createOMAIClientFromTransport(transport).workspaces
    const resolved = await client.resolve("/workspace/app")

    await expect(client.listFiles(resolved.id)).resolves.toHaveLength(1)
    await expect(client.readFile(resolved.id, "main.go")).resolves.toEqual({
      type: "text",
      content: "package main\n",
      revision,
      size: 13,
      modifiedUnixMillis: 1_000,
    })
    await expect(client.searchFiles(resolved.id, "main")).resolves.toEqual(["main.go"])
    await expect(client.searchFiles(resolved.id, "src", { kind: "directory", limit: 20 })).resolves.toEqual([])
  })

  it("rejects escaping paths and encodes binary responses", async () => {
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        readFile: () => ({ data: new Uint8Array([0, 1, 2]), revision, size: 3n, modifiedUnixMillis: 1_000n }),
      })
    })
    const client = createOMAIClientFromTransport(transport).workspaces

    await expect(client.readFile("workspace-1", "../secret")).rejects.toThrow(TypeError)
    await expect(client.readFile("workspace-1", "asset.bin")).resolves.toEqual({
      type: "binary",
      content: "AAEC",
      encoding: "base64",
      revision,
      size: 3,
      modifiedUnixMillis: 1_000,
    })
  })

  it("exposes curated workspace changes instead of protobuf internals", async () => {
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        async *watchFiles() {
          yield { sequence: 1n, path: "main.go", kind: FileChangeKind.CHANGE }
          yield { sequence: 2n, path: "", kind: FileChangeKind.RESYNC }
        },
      })
    })
    const stream = createOMAIClientFromTransport(transport).workspaces.watchFiles("workspace-1", [""])
    const changes = []
    for await (const change of stream) changes.push(change)

    expect(changes).toEqual([
      { sequence: 1n, path: "main.go", kind: "change" },
      { sequence: 2n, path: "", kind: "resync" },
    ])
  })

  it("creates only relative contained directories", async () => {
    const created: string[] = []
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        createDirectory: (request) => {
          created.push(request.path)
          return {}
        },
      })
    })
    const client = createOMAIClientFromTransport(transport).workspaces

    await client.createDirectory("workspace-1", "projects/demo")
    await expect(client.createDirectory("workspace-1", "../escape")).rejects.toThrow(TypeError)
    expect(created).toEqual(["projects/demo"])
  })

  it("uses revisions for Monaco-safe writes and Go-owned path mutations", async () => {
    const calls: unknown[] = []
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        writeFile: (request) => {
          calls.push(request)
          return { revision, size: BigInt(request.data.length), modifiedUnixMillis: 2_000n }
        },
        movePath: (request) => {
          calls.push(request)
          // Directory moves intentionally have no file revision.
          return { revision: "", size: 0n, modifiedUnixMillis: 3_000n }
        },
        deletePath: (request) => {
          calls.push(request)
          return {}
        },
      })
    })
    const client = createOMAIClientFromTransport(transport).workspaces

    await expect(
      client.writeFile("workspace-1", "main.go", new TextEncoder().encode("updated"), { expectedRevision: revision }),
    ).resolves.toEqual({ revision, size: 7, modifiedUnixMillis: 2_000 })
    await client.movePath("workspace-1", "main.go", "src/main.go", { expectedRevision: revision })
    await client.deletePath("workspace-1", "src/main.go", { expectedRevision: revision })

    expect(calls).toMatchObject([
      { path: "main.go", expectedRevision: revision, requireRevisionMatch: true },
      { sourcePath: "main.go", destinationPath: "src/main.go", expectedRevision: revision },
      { path: "src/main.go", expectedRevision: revision },
    ])
  })

  it("imports and streams workspace archives through Go", async () => {
    const uploads: Uint8Array[] = []
    const transport = createRouterTransport((router) => {
      router.service(WorkspaceService, {
        importArchive: (request) => {
          uploads.push(request.data)
          return { files: 2n, directories: 1n, uncompressedBytes: 12n }
        },
        async *exportArchive() {
          yield { data: new Uint8Array([1, 2]) }
          yield { data: new Uint8Array([3, 4]) }
        },
      })
    })
    const client = createOMAIClientFromTransport(transport).workspaces

    await expect(
      client.importArchive("workspace-1", new Uint8Array([80, 75]), { stripSingleRoot: true }),
    ).resolves.toEqual({
      files: 2,
      directories: 1,
      uncompressedBytes: 12,
    })
    await expect(client.exportArchive("workspace-1")).resolves.toEqual(new Uint8Array([1, 2, 3, 4]))
    expect(uploads).toEqual([new Uint8Array([80, 75])])
  })
})

function workspace(root: string) {
  return { id: "workspace-1", root, repoRoot: root, nodeId: "test" }
}
