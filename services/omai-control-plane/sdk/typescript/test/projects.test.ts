import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { ProjectService } from "../src/gen/omai/platform/v1/platform_pb.js"

describe("OMAI project facade", () => {
  it("resolves, lists and updates authoritative Go projects", async () => {
    const transport = createRouterTransport((router) => {
      router.service(ProjectService, {
        resolveProject: (request) => ({ project: { id: "project-1", root: request.root, name: request.name } }),
        listProjects: () => ({ projects: [{ id: "project-1", root: "/workspace", name: "workspace" }] }),
        updateProject: (request) => ({
          project: {
            id: request.projectId,
            root: "/workspace",
            name: request.name ?? "",
            iconColor: request.iconColor ?? "",
            iconOverride: request.iconOverride ?? "",
            startupCommand: request.startupCommand ?? "",
          },
        }),
      })
    })
    const projects = createOMAIClientFromTransport(transport).projects

    await expect(projects.resolve("/workspace", "workspace")).resolves.toMatchObject({ id: "project-1" })
    await expect(projects.list()).resolves.toEqual([
      {
        id: "project-1",
        workspaceId: "",
        root: "/workspace",
        repoRoot: "",
        name: "workspace",
        iconColor: "",
        iconOverride: "",
        startupCommand: "",
        createdUnixMillis: 0n,
        updatedUnixMillis: 0n,
      },
    ])
    await expect(
      projects.update("project-1", { name: "OMAI", iconColor: "#123abc", startupCommand: "bun dev" }),
    ).resolves.toMatchObject({
      name: "OMAI",
      iconColor: "#123abc",
      startupCommand: "bun dev",
    })
  })
})
