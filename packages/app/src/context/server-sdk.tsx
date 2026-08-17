import type { Event } from "@opencode-ai/sdk/v2/client"
import { createSimpleContext } from "@omai/ui/context"
import { createGlobalEmitter } from "@solid-primitives/event-bus"
import { type Accessor, createMemo, onCleanup } from "solid-js"
import { createSdkForServer } from "@/utils/server"
import { useLanguage } from "./language"
import { usePlatform } from "./platform"
import { ServerConnection, useServer } from "./server"
import { createRefCountMap } from "@/utils/refcount"
import { useGlobal } from "./global"
import { ServerScope } from "@/utils/server-scope"

type QueuedServerEvent = { directory: string; payload: Event }

const coalescedKey = (event: QueuedServerEvent) => {
  if (event.payload.type === "lsp.updated") return `lsp.updated:${event.directory}`
  if (event.payload.type === "message.part.updated") {
    const part = event.payload.properties.part
    return `message.part.updated:${event.directory}:${part.messageID}:${part.id}`
  }
  return undefined
}

export function enqueueServerEvent(queue: QueuedServerEvent[], event: QueuedServerEvent) {
  const key = coalescedKey(event)
  const previous = queue[queue.length - 1]
  if (key && previous && coalescedKey(previous) === key) {
    queue[queue.length - 1] = event
    return false
  }
  queue.push(event)
  return true
}

export function coalesceServerEvents(events: QueuedServerEvent[]) {
  const output: QueuedServerEvent[] = []
  events.forEach((event) => {
    if (event.payload.type !== "message.part.delta") {
      output.push(event)
      return
    }
    const props = event.payload.properties
    const previous = output[output.length - 1]
    if (
      !previous ||
      previous.payload.type !== "message.part.delta" ||
      previous.directory !== event.directory ||
      previous.payload.properties.messageID !== props.messageID ||
      previous.payload.properties.partID !== props.partID ||
      previous.payload.properties.field !== props.field
    ) {
      output.push({
        directory: event.directory,
        payload: { ...event.payload, properties: { ...props } },
      })
      return
    }
    output[output.length - 1] = {
      directory: event.directory,
      payload: {
        ...event.payload,
        properties: { ...props, delta: previous.payload.properties.delta + props.delta },
      },
    }
  })
  return output
}

export function resumeStreamAfterPageShow(event: PageTransitionEvent, start: () => unknown) {
  if (!event.persisted) return
  start()
}

function createServerSdkContextBase(server: ServerConnection.Any, scope: ServerScope) {
  const platform = usePlatform()
  const emitter = createGlobalEmitter<{
    [key: string]: Event
  }>()
  // OMAI session, workspace, terminal and LSP streams are owned by their
  // typed contexts. Keep this compatibility emitter for UI-local events, but
  // never open OpenCode's global SSE transport.
  const start = () => Promise.resolve()

  const sdk = createSdkForServer({
    server: server.http,
    fetch: platform.fetch,
    throwOnError: true,
  })

  return {
    server,
    scope,
    url: server.http.url,
    client: sdk,
    event: {
      on: emitter.on.bind(emitter),
      listen: emitter.listen.bind(emitter),
      start,
    },
    createClient(opts: Omit<Parameters<typeof createSdkForServer>[0], "server" | "fetch">) {
      return createSdkForServer({
        server: server.http,
        fetch: platform.fetch,
        ...opts,
      })
    },
  }
}

type ServerSDKBase = ReturnType<typeof createServerSdkContextBase>
export type ServerSDK = ServerSDKBase & {
  ensureDirSdkContext: (directory: string) => ReturnType<typeof createDirSdkContext>
}

export function createServerSdkContext(server: ServerConnection.Any, scope: ServerScope): ServerSDK {
  const sdk = createServerSdkContextBase(server, scope)
  return Object.assign(sdk, {
    ensureDirSdkContext: createRefCountMap((dir) => createDirSdkContext(dir, sdk)),
  })
}

export const { use: useServerSDK, provider: ServerSDKProvider } = createSimpleContext({
  name: "ServerSDK",
  // Returns an accessor so the resolved server can change reactively (e.g. a
  // /new-session draft retargeting its server) without re-instantiating the subtree.
  init: (props: { server?: Accessor<ServerConnection.Any | undefined> }) => {
    const global = useGlobal()
    const language = useLanguage()
    const server = useServer()

    return createMemo<ServerSDK>(() => {
      const conn = props.server?.() ?? server.current
      if (!conn) throw new Error(language.t("error.serverSDK.noServerAvailable"))
      return global.ensureServerCtx(conn).sdk
    })
  },
})

type SDKEventMap = {
  [key in Event["type"]]: Extract<Event, { type: key }>
}

function createDirSdkContext(directory: string, serverSDK: ServerSDKBase) {
  const client = serverSDK.createClient({
    directory,
    throwOnError: true,
  })

  const emitter = createGlobalEmitter<SDKEventMap>()

  const unsub = serverSDK.event.on(directory, (event) => {
    emitter.emit(event.type, event)
  })
  onCleanup(unsub)

  return {
    scope: serverSDK.scope,
    directory,
    client,
    event: emitter,
    get url() {
      return serverSDK.url
    },
    createClient(opts: Parameters<typeof serverSDK.createClient>[0]) {
      return serverSDK.createClient(opts)
    },
  }
}
