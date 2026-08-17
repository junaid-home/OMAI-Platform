import {
  createContext,
  createEffect,
  createMemo,
  createResource,
  on,
  onCleanup,
  useContext,
  type ParentProps,
} from "solid-js"
import { createStore } from "solid-js/store"
import { useParams } from "@solidjs/router"
import type { OMAIConversationMessage, RuntimeInstallation } from "@omai/sdk-web"
import { useServerSync } from "@/context/server-sync"
import { listRuntimes, omaiClient } from "@/omai/client"
import { persisted } from "@/utils/persist"
import { uuid } from "@/utils/uuid"
import { rpcSessionsPersist, selectionPersist, type CatalogSelection, type RpcSessionMeta } from "./catalog"
import {
  addUserMessage,
  createChatState,
  dedupeOptimisticUserMessage,
  restoreHistory,
  runSubscription,
  type ChatState,
} from "./bridge"

type Bridge = {
  state: ChatState
  controller: AbortController
}

export type RpcChatSendInput = {
  text: string
  title: string
  root: string
  sessionID?: string
}

export type RpcChat = {
  runtimes: () => RuntimeInstallation[]
  selection: () => CatalogSelection | undefined
  selectedRuntime: () => RuntimeInstallation | undefined
  active: () => boolean
  select: (selection: CatalogSelection) => void
  sessions: () => RpcSessionMeta[]
  sessionMeta: (id: string) => RpcSessionMeta | undefined
  send: (input: RpcChatSendInput) => Promise<{ sessionId: string; workspaceId: string; isNew: boolean }>
  stop: (sessionID: string) => Promise<void>
  registerUserMessage: (sessionID: string, messageID: string, created: number) => void
  restore: (sessionID: string) => Promise<void>
  rememberSession: (meta: RpcSessionMeta) => void
  forgetSession: (id: string) => void
}

const RpcChatContext = createContext<RpcChat>()

export function useRpcChat() {
  return useContext(RpcChatContext)
}

export function RpcChatProvider(props: ParentProps) {
  const serverSync = useServerSync()

  const [selectionStore, setSelectionStore] = persisted(
    selectionPersist(),
    createStore<{ selection?: CatalogSelection }>({}),
  )
  const [sessionStore, setSessionStore] = persisted(
    rpcSessionsPersist(),
    createStore<{ sessions: RpcSessionMeta[] }>({ sessions: [] }),
  )

  const [runtimesResource] = createResource(() => listRuntimes())
  const [platformSessionsResource] = createResource(async () => {
    const projects = await omaiClient.projects.list()
    const values = await Promise.all(
      projects.map(async (project) => {
        const sessions = await omaiClient.sessions.list(project.id)
        return sessions.map(
          (session): RpcSessionMeta => ({
            id: session.id,
            externalId: session.externalSessionId,
            projectId: session.projectId,
            workspaceId: session.workspaceId,
            runtimeId: session.runtimeId,
            providerId: session.providerId,
            modelId: session.modelId,
            root: project.root,
            title: session.title,
            updatedUnixMillis: Number(session.updatedUnixMillis || session.createdUnixMillis),
          }),
        )
      }),
    )
    return values.flat().sort((left, right) => right.updatedUnixMillis - left.updatedUnixMillis)
  })

  const bridges = new Map<string, Bridge>()
  const states = new Map<string, ChatState>()

  const select = (selection: CatalogSelection) => setSelectionStore("selection", selection)

  const selection = () => selectionStore.selection
  const runtimes = () => runtimesResource()?.runtimes ?? []
  const selectedRuntime = createMemo(() => {
    const selected = selection()
    if (!selected) return undefined
    return runtimes().find((runtime) => runtime.id === selected.runtime_id && runtime.enabled)
  })
  const active = () => selectedRuntime() !== undefined

  const sessions = () => sessionStore.sessions
  const sessionMeta = (id: string) => sessions().find((session) => session.id === id)

  const upsertSession = (meta: RpcSessionMeta) => {
    setSessionStore("sessions", (sessions) => {
      const index = sessions.findIndex((session) => session.id === meta.id)
      if (index === -1) return [...sessions, meta]
      return sessions.map((session, i) => (i === index ? { ...session, ...meta } : session))
    })
  }

  const rememberSession = (meta: RpcSessionMeta) => {
    upsertSession(meta)
    serverSync().session.remember({
      id: meta.id,
      slug: meta.externalId || meta.id,
      projectID: meta.projectId,
      workspaceID: meta.workspaceId,
      directory: meta.root,
      title: meta.title,
      version: "1.0.0",
      time: { created: meta.updatedUnixMillis, updated: meta.updatedUnixMillis },
    })
  }

  const forgetSession = (id: string) => {
    bridges.get(id)?.controller.abort()
    bridges.delete(id)
    states.delete(id)
    setSessionStore("sessions", (sessions) => sessions.filter((session) => session.id !== id))
  }

  createEffect(() => {
    const loaded = platformSessionsResource()
    if (!loaded) return
    setSessionStore("sessions", loaded)
    loaded.forEach(rememberSession)
  })

  const stateFor = (sessionID: string) => {
    const cached = states.get(sessionID)
    if (cached) return cached
    const meta = sessionMeta(sessionID)
    const state = createChatState(sessionID, meta?.root ?? "")
    states.set(sessionID, state)
    return state
  }

  const ensureSubscription = (meta: RpcSessionMeta, state: ChatState) => {
    if (bridges.has(meta.id)) return
    const controller = new AbortController()
    bridges.set(meta.id, { state, controller })
    void runSubscription({
      runtimeId: meta.runtimeId,
      sessionId: meta.id,
      workspaceId: meta.workspaceId,
      state,
      session: serverSync().session,
      signal: controller.signal,
    })
  }

  const dedupeOptimistic = (sessionID: string, listed: readonly OMAIConversationMessage[]) => {
    const session = serverSync().session
    for (const message of listed) {
      if (message.role !== "user") continue
      dedupeOptimisticUserMessage(session, stateFor(sessionID), message.id)
    }
  }

  const restore = async (sessionID: string) => {
    const meta = sessionMeta(sessionID)
    if (!meta) return
    rememberSession(meta)
    const state = stateFor(sessionID)
    const listed = await omaiClient.sessions.listMessages(sessionID)

    restoreHistory(serverSync().session, state, listed)
    dedupeOptimistic(sessionID, listed)
    const session = serverSync().session
    for (const message of session.data.message[sessionID] ?? []) {
      if (message.role !== "user" || state.userMessages.some((item) => item.id === message.id)) continue
      addUserMessage(state, message.id, message.time.created)
      state.optimisticIDs.add(message.id)
    }
    ensureSubscription(meta, state)
  }

  const send = async (input: RpcChatSendInput) => {
    const existing = input.sessionID ? sessionMeta(input.sessionID) : undefined
    const selected = selectedRuntime()
    const route =
      selection() ??
      (existing
        ? {
            runtime_id: existing.runtimeId,
            provider_id: existing.providerId,
            id: existing.modelId,
            name: existing.modelId,
          }
        : undefined)
    if (!selected || !route?.provider_id || !route.id) throw new Error("No executable OMAI model route selected")
    // The dropdown selection drives the runtime. Switching runtimes mid-session starts a
    // fresh session on the new runtime because the backend keys sessions by runtime.
    const switching = existing !== undefined && existing.runtimeId !== selected.id
    const externalId = existing && !switching ? existing.externalId : `web-${uuid()}`
    const response = await omaiClient.sessions.send({
      runtimeId: selected.id,
      providerId: route.provider_id,
      modelId: route.id,
      externalSessionId: externalId,
      root: input.root,
      text: input.text,
      title: input.title,
    })
    const meta: RpcSessionMeta = {
      id: response.sessionId,
      externalId,
      projectId: response.projectId,
      workspaceId: response.workspaceId,
      runtimeId: selected.id,
      providerId: route.provider_id,
      modelId: route.id,
      root: input.root,
      title: input.title,
      updatedUnixMillis: Date.now(),
    }
    upsertSession(meta)
    rememberSession(meta)
    return {
      sessionId: response.sessionId,
      workspaceId: response.workspaceId,
      isNew: existing === undefined || switching,
    }
  }

  const stop = async (sessionID: string) => {
    const meta = sessionMeta(sessionID)
    if (!meta) return
    await omaiClient.sessions.cancel({ sessionId: meta.id })
  }

  const registerUserMessage = (sessionID: string, messageID: string, created: number) => {
    const state = stateFor(sessionID)
    addUserMessage(state, messageID, created)
    state.optimisticIDs.add(messageID)
  }

  onCleanup(() => {
    for (const bridge of bridges.values()) bridge.controller.abort()
  })

  const value: RpcChat = {
    runtimes,
    selection,
    selectedRuntime,
    active,
    select,
    sessions,
    sessionMeta,
    send,
    stop,
    registerUserMessage,
    restore,
    rememberSession,
    forgetSession,
  }

  return <RpcChatContext.Provider value={value}>{props.children}</RpcChatContext.Provider>
}

export function RpcChatSessionSeeder() {
  const params = useParams<{ serverKey: string; id: string }>()
  const rpcChat = useRpcChat()

  createEffect(
    on(
      () => params.id,
      (id) => {
        if (!id || !rpcChat) return
        if (!rpcChat.sessionMeta(id)) return
        void rpcChat.restore(id)
      },
    ),
  )

  return null
}
