import { createRouterTransport } from "@connectrpc/connect"
import { describe, expect, it } from "vitest"
import { createOMAIClientFromTransport } from "../src/client.js"
import { PermissionDecision, PermissionService, QuestionService } from "../src/gen/omai/platform/v1/platform_pb.js"

describe("OMAI interaction facades", () => {
  it("lists and resolves typed permissions without leaking protobuf values", async () => {
    const transport = createRouterTransport((router) => {
      router.service(PermissionService, {
        listPermissions: () => ({
          permissions: [
            {
              id: "permission-1",
              sessionId: "session-1",
              projectId: "project-1",
              permission: "edit",
              patterns: ["src/**"],
              metadataJson: new TextEncoder().encode('{"reason":"agent edit"}'),
              always: ["src/**"],
              decision: PermissionDecision.UNSPECIFIED,
              createdUnixMillis: 10n,
            },
          ],
        }),
        respondPermission: (request) => ({
          permission: {
            id: request.permissionId,
            sessionId: request.sessionId,
            projectId: "project-1",
            permission: "edit",
            decision: request.decision,
            createdUnixMillis: 10n,
            resolvedUnixMillis: 20n,
          },
        }),
      })
    })
    const permissions = createOMAIClientFromTransport(transport).permissions

    await expect(permissions.list({ projectId: "project-1" })).resolves.toEqual([
      expect.objectContaining({ id: "permission-1", metadata: { reason: "agent edit" }, patterns: ["src/**"] }),
    ])
    await expect(permissions.respond("session-1", "permission-1", "always")).resolves.toMatchObject({
      decision: "always",
      resolvedUnixMillis: 20n,
    })
  })

  it("preserves question answer sets and validates empty replies", async () => {
    const transport = createRouterTransport((router) => {
      router.service(QuestionService, {
        listQuestions: () => ({
          questions: [
            {
              id: "question-1",
              sessionId: "session-1",
              projectId: "project-1",
              questions: [
                {
                  question: "Deploy now?",
                  header: "Deploy",
                  options: [{ label: "Yes", description: "Start deployment" }],
                },
              ],
              createdUnixMillis: 10n,
            },
          ],
        }),
        replyQuestion: (request) => ({
          question: {
            id: request.questionId,
            sessionId: request.sessionId,
            projectId: "project-1",
            answers: request.answers,
            createdUnixMillis: 10n,
            resolvedUnixMillis: 20n,
          },
        }),
      })
    })
    const questions = createOMAIClientFromTransport(transport).questions

    await expect(questions.list({ sessionId: "session-1" })).resolves.toEqual([
      expect.objectContaining({ id: "question-1", questions: [expect.objectContaining({ header: "Deploy" })] }),
    ])
    await expect(questions.reply("session-1", "question-1", [["Yes"]])).resolves.toMatchObject({ answers: [["Yes"]] })
    await expect(questions.reply("session-1", "question-1", [])).rejects.toThrow(RangeError)
  })
})
