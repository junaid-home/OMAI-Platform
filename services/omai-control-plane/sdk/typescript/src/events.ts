import type { Event as WireEvent } from "./gen/uab/v1/uab_pb.js"
import {
  MessageChannel,
  SessionState,
  type SessionEvent as TypedWireEvent,
} from "./gen/omai/platform/v1/platform_pb.js"
import { decodeJsonBytes } from "./json.js"
import {
  permissionDecisionView,
  permissionView,
  questionView,
  type OMAIPermission,
  type OMAIQuestionRequest,
} from "./interactions.js"

export type OMAISessionState = "running" | "idle"

export interface OMAIEventBase {
  readonly sequence: bigint
  readonly occurredAtUnixMillis: bigint
  readonly workspaceId: string
  readonly sessionId: string
  readonly runtimeId: string
  readonly externalSessionId: string
}

export interface OMAISessionStateEvent extends OMAIEventBase {
  readonly type: "session_state"
  readonly state: OMAISessionState
}

export interface OMAIMessageDeltaEvent extends OMAIEventBase {
  readonly type: "message_delta"
  readonly messageId: string
  readonly channel: "answer" | "reasoning"
  readonly delta: string
}

export interface OMAIToolCallEvent extends OMAIEventBase {
  readonly type: "tool_call"
  readonly toolCallId: string
  readonly toolName: string
  readonly status: string
  readonly arguments: unknown
}

export interface OMAIToolUpdateEvent extends OMAIEventBase {
  readonly type: "tool_update"
  readonly toolCallId: string
  readonly status: string
  readonly output: unknown
}

export interface OMAISessionErrorEvent extends OMAIEventBase {
  readonly type: "session_error"
  readonly message: string
}

export interface OMAIPermissionRequestedEvent extends OMAIEventBase {
  readonly type: "permission_requested"
  readonly permission: OMAIPermission
}

export interface OMAIPermissionResolvedEvent extends OMAIEventBase {
  readonly type: "permission_resolved"
  readonly permissionId: string
  readonly decision: "once" | "always" | "reject"
}

export interface OMAIQuestionRequestedEvent extends OMAIEventBase {
  readonly type: "question_requested"
  readonly question: OMAIQuestionRequest
}

export interface OMAIQuestionResolvedEvent extends OMAIEventBase {
  readonly type: "question_resolved"
  readonly questionId: string
  readonly answers: readonly (readonly string[])[]
  readonly rejected: boolean
}

export interface OMAIUnknownEvent extends OMAIEventBase {
  readonly type: "unknown"
  readonly wireType: string
  readonly payload: unknown
}

export type OMAISessionEvent =
  | OMAISessionStateEvent
  | OMAIMessageDeltaEvent
  | OMAIToolCallEvent
  | OMAIToolUpdateEvent
  | OMAISessionErrorEvent
  | OMAIPermissionRequestedEvent
  | OMAIPermissionResolvedEvent
  | OMAIQuestionRequestedEvent
  | OMAIQuestionResolvedEvent
  | OMAIUnknownEvent

/** Convert the stable omai.platform.v1 oneof into the SDK event algebra. */
export function parseTypedSessionEvent(event: TypedWireEvent): OMAISessionEvent {
  const base = Object.freeze({
    sequence: event.sequence,
    occurredAtUnixMillis: event.occurredAtUnixMillis,
    workspaceId: event.workspaceId,
    sessionId: event.sessionId,
    runtimeId: event.runtimeId,
    externalSessionId: event.externalSessionId,
  })
  const payload = event.payload
  switch (payload.case) {
    case "stateChanged":
      if (payload.value.state === SessionState.RUNNING || payload.value.state === SessionState.IDLE) {
        return Object.freeze({
          ...base,
          type: "session_state",
          state: payload.value.state === SessionState.RUNNING ? "running" : "idle",
        })
      }
      return Object.freeze({ ...base, type: "unknown", wireType: "session_state_unspecified", payload: payload.value })
    case "messageDelta":
      return Object.freeze({
        ...base,
        type: "message_delta",
        messageId: payload.value.messageId,
        channel: payload.value.channel === MessageChannel.REASONING ? "reasoning" : "answer",
        delta: payload.value.delta,
      })
    case "toolCallStarted":
      return Object.freeze({
        ...base,
        type: "tool_call",
        toolCallId: payload.value.toolCallId,
        toolName: payload.value.toolName,
        status: payload.value.status,
        arguments: decodeOptionalJson(payload.value.argumentsJson),
      })
    case "toolCallUpdated":
      return Object.freeze({
        ...base,
        type: "tool_update",
        toolCallId: payload.value.toolCallId,
        status: payload.value.status,
        output: decodeOptionalJson(payload.value.outputJson),
      })
    case "failed":
      return Object.freeze({ ...base, type: "session_error", message: payload.value.message })
    case "permissionRequested":
      if (payload.value.permission === undefined) {
        return Object.freeze({ ...base, type: "unknown", wireType: "missing_permission", payload: payload.value })
      }
      return Object.freeze({
        ...base,
        type: "permission_requested",
        permission: permissionView(payload.value.permission),
      })
    case "permissionResolved":
      return Object.freeze({
        ...base,
        type: "permission_resolved",
        permissionId: payload.value.permissionId,
        decision: permissionDecisionView(payload.value.decision),
      })
    case "questionRequested":
      if (payload.value.question === undefined) {
        return Object.freeze({ ...base, type: "unknown", wireType: "missing_question", payload: payload.value })
      }
      return Object.freeze({
        ...base,
        type: "question_requested",
        question: questionView(payload.value.question),
      })
    case "questionResolved":
      return Object.freeze({
        ...base,
        type: "question_resolved",
        questionId: payload.value.questionId,
        answers: Object.freeze(payload.value.answers.map((answer) => Object.freeze([...answer.values]))),
        rejected: payload.value.rejected,
      })
    case "unknown":
      return Object.freeze({
        ...base,
        type: "unknown",
        wireType: payload.value.wireType,
        payload: decodeOptionalJson(payload.value.payloadJson),
      })
    case undefined:
      return Object.freeze({ ...base, type: "unknown", wireType: "missing_payload", payload: undefined })
  }
}

/**
 * Decode the legacy uab.v1 JSON event boundary into the stable OMAI SDK event
 * algebra. Unknown events remain observable so an older SDK never drops data.
 */
export function parseSessionEvent(event: WireEvent): OMAISessionEvent {
  const base = Object.freeze({
    sequence: event.seq,
    occurredAtUnixMillis: event.unixMillis,
    workspaceId: event.workspaceId,
    sessionId: event.sessionId,
    runtimeId: event.runtimeId,
    externalSessionId: event.externalSessionId,
  })
  const envelope = asRecord(decodeJsonBytes(event.payloadJson))
  const payload = envelope === undefined ? undefined : envelope.payload
  const object = asRecord(payload)

  if (event.type === "session.status") {
    const state = object?.status
    if (state === "running" || state === "idle") {
      return Object.freeze({ ...base, type: "session_state", state })
    }
  }
  if (event.type === "session.error") {
    const message = object?.message
    if (typeof message === "string") {
      return Object.freeze({ ...base, type: "session_error", message })
    }
  }
  if (event.type === "acp.session/update") {
    const update = asRecord(object?.update)
    if (update === undefined) {
      return Object.freeze({ ...base, type: "unknown", wireType: event.type, payload })
    }
    const kind = update?.sessionUpdate
    if (kind === "agent_message_chunk" || kind === "agent_thought_chunk") {
      const content = asRecord(update.content)
      if (typeof update.messageId === "string" && typeof content?.text === "string") {
        return Object.freeze({
          ...base,
          type: "message_delta",
          messageId: update.messageId,
          channel: kind === "agent_message_chunk" ? "answer" : "reasoning",
          delta: content.text,
        })
      }
    }
    if (kind === "tool_call" && typeof update.toolCallId === "string") {
      return Object.freeze({
        ...base,
        type: "tool_call",
        toolCallId: update.toolCallId,
        toolName: typeof update.title === "string" ? update.title : "tool",
        status: typeof update.status === "string" ? update.status : "pending",
        arguments: update.rawInput,
      })
    }
    if (kind === "tool_call_update" && typeof update.toolCallId === "string") {
      return Object.freeze({
        ...base,
        type: "tool_update",
        toolCallId: update.toolCallId,
        status: typeof update.status === "string" ? update.status : "unknown",
        output: update.rawOutput,
      })
    }
  }
  return Object.freeze({ ...base, type: "unknown", wireType: event.type, payload })
}

function asRecord(value: unknown): Readonly<Record<string, unknown>> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined
  }
  return value as Readonly<Record<string, unknown>>
}

function decodeOptionalJson(bytes: Uint8Array): unknown {
  if (bytes.byteLength === 0) return undefined
  try {
    return decodeJsonBytes(bytes)
  } catch {
    return bytes.slice()
  }
}
