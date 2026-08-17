import { Code, type CallOptions, type Client } from "@connectrpc/connect"
import {
  PermissionDecision,
  type Permission,
  type PermissionService,
  type QuestionRequestResource,
  type QuestionService,
} from "./gen/omai/platform/v1/platform_pb.js"
import { decodeJsonBytes } from "./json.js"
import { OMAIError } from "./errors.js"

export interface OMAIToolReference {
  readonly messageId: string
  readonly callId: string
}

export interface OMAIPermission {
  readonly id: string
  readonly sessionId: string
  readonly projectId: string
  readonly permission: string
  readonly patterns: readonly string[]
  readonly metadata: Readonly<Record<string, unknown>>
  readonly always: readonly string[]
  readonly tool?: OMAIToolReference
  readonly decision?: "once" | "always" | "reject"
  readonly createdUnixMillis: bigint
  readonly resolvedUnixMillis?: bigint
}

export interface OMAIQuestionOption {
  readonly label: string
  readonly description: string
}

export interface OMAIQuestion {
  readonly question: string
  readonly header: string
  readonly options: readonly OMAIQuestionOption[]
  readonly multiple: boolean
  readonly custom: boolean
}

export interface OMAIQuestionRequest {
  readonly id: string
  readonly sessionId: string
  readonly projectId: string
  readonly questions: readonly OMAIQuestion[]
  readonly tool?: OMAIToolReference
  readonly answers: readonly (readonly string[])[]
  readonly rejected: boolean
  readonly createdUnixMillis: bigint
  readonly resolvedUnixMillis?: bigint
}

export interface OMAIInteractionFilter {
  readonly projectId?: string
  readonly sessionId?: string
}

export interface OMAIPermissions {
  list(filter: OMAIInteractionFilter, options?: CallOptions): Promise<readonly OMAIPermission[]>
  respond(
    sessionId: string,
    permissionId: string,
    decision: "once" | "always" | "reject",
    options?: CallOptions,
  ): Promise<OMAIPermission>
}

export interface OMAIQuestions {
  list(filter: OMAIInteractionFilter, options?: CallOptions): Promise<readonly OMAIQuestionRequest[]>
  reply(
    sessionId: string,
    questionId: string,
    answers: readonly (readonly string[])[],
    options?: CallOptions,
  ): Promise<OMAIQuestionRequest>
  reject(sessionId: string, questionId: string, options?: CallOptions): Promise<OMAIQuestionRequest>
}

export function createPermissions(client: Client<typeof PermissionService>): OMAIPermissions {
  return Object.freeze({
    async list(filter: OMAIInteractionFilter, options?: CallOptions) {
      const checked = checkedFilter(filter)
      const result: OMAIPermission[] = []
      let pageToken = ""
      for (let page = 0; page < 10_000; page++) {
        const response = await client.listPermissions({ ...checked, pageSize: 200, pageToken }, options)
        result.push(...response.permissions.map(permissionView))
        if (!response.nextPageToken) return Object.freeze(result)
        if (response.nextPageToken === pageToken) throw repeatedPageToken("permission")
        pageToken = response.nextPageToken
      }
      throw pageLimit("permission")
    },
    async respond(
      sessionId: string,
      permissionId: string,
      decision: "once" | "always" | "reject",
      options?: CallOptions,
    ) {
      const response = await client.respondPermission(
        {
          sessionId: checkedID(sessionId, "session"),
          permissionId: checkedID(permissionId, "permission"),
          decision: permissionDecision(decision),
        },
        options,
      )
      return permissionView(required(response.permission, "permission"))
    },
  })
}

export function createQuestions(client: Client<typeof QuestionService>): OMAIQuestions {
  return Object.freeze({
    async list(filter: OMAIInteractionFilter, options?: CallOptions) {
      const checked = checkedFilter(filter)
      const result: OMAIQuestionRequest[] = []
      let pageToken = ""
      for (let page = 0; page < 10_000; page++) {
        const response = await client.listQuestions({ ...checked, pageSize: 200, pageToken }, options)
        result.push(...response.questions.map(questionView))
        if (!response.nextPageToken) return Object.freeze(result)
        if (response.nextPageToken === pageToken) throw repeatedPageToken("question")
        pageToken = response.nextPageToken
      }
      throw pageLimit("question")
    },
    async reply(sessionId: string, questionId: string, answers: readonly (readonly string[])[], options?: CallOptions) {
      if (answers.length < 1 || answers.length > 32) throw new RangeError("Question answers are invalid")
      const response = await client.replyQuestion(
        {
          sessionId: checkedID(sessionId, "session"),
          questionId: checkedID(questionId, "question"),
          answers: answers.map((values) => ({ values: checkedAnswers(values) })),
        },
        options,
      )
      return questionView(required(response.question, "question"))
    },
    async reject(sessionId: string, questionId: string, options?: CallOptions) {
      const response = await client.rejectQuestion(
        { sessionId: checkedID(sessionId, "session"), questionId: checkedID(questionId, "question") },
        options,
      )
      return questionView(required(response.question, "question"))
    },
  })
}

export function permissionView(value: Permission): OMAIPermission {
  const metadata = value.metadataJson.byteLength === 0 ? {} : decodeJsonBytes(value.metadataJson, 64 * 1024)
  if (typeof metadata !== "object" || metadata === null || Array.isArray(metadata)) {
    throw new OMAIError("OMAI returned invalid permission metadata", { code: Code.DataLoss })
  }
  const tool = value.tool
  return Object.freeze({
    id: value.id,
    sessionId: value.sessionId,
    projectId: value.projectId,
    permission: value.permission,
    patterns: Object.freeze([...value.patterns]),
    metadata: Object.freeze(metadata as Record<string, unknown>),
    always: Object.freeze([...value.always]),
    ...(tool === undefined ? {} : { tool: Object.freeze({ messageId: tool.messageId, callId: tool.callId }) }),
    ...(value.decision === PermissionDecision.UNSPECIFIED ? {} : { decision: permissionDecisionView(value.decision) }),
    createdUnixMillis: value.createdUnixMillis,
    ...(value.resolvedUnixMillis === 0n ? {} : { resolvedUnixMillis: value.resolvedUnixMillis }),
  })
}

export function questionView(value: QuestionRequestResource): OMAIQuestionRequest {
  const tool = value.tool
  return Object.freeze({
    id: value.id,
    sessionId: value.sessionId,
    projectId: value.projectId,
    questions: Object.freeze(
      value.questions.map((question) =>
        Object.freeze({
          question: question.question,
          header: question.header,
          options: Object.freeze(
            question.options.map((option) => Object.freeze({ label: option.label, description: option.description })),
          ),
          multiple: question.multiple,
          custom: question.custom,
        }),
      ),
    ),
    ...(tool === undefined ? {} : { tool: Object.freeze({ messageId: tool.messageId, callId: tool.callId }) }),
    answers: Object.freeze(value.answers.map((answer) => Object.freeze([...answer.values]))),
    rejected: value.rejected,
    createdUnixMillis: value.createdUnixMillis,
    ...(value.resolvedUnixMillis === 0n ? {} : { resolvedUnixMillis: value.resolvedUnixMillis }),
  })
}

function checkedFilter(filter: OMAIInteractionFilter): { projectId: string; sessionId: string } {
  if (filter.projectId === undefined && filter.sessionId === undefined) {
    throw new TypeError("Interaction filter requires a projectId or sessionId")
  }
  return {
    projectId: filter.projectId === undefined ? "" : checkedID(filter.projectId, "project"),
    sessionId: filter.sessionId === undefined ? "" : checkedID(filter.sessionId, "session"),
  }
}

function checkedAnswers(values: readonly string[]): string[] {
  if (values.length < 1 || values.length > 64) throw new RangeError("Question answer set is invalid")
  return values.map((value) => {
    const trimmed = value.trim()
    if (!trimmed || trimmed.length > 4096 || /\0/u.test(trimmed)) throw new TypeError("Question answer is invalid")
    return trimmed
  })
}

function checkedID(value: string, label: string): string {
  const trimmed = value.trim()
  if (!trimmed || trimmed.length > 512 || /[\0\r\n]/u.test(trimmed)) throw new TypeError(`Invalid OMAI ${label} ID`)
  return trimmed
}

function permissionDecision(value: "once" | "always" | "reject"): PermissionDecision {
  if (value === "once") return PermissionDecision.ONCE
  if (value === "always") return PermissionDecision.ALWAYS
  return PermissionDecision.REJECT
}

export function permissionDecisionView(value: PermissionDecision): "once" | "always" | "reject" {
  if (value === PermissionDecision.ONCE) return "once"
  if (value === PermissionDecision.ALWAYS) return "always"
  if (value === PermissionDecision.REJECT) return "reject"
  throw new OMAIError("OMAI returned an invalid permission decision", { code: Code.DataLoss })
}

function required<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new OMAIError(`OMAI returned no ${label}`, { code: Code.Internal })
  return value
}

function repeatedPageToken(label: string) {
  return new OMAIError(`OMAI returned a repeated ${label} page token`, { code: Code.DataLoss })
}

function pageLimit(label: string) {
  return new OMAIError(`OMAI ${label} pagination exceeded its safety limit`, { code: Code.ResourceExhausted })
}
