import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { GitService } from "../src/gen/uab/v1/native_pb.js"

describe("OMAI Git facade", () => {
  it("owns init, status and mutations through the typed Go service", async () => {
    const calls: string[] = []
    const transport = createRouterTransport((router) => {
      router.service(GitService, {
        init: (request) => ({ status: { branch: request.workspaceId, files: [] } }),
        status: () => ({ status: { branch: "main", files: [{ path: "main.go", status: " M" }] } }),
        diffFiles: (request) => ({
          files: [
            {
              file: request.path || "main.go",
              patch: "@@ -1 +1 @@\n-old\n+new\n",
              additions: 1,
              deletions: 1,
              status: "modified",
            },
          ],
        }),
        stage: (request) => {
          calls.push(request.paths.join(","))
          return { status: { branch: "main" } }
        },
        commit: (request) => {
          calls.push(request.message)
          return { commit: "abc123" }
        },
      })
    })
    const git = createOMAIClientFromTransport(transport).git

    await expect(git.init("workspace-1")).resolves.toMatchObject({ branch: "workspace-1" })
    await expect(git.status("workspace-1")).resolves.toMatchObject({ branch: "main" })
    await expect(git.diffFiles("workspace-1", "git", "main.go", 3)).resolves.toEqual([
      {
        file: "main.go",
        patch: "@@ -1 +1 @@\n-old\n+new\n",
        additions: 1,
        deletions: 1,
        status: "modified",
      },
    ])
    await git.stage("workspace-1", ["main.go"])
    await expect(git.commit("workspace-1", "feat: migrate Git")).resolves.toBe("abc123")
    expect(calls).toEqual(["main.go", "feat: migrate Git"])
  })

  it("rejects escaping paths and unsafe references before transport", async () => {
    const transport = createRouterTransport((router) => router.service(GitService, {}))
    const git = createOMAIClientFromTransport(transport).git

    await expect(git.diff("workspace-1", "../secret")).rejects.toThrow(TypeError)
    await expect(git.merge("workspace-1", "--upload-pack=bad")).rejects.toThrow(TypeError)
  })
})
