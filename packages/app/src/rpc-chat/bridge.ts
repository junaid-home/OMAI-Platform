import type { OMAIConversationMessage, OMAISessionEvent } from "@omai/sdk-web"
import type { Message, Part, SessionStatus, ToolState } from "@opencode-ai/sdk/v2/client"
import type { ServerSession } from "@/context/server-session"
import { omaiClient } from "@/omai/client"

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

const cmp = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0)

export type ChatTurn = {
  messageID: string
  parentID: string
  created: number
  completed?: number
  textPartID: string
  reasoningPartID: string
}

export type ChatState = {
  sessionID: string
  root: string
  userMessages: { id: string; created: number }[]
  turns: Map<string, ChatTurn>
  optimisticIDs: Set<string>
  lastSeq: bigint
}

export function createChatState(sessionID: string, root: string): ChatState {
  return {
    sessionID,
    root,
    userMessages: [],
    turns: new Map(),
    optimisticIDs: new Set(),
    lastSeq: 0n,
  }
}

export function addUserMessage(state: ChatState, messageID: string, created: number) {
  if (state.userMessages.some((message) => message.id === messageID)) return
  state.userMessages.push({ id: messageID, created })
  state.userMessages.sort((a, b) => a.created - b.created)
}

export function removeUserMessage(state: ChatState, messageID: string) {
  state.userMessages = state.userMessages.filter((message) => message.id !== messageID)
  state.optimisticIDs.delete(messageID)
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

function latestUserBefore(state: ChatState, at: number): string | undefined {
  let parent: string | undefined
  for (const message of state.userMessages) {
    if (message.created > at) break
    parent = message.id
  }
  return parent
}

function userMessageFor(state: ChatState, id: string, created: number): Message {
  return {
    id,
    sessionID: state.sessionID,
    role: "user",
    time: { created },
    agent: "",
    model: { providerID: "", modelID: "" },
  }
}

function assistantMessageFor(state: ChatState, turn: ChatTurn): Message {
  return {
    id: turn.messageID,
    sessionID: state.sessionID,
    role: "assistant",
    time: {
      created: turn.created,
      ...(turn.completed === undefined ? {} : { completed: turn.completed }),
    },
    parentID: turn.parentID,
    modelID: "",
    providerID: "",
    mode: "prompt",
    agent: "",
    path: { cwd: state.root, root: state.root },
    cost: 0,
    tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } },
    finish: "end_turn",
  }
}

function textPartFor(state: ChatState, messageID: string, id: string, text: string, start?: number): Part {
  return start === undefined
    ? { id, sessionID: state.sessionID, messageID, type: "text", text }
    : { id, sessionID: state.sessionID, messageID, type: "text", text, time: { start } }
}

function reasoningPartFor(state: ChatState, messageID: string, id: string, text: string, start: number): Part {
  return { id, sessionID: state.sessionID, messageID, type: "reasoning", text, time: { start } }
}

function hasMessage(session: ServerSession, sessionID: string, messageID: string) {
  return (session.data.message[sessionID] ?? []).some((message) => message.id === messageID)
}

function mergeMessages(current: readonly Message[], incoming: readonly Message[]) {
  const items = new Map(current.map((message) => [message.id, message] as const))
  for (const message of incoming) items.set(message.id, message)
  return [...items.values()].sort((a, b) => a.time.created - b.time.created || cmp(a.id, b.id))
}

function sortParts(parts: Part[]) {
  const start = (part: Part) => (part as { time?: { start?: number } }).time?.start ?? 0
  return parts.sort((a, b) => start(a) - start(b) || cmp(a.id, b.id))
}

function insertMessage(session: ServerSession, sessionID: string, message: Message) {
  session.set("message", sessionID, mergeMessages(session.data.message[sessionID] ?? [], [message]))
}

function upsertPart(session: ServerSession, messageID: string, part: Part) {
  const current = session.data.part[messageID] ?? []
  const index = current.findIndex((item) => item.id === part.id)
  const next = current.slice()
  if (index === -1) next.push(part)
  else next[index] = part
  session.set("part", messageID, sortParts(next))
}

function turnFor(state: ChatState, messageID: string, at: number): ChatTurn {
  const existing = state.turns.get(messageID)
  if (existing) return existing
  const turn: ChatTurn = {
    messageID,
    parentID: latestUserBefore(state, at) ?? "",
    created: at,
    textPartID: `${messageID}:text`,
    reasoningPartID: `${messageID}:reasoning`,
  }
  state.turns.set(messageID, turn)
  return turn
}

function completeTurns(session: ServerSession, state: ChatState, at: number) {
  for (const turn of state.turns.values()) {
    if (turn.completed !== undefined) continue
    turn.completed = at
    const current = session.data.message[state.sessionID] ?? []
    const next = current.map((message) =>
      message.role === "assistant" && message.id === turn.messageID
        ? { ...message, time: { ...message.time, completed: at } }
        : message,
    )
    session.set("message", state.sessionID, next)
  }
}

function appendText(
  session: ServerSession,
  state: ChatState,
  messageID: string,
  partID: string,
  type: "text" | "reasoning",
  delta: string,
  at: number,
) {
  const current = session.data.part[messageID] ?? []
  const index = current.findIndex((part) => part.id === partID)
  const next = current.slice()
  if (index === -1) {
    next.push(
      type === "reasoning"
        ? reasoningPartFor(state, messageID, partID, delta, at)
        : textPartFor(state, messageID, partID, delta, at),
    )
  } else {
    const part = next[index]
    if (part.type !== "text" && part.type !== "reasoning") return
    next[index] = { ...part, text: part.text + delta }
  }
  session.set("part", messageID, sortParts(next))
}

function contentText(content: unknown): string | undefined {
  if (!Array.isArray(content)) return undefined
  const first = asRecord(content[0])
  const inner = asRecord(first?.content)
  if (inner && typeof inner.text === "string") return inner.text
  return undefined
}

function parseToolInput(update: Record<string, unknown>): { input: Record<string, unknown>; raw: string } {
  const candidates = [update.rawInput, update.arguments]
  for (const candidate of candidates) {
    if (candidate === undefined) continue
    if (typeof candidate === "string") {
      try {
        const parsed: unknown = JSON.parse(candidate)
        if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
          return { input: parsed as Record<string, unknown>, raw: candidate }
        }
      } catch {
        // ignore malformed arguments
      }
    }
    if (typeof candidate === "object" && candidate !== null && !Array.isArray(candidate)) {
      return { input: candidate as Record<string, unknown>, raw: JSON.stringify(candidate) }
    }
  }
  return { input: {}, raw: "" }
}

function applyToolUpdate(session: ServerSession, state: ChatState, update: Record<string, unknown>, at: number) {
  const toolCallId = typeof update.toolCallId === "string" ? update.toolCallId : ""
  if (!toolCallId) return
  const messageId = typeof update.messageId === "string" ? update.messageId : "assistant"
  const turn = turnFor(state, messageId, at)
  if (!hasMessage(session, state.sessionID, turn.messageID))
    insertMessage(session, state.sessionID, assistantMessageFor(state, turn))

  const status = typeof update.status === "string" ? update.status : "pending"
  const { input, raw } = parseToolInput(update)
  const tool = typeof update.title === "string" && update.title ? update.title : "tool"
  const streamed = contentText(update.content)
  const rawOutput = asRecord(update.rawOutput)
  const output =
    typeof streamed === "string" ? streamed : typeof rawOutput?.output === "string" ? rawOutput.output : undefined
  const start = turn.created

  const toolState: ToolState = (() => {
    switch (status) {
      case "completed":
        return {
          status: "completed",
          input,
          output: output ?? "",
          title: tool,
          metadata: {},
          time: { start, end: at },
        }
      case "failed":
      case "error":
        return {
          status: "error",
          input,
          error: output ?? "Tool call failed",
          time: { start, end: at },
        }
      case "in_progress":
      case "running":
        return {
          status: "running",
          input,
          ...(output === undefined ? {} : { metadata: { output } }),
          time: { start },
        }
      case "pending":
      case "queued":
      default:
        return { status: "pending", input, raw }
    }
  })()

  upsertPart(session, turn.messageID, {
    id: toolCallId,
    sessionID: state.sessionID,
    messageID: turn.messageID,
    type: "tool",
    callID: toolCallId,
    tool,
    state: toolState,
  })
}

function removeOptimistic(session: ServerSession, state: ChatState, messageID: string, realID: string) {
  removeUserMessage(state, messageID)
  session.optimistic.remove({ sessionID: state.sessionID, messageID })
  const next = (session.data.message[state.sessionID] ?? []).map((message) =>
    message.role === "assistant" && message.parentID === messageID ? { ...message, parentID: realID } : message,
  )
  session.set("message", state.sessionID, next)
}

// Drops an optimistic user message once the backend's real copy of it exists
// (same text, close timestamp) so the timeline does not show a duplicate row.
// Assistant turns that referenced the optimistic id are re-linked to the real id.
export function dedupeOptimisticUserMessage(session: ServerSession, state: ChatState, realID: string) {
  const real = (session.data.message[state.sessionID] ?? []).find((message) => message.id === realID)
  if (!real || real.role !== "user") return
  const realText = (session.data.part[realID] ?? []).find((part) => part.type === "text")?.text ?? ""
  if (!realText) return
  for (const messageID of [...state.optimisticIDs]) {
    if (messageID === realID) continue
    const optimistic = (session.data.message[state.sessionID] ?? []).find((message) => message.id === messageID)
    if (!optimistic || optimistic.role !== "user") continue
    const text = (session.data.part[messageID] ?? []).find((part) => part.type === "text")?.text ?? ""
    if (text !== realText || Math.abs(optimistic.time.created - real.time.created) > 60_000) continue
    removeOptimistic(session, state, messageID, realID)
    return
  }
}

function applyChunk(
  session: ServerSession,
  state: ChatState,
  update: Record<string, unknown>,
  kind: string,
  at: number,
) {
  const content = asRecord(update.content)
  if (typeof content?.text !== "string") return
  const messageID =
    typeof update.messageId === "string" ? update.messageId : kind === "agent_thought_chunk" ? "thought" : "assistant"

  if (kind === "user_message_chunk") {
    if (hasMessage(session, state.sessionID, messageID)) return
    insertMessage(session, state.sessionID, userMessageFor(state, messageID, at))
    appendText(session, state, messageID, `${messageID}:text`, "text", content.text, at)
    addUserMessage(state, messageID, at)
    dedupeOptimisticUserMessage(session, state, messageID)
    return
  }

  const turn = turnFor(state, messageID, at)
  const existing = session.data.message[state.sessionID]?.find((message) => message.id === turn.messageID)
  if (existing) {
    if (existing.role !== "assistant" || existing.time.completed !== undefined) return
  } else {
    insertMessage(session, state.sessionID, assistantMessageFor(state, turn))
  }
  const partID = kind === "agent_thought_chunk" ? turn.reasoningPartID : turn.textPartID
  appendText(
    session,
    state,
    turn.messageID,
    partID,
    kind === "agent_thought_chunk" ? "reasoning" : "text",
    content.text,
    at,
  )
}

export function applyEvent(ev: OMAISessionEvent, state: ChatState, session: ServerSession) {
  const at = Number(ev.occurredAtUnixMillis)
  switch (ev.type) {
    case "session_state": {
      const running = ev.state === "running"
      const status: SessionStatus = running ? { type: "busy" } : { type: "idle" }
      session.set("session_status", state.sessionID, status)
      if (!running) completeTurns(session, state, at)
      return
    }
    case "message_delta":
      applyChunk(
        session,
        state,
        { messageId: ev.messageId, content: { text: ev.delta } },
        ev.channel === "reasoning" ? "agent_thought_chunk" : "agent_message_chunk",
        at,
      )
      return
    case "tool_call":
      applyToolUpdate(
        session,
        state,
        {
          toolCallId: ev.toolCallId,
          title: ev.toolName,
          status: ev.status,
          rawInput: ev.arguments,
        },
        at,
      )
      return
    case "tool_update":
      applyToolUpdate(session, state, { toolCallId: ev.toolCallId, status: ev.status, rawOutput: ev.output }, at)
      return
    case "session_error": {
      session.set("session_status", state.sessionID, { type: "idle" })
      completeTurns(session, state, at)
      return
    }
    case "permission_requested": {
      const value = ev.permission
      const current = session.data.permission[state.sessionID] ?? []
      const request = {
        id: value.id,
        sessionID: value.sessionId,
        permission: value.permission,
        patterns: [...value.patterns],
        metadata: { ...value.metadata },
        always: [...value.always],
        ...(value.tool === undefined ? {} : { tool: { messageID: value.tool.messageId, callID: value.tool.callId } }),
      }
      session.set(
        "permission",
        state.sessionID,
        [...current.filter((item) => item.id !== request.id), request].sort((left, right) => cmp(left.id, right.id)),
      )
      return
    }
    case "permission_resolved":
      session.set(
        "permission",
        state.sessionID,
        (session.data.permission[state.sessionID] ?? []).filter((request) => request.id !== ev.permissionId),
      )
      return
    case "question_requested": {
      const value = ev.question
      const current = session.data.question[state.sessionID] ?? []
      const request = {
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
      session.set(
        "question",
        state.sessionID,
        [...current.filter((item) => item.id !== request.id), request].sort((left, right) => cmp(left.id, right.id)),
      )
      return
    }
    case "question_resolved":
      session.set(
        "question",
        state.sessionID,
        (session.data.question[state.sessionID] ?? []).filter((request) => request.id !== ev.questionId),
      )
      return
    default:
      return
  }
}

export function restoreHistory(session: ServerSession, state: ChatState, listed: readonly OMAIConversationMessage[]) {
  for (const message of listed) {
    if (message.role !== "user") continue
    const created = Number(message.createdAtUnixMillis)
    addUserMessage(state, message.id, created)
    if (!hasMessage(session, state.sessionID, message.id)) {
      insertMessage(session, state.sessionID, userMessageFor(state, message.id, created))
    }
    if (message.text)
      upsertPart(session, message.id, textPartFor(state, message.id, `${message.id}:text`, message.text))
  }
  for (const message of listed) {
    if (message.role !== "assistant" || !message.text) continue
    const created = Number(message.createdAtUnixMillis)
    if (hasMessage(session, state.sessionID, message.id)) continue
    const completed = listed.some((other) => Number(other.createdAtUnixMillis) > created) ? created : undefined
    const turn: ChatTurn = {
      messageID: message.id,
      parentID: latestUserBefore(state, created) ?? "",
      created,
      completed,
      textPartID: `${message.id}:text`,
      reasoningPartID: `${message.id}:reasoning`,
    }
    insertMessage(session, state.sessionID, assistantMessageFor(state, turn))
    const isThought = message.kind.includes("thought") || message.kind.includes("reasoning")
    upsertPart(
      session,
      message.id,
      isThought
        ? reasoningPartFor(state, message.id, `${message.id}:reasoning`, message.text, created)
        : textPartFor(state, message.id, `${message.id}:text`, message.text),
    )
  }
}

export async function runSubscription(input: {
  runtimeId: string
  sessionId: string
  workspaceId: string
  state: ChatState
  session: ServerSession
  onStatus?: (connected: boolean) => void
  signal: AbortSignal
}) {
  let cursor = input.state.lastSeq
  while (!input.signal.aborted) {
    try {
      const stream = omaiClient.sessions.subscribe(
        {
          runtimeId: input.runtimeId,
          sessionId: input.sessionId,
          workspaceId: input.workspaceId,
          since: cursor,
        },
        { signal: input.signal },
      )
      for await (const ev of stream) {
        if (input.signal.aborted) break
        cursor = ev.sequence
        input.state.lastSeq = ev.sequence
        applyEvent(ev, input.state, input.session)
      }
    } catch {
      if (input.signal.aborted) return
      input.onStatus?.(false)
      await sleep(1000)
    }
  }
}
