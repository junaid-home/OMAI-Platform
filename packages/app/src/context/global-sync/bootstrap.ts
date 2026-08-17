import type {
  Config,
  OpencodeClient,
  Path,
  PermissionRequest,
  Project,
  ProviderAuthResponse,
  QuestionRequest,
  ReferenceInfo,
  Session,
} from "@opencode-ai/sdk/v2/client"
import { retry } from "@omai/platform-utils/retry"
import { batch } from "solid-js"
import { produce, reconcile, type SetStoreFunction, type Store } from "solid-js/store"
import type { State, VcsCache } from "./types"
import type { ServerSession } from "../server-session"
import { cmp } from "./utils"
import { QueryClient, queryOptions } from "@tanstack/solid-query"
import { loadMcpQuery, loadMcpResourcesQuery } from "../server-sync"
import { NormalizedProviderListResponse } from "@omai/session-ui/context"
import { ScopedKey, type ServerScope } from "@/utils/server-scope"
import { omaiClient, resolveOMAIWorkspace } from "@/omai/client"
import { asOMAIError } from "@omai/sdk-web"
import { Code } from "@connectrpc/connect"
import { omaiProjectView } from "@/omai/projects"
import { loadOMAIProviderView } from "@/omai/providers"
import { listOMAIPermissionsForRoot, listOMAIQuestionsForRoot } from "@/omai/interactions"

type GlobalStore = {
  ready: boolean
  path: Path
  project: Project[]
  provider: NormalizedProviderListResponse
  provider_auth: ProviderAuthResponse
  config: Config
  reload: undefined | "pending" | "complete"
}

function waitForPaint() {
  return new Promise<void>((resolve) => {
    let done = false
    const finish = () => {
      if (done) return
      done = true
      resolve()
    }
    const timer = setTimeout(finish, 50)
    if (typeof requestAnimationFrame !== "function") return
    requestAnimationFrame(() => {
      setTimeout(() => {
        clearTimeout(timer)
        finish()
      }, 0)
    })
  })
}

function errors(list: PromiseSettledResult<unknown>[]) {
  return list.filter((item): item is PromiseRejectedResult => item.status === "rejected").map((item) => item.reason)
}

const providerRev = new Map<string, number>()

export function clearProviderRev(scope: ServerScope, directory: string) {
  providerRev.delete(ScopedKey.from(scope, directory))
}

function runAll(list: Array<() => Promise<unknown>>) {
  return Promise.allSettled(list.map((item) => item()))
}

export const loadGlobalConfigQuery = (scope: ServerScope, sdk: OpencodeClient) =>
  queryOptions({
    queryKey: [scope, "config"],
    queryFn: () => retry(() => sdk.global.config.get().then((x) => x.data!)),
  })

export const loadProjectsQuery = (scope: ServerScope, sdk: OpencodeClient) =>
  queryOptions({
    queryKey: [scope, "project"],
    queryFn: async () => {
      void sdk
      return (await omaiClient.projects.list())
        .map(omaiProjectView)
        .filter((project) => !!project.worktree)
        .sort((a, b) => cmp(a.id, b.id))
    },
  })

export async function bootstrapGlobal(input: {
  serverSDK: OpencodeClient
  scope: ServerScope
  setGlobalStore: SetStoreFunction<GlobalStore>
  queryClient: QueryClient
}) {
  const slow = [
    () => input.queryClient.fetchQuery(loadGlobalConfigQuery(input.scope, input.serverSDK)),
    () => input.queryClient.fetchQuery(loadProvidersQuery(input.scope, null, input.serverSDK)),
    () => input.queryClient.fetchQuery(loadPathQuery(input.scope, null, input.serverSDK)),
    () =>
      input.queryClient
        .fetchQuery(loadProjectsQuery(input.scope, input.serverSDK))
        .then((data) => input.setGlobalStore("project", data)),
  ]
  await runAll(slow)
}

function groupBySession<T extends { id: string; sessionID: string }>(input: T[]) {
  return input.reduce<Record<string, T[]>>((acc, item) => {
    if (!item?.id || !item.sessionID) return acc
    const list = acc[item.sessionID]
    if (list) list.push(item)
    if (!list) acc[item.sessionID] = [item]
    return acc
  }, {})
}

function projectID(directory: string, projects: Project[]) {
  return projects.find((project) => project.worktree === directory || project.sandboxes?.includes(directory))?.id
}

function mergeSession(setStore: SetStoreFunction<State>, session: Session) {
  setStore("session", (list) => {
    const next = list.slice()
    const idx = next.findIndex((item) => item.id >= session.id)
    if (idx === -1) return [...next, session]
    if (next[idx]?.id === session.id) {
      next[idx] = session
      return next
    }
    next.splice(idx, 0, session)
    return next
  })
}

function warmSessions(input: {
  ids: string[]
  store: Store<State>
  setStore: SetStoreFunction<State>
  sdk: OpencodeClient
}) {
  const known = new Set(input.store.session.map((item) => item.id))
  const ids = [...new Set(input.ids)].filter((id) => !!id && !known.has(id))
  if (ids.length === 0) return Promise.resolve()
  return Promise.all(
    ids.map((sessionID) =>
      retry(() => input.sdk.session.get({ sessionID })).then((x) => {
        const session = x.data
        if (!session?.id) return
        mergeSession(input.setStore, session)
      }),
    ),
  ).then(() => undefined)
}

export const loadProvidersQuery = (scope: ServerScope, directory: string | null, sdk: OpencodeClient) =>
  queryOptions({
    queryKey: [scope, directory, "providers"],
    queryFn: () => {
      void sdk
      return retry(loadOMAIProviderView)
    },
  })

export const loadAgentsQuery = (scope: ServerScope, directory: string | null, sdk: OpencodeClient) =>
  queryOptions({
    queryKey: [scope, directory, "agents"],
    queryFn: async () => {
      void sdk
      const response = await omaiClient.services.controlPlane.listRuntimes({})
      return response.runtimes
        .filter((runtime) => runtime.enabled)
        .map((runtime) => ({
          name: runtime.id,
          description: runtime.label,
          mode: "primary" as const,
          native: true,
          hidden: false,
          permission: [],
          options: { runtimeId: runtime.id },
        }))
    },
  })

export const loadPathQuery = (scope: ServerScope, directory: string | null, sdk: OpencodeClient) =>
  queryOptions<Path>({
    queryKey: [scope, directory, "path"],
    queryFn: () => retry(() => sdk.path.get().then((x) => x.data!)),
  })

export const loadReferencesQuery = (scope: ServerScope, directory: string, sdk: OpencodeClient) =>
  queryOptions<ReferenceInfo[]>({
    queryKey: [scope, directory, "references"] as const,
    queryFn: () => retry(() => sdk.v2.reference.list().then((x) => x.data?.data ?? [])).catch(() => []),
    placeholderData: [],
  })

export async function bootstrapDirectory(input: {
  directory: string
  scope: ServerScope
  mcp: boolean
  sdk: OpencodeClient
  store: Store<State>
  setStore: SetStoreFunction<State>
  vcsCache: VcsCache
  loadSessions: (directory: string) => Promise<void> | void
  global: {
    config: Config
    path: Path
    project: Project[]
    provider: NormalizedProviderListResponse
  }
  queryClient: QueryClient
  session?: ServerSession
  platform?: {
    agents?: () => Promise<State["agent"]>
    gitStatus?: () => Promise<{ branch: string; defaultBranch: string } | undefined>
    permissions?: () => ReturnType<typeof listOMAIPermissionsForRoot>
    questions?: () => ReturnType<typeof listOMAIQuestionsForRoot>
    providers?: () => Promise<NormalizedProviderListResponse>
  }
}) {
  const loading = input.store.status !== "complete"
  const seededProject = projectID(input.directory, input.global.project)
  const seededPath = input.global.path.directory === input.directory ? input.global.path : undefined
  if (seededProject) input.setStore("project", seededProject)
  if (seededPath) input.setStore("path", seededPath)
  if (Object.keys(input.store.config).length === 0 && Object.keys(input.global.config).length > 0) {
    input.setStore("config", reconcile(input.global.config, { merge: false }))
  }
  if (loading) input.setStore("status", "partial")

  const revKey = ScopedKey.from(input.scope, input.directory)
  const rev = (providerRev.get(revKey) ?? 0) + 1
  providerRev.set(revKey, rev)
  ;(async () => {
    const slow = [
      () => Promise.resolve(input.loadSessions(input.directory)),
      () =>
        (
          input.platform?.agents?.() ??
          input.queryClient.ensureQueryData(loadAgentsQuery(input.scope, input.directory, input.sdk))
        ).then((data) => input.setStore("agent", data)),
      () =>
        retry(() => input.sdk.config.get().then((x) => input.setStore("config", reconcile(x.data!, { merge: false })))),
      () =>
        retry(() =>
          input.sdk.session.status().then(async (x) => {
            if (!input.session) {
              input.setStore("session_status", x.data!)
              return
            }
            const statuses = x.data ?? {}
            input.session.set(
              "session_status",
              produce((draft) => {
                for (const sessionID of Object.keys(draft)) {
                  if (statuses[sessionID]) continue
                  if (input.session?.get(sessionID)?.directory === input.directory) delete draft[sessionID]
                }
              }),
            )
            for (const [sessionID, status] of Object.entries(statuses)) {
              input.session.set("session_status", sessionID, reconcile(status))
            }
            // Warm session info only after seeding statuses so a stalled session
            // fetch cannot park busy indicators behind it, mirroring how live
            // session.status events apply first and resolve info in the background.
            await Promise.all(
              Object.keys(statuses).map((sessionID) => input.session!.resolve(sessionID).catch(() => undefined)),
            )
          }),
        ),
      !seededProject &&
        (() =>
          retry(() => omaiClient.projects.resolve(input.directory)).then((project) =>
            input.setStore("project", project.id),
          )),
      !seededPath &&
        (() =>
          input.queryClient.ensureQueryData(loadPathQuery(input.scope, input.directory, input.sdk)).then((data) => {
            const next = projectID(data.directory ?? input.directory, input.global.project)
            if (next) input.setStore("project", next)
          })),
      () =>
        retry(() =>
          (
            input.platform?.gitStatus?.() ??
            resolveOMAIWorkspace(input.directory).then((workspace) => omaiClient.git.status(workspace.id))
          )
            .then((status) => {
              const next = status ? { branch: status.branch, default_branch: status.defaultBranch } : undefined
              input.setStore("vcs", next)
              input.vcsCache.setStore("value", next)
            })
            .catch((error) => {
              const code = asOMAIError(error).code
              if (code !== Code.InvalidArgument && code !== Code.NotFound) throw error
              const next = undefined
              input.setStore("vcs", next)
              input.vcsCache.setStore("value", next)
            }),
        ),
      input.mcp && (() => retry(() => input.sdk.command.list().then((x) => input.setStore("command", x.data ?? [])))),
      () => input.queryClient.fetchQuery(loadReferencesQuery(input.scope, input.directory, input.sdk)),
      () =>
        retry(() =>
          (input.platform?.permissions?.() ?? listOMAIPermissionsForRoot(input.directory)).then((permissions) => {
            const ids = permissions.map((perm) => perm.sessionID).filter((id): id is string => !!id)
            const grouped = groupBySession(
              permissions.filter((perm): perm is PermissionRequest => !!perm.id && !!perm.sessionID),
            )
            const warm = input.session
              ? Promise.all(ids.map((sessionID) => input.session!.resolve(sessionID))).then(() => undefined)
              : warmSessions({ ids, store: input.store, setStore: input.setStore, sdk: input.sdk })
            return warm.then(() =>
              batch(() => {
                const current = input.session?.data.permission ?? input.store.permission
                for (const sessionID of Object.keys(current)) {
                  if (grouped[sessionID]) continue
                  if (input.session?.get(sessionID)?.directory !== input.directory) continue
                  if (input.session) input.session.set("permission", sessionID, [])
                  if (!input.session) input.setStore("permission", sessionID, [])
                }
                for (const [sessionID, permissions] of Object.entries(grouped)) {
                  const value = reconcile(
                    permissions.filter((p) => !!p?.id).sort((a, b) => cmp(a.id, b.id)),
                    { key: "id" },
                  )
                  if (input.session) input.session.set("permission", sessionID, value)
                  if (!input.session) input.setStore("permission", sessionID, value)
                }
              }),
            )
          }),
        ),
      () =>
        retry(() =>
          (input.platform?.questions?.() ?? listOMAIQuestionsForRoot(input.directory)).then((questions) => {
            const ids = questions.map((question) => question.sessionID).filter((id): id is string => !!id)
            const grouped = groupBySession(questions.filter((q): q is QuestionRequest => !!q.id && !!q.sessionID))
            const warm = input.session
              ? Promise.all(ids.map((sessionID) => input.session!.resolve(sessionID))).then(() => undefined)
              : warmSessions({ ids, store: input.store, setStore: input.setStore, sdk: input.sdk })
            return warm.then(() =>
              batch(() => {
                const current = input.session?.data.question ?? input.store.question
                for (const sessionID of Object.keys(current)) {
                  if (grouped[sessionID]) continue
                  if (input.session?.get(sessionID)?.directory !== input.directory) continue
                  if (input.session) input.session.set("question", sessionID, [])
                  if (!input.session) input.setStore("question", sessionID, [])
                }
                for (const [sessionID, questions] of Object.entries(grouped)) {
                  const value = reconcile(
                    questions.filter((q) => !!q?.id).sort((a, b) => cmp(a.id, b.id)),
                    { key: "id" },
                  )
                  if (input.session) input.session.set("question", sessionID, value)
                  if (!input.session) input.setStore("question", sessionID, value)
                }
              }),
            )
          }),
        ),
      () => Promise.resolve(input.loadSessions(input.directory)),
      input.mcp && (() => input.queryClient.fetchQuery(loadMcpQuery(input.scope, input.directory, input.sdk))),
      input.mcp && (() => input.queryClient.fetchQuery(loadMcpResourcesQuery(input.scope, input.directory, input.sdk))),
      () =>
        (
          input.platform?.providers?.() ??
          input.queryClient.fetchQuery(loadProvidersQuery(input.scope, input.directory, input.sdk))
        ).catch((err) => {
          console.error("Failed to load providers", err)
        }),
    ].filter(Boolean) as (() => Promise<any>)[]

    await waitForPaint()
    const slowErrs = errors(await runAll(slow))
    if (slowErrs.length > 0) {
      console.error("Failed to finish bootstrap instance", slowErrs[0])
    }

    if (loading && slowErrs.length === 0) input.setStore("status", "complete")
  })()
}
