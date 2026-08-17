import type { PermissionRequest, QuestionAnswer, QuestionRequest } from "@opencode-ai/sdk/v2/client"
import type { OMAIPermission, OMAIQuestionRequest } from "@omai/sdk-web"
import { omaiClient } from "./client"

export async function listOMAIPermissionsForRoot(root: string, signal?: AbortSignal): Promise<PermissionRequest[]> {
  const project = await omaiClient.projects.resolve(root, "", { signal })
  return (await omaiClient.permissions.list({ projectId: project.id }, { signal })).map(permissionView)
}

export async function listOMAIQuestionsForRoot(root: string, signal?: AbortSignal): Promise<QuestionRequest[]> {
  const project = await omaiClient.projects.resolve(root, "", { signal })
  return (await omaiClient.questions.list({ projectId: project.id }, { signal })).map(questionView)
}

export function respondOMAIPermission(input: {
  sessionID: string
  permissionID: string
  response: "once" | "always" | "reject"
}) {
  return omaiClient.permissions.respond(input.sessionID, input.permissionID, input.response)
}

export function replyOMAIQuestion(request: Pick<QuestionRequest, "id" | "sessionID">, answers: QuestionAnswer[]) {
  return omaiClient.questions.reply(request.sessionID, request.id, answers)
}

export function rejectOMAIQuestion(request: Pick<QuestionRequest, "id" | "sessionID">) {
  return omaiClient.questions.reject(request.sessionID, request.id)
}

function permissionView(value: OMAIPermission): PermissionRequest {
  return {
    id: value.id,
    sessionID: value.sessionId,
    permission: value.permission,
    patterns: [...value.patterns],
    metadata: { ...value.metadata },
    always: [...value.always],
    ...(value.tool === undefined ? {} : { tool: { messageID: value.tool.messageId, callID: value.tool.callId } }),
  }
}

function questionView(value: OMAIQuestionRequest): QuestionRequest {
  return {
    id: value.id,
    sessionID: value.sessionId,
    questions: value.questions.map((question) => ({
      question: question.question,
      header: question.header,
      options: question.options.map((option) => ({ label: option.label, description: option.description })),
      multiple: question.multiple,
      custom: question.custom,
    })),
    ...(value.tool === undefined ? {} : { tool: { messageID: value.tool.messageId, callID: value.tool.callId } }),
  }
}
