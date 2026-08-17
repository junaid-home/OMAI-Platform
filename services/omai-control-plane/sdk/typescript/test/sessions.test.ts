import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { ProjectService, SessionService } from "../src/gen/omai/platform/v1/platform_pb.js"
import { createOMAIClientFromTransport } from "../src/client.js"

describe("OMAI sessions facade", () => {
  it("owns session lifecycle through the platform service", async () => {
    const transport = createRouterTransport((router) => {
      router.service(SessionService, {
        createSession: (request) => ({
          created: true,
          session: {
            id: "session-1",
            projectId: request.projectId,
            runtimeId: request.runtimeId,
            externalSessionId: request.externalSessionId,
            title: request.title,
          },
        }),
        listSessions: () => ({ sessions: [{ id: "session-1", projectId: "project-1" }] }),
        getSession: () => ({ session: { id: "session-1", projectId: "project-1" } }),
        updateSession: (request) => ({
          session: { id: request.sessionId, projectId: "project-1", title: request.title ?? "" },
        }),
        deleteSession: () => ({ deleted: true }),
      })
    })
    const sessions = createOMAIClientFromTransport(transport).sessions

    await expect(
      sessions.create({
        projectId: "project-1",
        runtimeId: "go-adk",
        externalSessionId: "external-1",
        title: "Build OMAI",
      }),
    ).resolves.toMatchObject({ created: true, session: { id: "session-1", title: "Build OMAI" } })
    await expect(sessions.list("project-1")).resolves.toHaveLength(1)
    await expect(sessions.get("session-1")).resolves.toMatchObject({ projectId: "project-1" })
    await expect(sessions.update("session-1", { title: "Production OMAI" })).resolves.toMatchObject({
      title: "Production OMAI",
    })
    await expect(sessions.delete("session-1")).resolves.toBe(true)
  })

  it("submits an explicit executable route and maps the handle", async () => {
    const transport = createRouterTransport((router) => {
      router.service(ProjectService, {
        resolveProject: (request) => {
          expect(request).toMatchObject({ root: "/workspace/project" })
          return { project: { id: "project-1", workspaceId: "workspace-1", root: request.root } }
        },
      })
      router.service(SessionService, {
        createSession: (request) => {
          expect(request).toMatchObject({
            projectId: "project-1",
            runtimeId: "go-adk",
            externalSessionId: "portal-1",
          })
          return { created: true, session: { id: "session-1" } }
        },
        submitText: (request) => {
          expect(request).toMatchObject({
            sessionId: "session-1",
            providerId: "openrouter",
            modelId: "anthropic/claude-sonnet-4.5",
          })
          return {
            session: {
              id: "session-1",
              externalSessionId: "portal-1",
              projectId: "project-1",
              workspaceId: "workspace-1",
              runtimeId: "go-adk",
              providerId: "openrouter",
              modelId: "anthropic/claude-sonnet-4.5",
            },
          }
        },
      })
    })

    await expect(
      createOMAIClientFromTransport(transport).sessions.send({
        runtimeId: "go-adk",
        providerId: "openrouter",
        modelId: "anthropic/claude-sonnet-4.5",
        externalSessionId: "portal-1",
        root: "/workspace/project",
        text: "Review the diff",
      }),
    ).resolves.toEqual({
      runtimeId: "go-adk",
      providerId: "openrouter",
      modelId: "anthropic/claude-sonnet-4.5",
      sessionId: "session-1",
      externalSessionId: "portal-1",
      projectId: "project-1",
      workspaceId: "workspace-1",
      root: "/workspace/project",
    })
  })

  it("maps immutable conversation history", async () => {
    const transport = createRouterTransport((router) => {
      router.service(SessionService, {
        listMessages: () => ({
          messages: [
            {
              id: "message-1",
              sessionId: "session-1",
              role: "assistant",
              kind: "text",
              text: "Done",
              dataJson: new Uint8Array([1, 2]),
              createdUnixMillis: 5n,
            },
          ],
        }),
      })
    })
    const messages = await createOMAIClientFromTransport(transport).sessions.listMessages("session-1")

    expect(messages).toEqual([
      {
        id: "message-1",
        sessionId: "session-1",
        role: "assistant",
        kind: "text",
        text: "Done",
        dataJson: new Uint8Array([1, 2]),
        createdAtUnixMillis: 5n,
      },
    ])
    expect(Object.isFrozen(messages)).toBe(true)
    expect(Object.isFrozen(messages[0])).toBe(true)
  })

  it("rejects incomplete routes before transport", async () => {
    const transport = createRouterTransport(() => undefined)
    await expect(
      createOMAIClientFromTransport(transport).sessions.send({
        runtimeId: "go-adk",
        providerId: "",
        modelId: "model",
        externalSessionId: "portal-1",
        root: "/workspace/project",
        text: "test",
      }),
    ).rejects.toThrow(TypeError)
  })
})
