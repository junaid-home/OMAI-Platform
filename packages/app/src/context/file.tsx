import { batch, createEffect, createMemo, createSignal, onCleanup } from "solid-js"
import { createStore, produce, reconcile } from "solid-js/store"
import { createSimpleContext } from "@omai/ui/context"
import { showToast } from "@/utils/toast"
import { useParams } from "@solidjs/router"
import { base64Encode } from "@omai/platform-utils/encode"
import { getFilename } from "@omai/platform-utils/path"
import { useSDK } from "./sdk"
import { useSync } from "./sync"
import { useLanguage } from "@/context/language"
import { useLayout } from "@/context/layout"
import { createPathHelpers } from "./file/path"
import {
  approxBytes,
  evictContentLru,
  getFileContentBytesTotal,
  getFileContentEntryCount,
  hasFileContent,
  removeFileContentBytes,
  resetFileContentLru,
  setFileContentBytes,
  touchFileContent,
} from "./file/content-cache"
import { createFileViewCache } from "./file/view-cache"
import { useServerSDK } from "./server-sdk"
import { SessionRouteKey, SessionStateKey } from "@/utils/server-scope"
import { createFileTreeStore } from "./file/tree-store"
import { invalidateFromWatcher } from "./file/watcher"
import { omaiClient, resolveOMAIWorkspace } from "@/omai/client"
import {
  selectionFromLines,
  type FileState,
  type FileSelection,
  type FileViewState,
  type SelectedLineRange,
} from "./file/types"

export type { FileSelection, SelectedLineRange, FileViewState, FileState }
export { selectionFromLines }
export {
  evictContentLru,
  getFileContentBytesTotal,
  getFileContentEntryCount,
  removeFileContentBytes,
  resetFileContentLru,
  setFileContentBytes,
  touchFileContent,
}

function errorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return error.message
  if (typeof error === "string" && error) return error
  return fallback
}

export const { use: useFile, provider: FileProvider } = createSimpleContext({
  name: "File",
  gate: false,
  init: () => {
    const sdk = useSDK()
    useSync()
    const params = useParams()
    const serverSDK = useServerSDK()
    const language = useLanguage()
    const layout = useLayout()

    const scope = createMemo(() => sdk().directory)
    const path = createPathHelpers(scope)
    const tabs = layout.tabs(() =>
      SessionStateKey.from(serverSDK().scope, SessionRouteKey.fromRoute(base64Encode(sdk().directory), params.id)),
    )

    const inflight = new Map<string, Promise<void>>()
    const [store, setStore] = createStore<{
      file: Record<string, FileState>
    }>({
      file: {},
    })
    const [watchRevision, setWatchRevision] = createSignal(0)

    const tree = createFileTreeStore({
      scope,
      normalizeDir: path.normalizeDir,
      list: async (dir) => {
        const workspace = await resolveOMAIWorkspace(scope())
        const entries = await omaiClient.workspaces.listFiles(workspace.id, dir)
        return entries.map((entry) => ({
          name: entry.name,
          path: path.normalize(entry.path),
          absolute: entry.path,
          type: entry.directory ? ("directory" as const) : ("file" as const),
          ignored: false,
        }))
      },
      onError: (message) => {
        showToast({
          variant: "error",
          title: language.t("toast.file.listFailed.title"),
          description: message,
        })
      },
    })

    const evictContent = (keep?: Set<string>) => {
      evictContentLru(keep, (target) => {
        if (!store.file[target]) return
        setStore(
          "file",
          target,
          produce((draft) => {
            draft.content = undefined
            draft.loaded = false
          }),
        )
      })
    }

    createEffect(() => {
      scope()
      inflight.clear()
      resetFileContentLru()
      batch(() => {
        setStore("file", reconcile({}))
        tree.reset()
      })
    })

    const viewCache = createFileViewCache(serverSDK().scope)
    const view = createMemo(() => viewCache.load(scope(), params.id))

    const ensure = (file: string) => {
      if (!file) return
      if (store.file[file]) return
      setStore("file", file, { path: file, name: getFilename(file) })
    }

    const setLoading = (file: string) => {
      setStore(
        "file",
        file,
        produce((draft) => {
          draft.loading = true
          draft.error = undefined
        }),
      )
    }

    const setLoaded = (file: string, content: FileState["content"]) => {
      setStore(
        "file",
        file,
        produce((draft) => {
          draft.loaded = true
          draft.loading = false
          draft.content = content
        }),
      )
    }

    const setLoadError = (file: string, message: string) => {
      setStore(
        "file",
        file,
        produce((draft) => {
          draft.loading = false
          draft.error = message
        }),
      )
      showToast({
        variant: "error",
        title: language.t("toast.file.loadFailed.title"),
        description: message,
      })
    }

    const load = (input: string, options?: { force?: boolean }) => {
      const file = path.normalize(input)
      if (!file) return Promise.resolve()

      const directory = scope()
      const key = `${directory}\n${file}`
      ensure(file)

      const current = store.file[file]
      if (!options?.force && current?.loaded) return Promise.resolve()

      const pending = inflight.get(key)
      if (pending) return pending

      setLoading(file)

      const promise = resolveOMAIWorkspace(directory)
        .then((workspace) => omaiClient.workspaces.readFile(workspace.id, file))
        .then((content) => {
          if (scope() !== directory) return
          setLoaded(file, content)

          if (!content) return
          touchFileContent(file, approxBytes(content))
          evictContent(new Set([file]))
        })
        .catch((e) => {
          if (scope() !== directory) return
          setLoadError(file, errorMessage(e, language.t("error.chain.unknown")))
        })
        .finally(() => {
          inflight.delete(key)
        })

      inflight.set(key, promise)
      return promise
    }

    const search = (query: string, _dirs: "true" | "false", options?: { limit?: number; signal?: AbortSignal }) =>
      resolveOMAIWorkspace(scope())
        .then((workspace) =>
          omaiClient.workspaces.searchFiles(
            workspace.id,
            query,
            { kind: "file", ...(options?.limit === undefined ? {} : { limit: options.limit }) },
            { signal: options?.signal },
          ),
        )
        .then(
          (files) => files.map(path.normalize),
          (error) => {
            if (options?.signal?.aborted) throw error
            return []
          },
        )

    createEffect(() => {
      const directory = scope()
      const loaded = tree.loadedDirectories()
      const open = tabs.all().flatMap((tab) => path.pathFromTab(tab) ?? [])
      const watched = [...new Set([...open, ...loaded])].sort().slice(0, 256)
      if (!directory || watched.length === 0) return

      const controller = new AbortController()
      const openFiles = new Set(open)
      const refresh = () => {
        loaded.forEach((dir) => void tree.listDir(dir, { force: true }))
        open.forEach((file) => void load(file, { force: true }))
      }
      const wait = (milliseconds: number) =>
        new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, milliseconds)
          controller.signal.addEventListener(
            "abort",
            () => {
              clearTimeout(timer)
              resolve()
            },
            { once: true },
          )
        })

      const run = async () => {
        let failures = 0
        while (!controller.signal.aborted) {
          try {
            const workspace = await resolveOMAIWorkspace(directory)
            for await (const change of omaiClient.workspaces.watchFiles(workspace.id, watched, {
              signal: controller.signal,
            })) {
              if (controller.signal.aborted || scope() !== directory) return
              failures = 0
              setWatchRevision((value) => value + 1)
              if (change.kind === "resync") {
                refresh()
                continue
              }
              invalidateFromWatcher(
                {
                  type: "file.watcher.updated",
                  properties: { file: change.path, event: change.kind },
                },
                {
                  normalize: path.normalize,
                  hasFile: (file) => Boolean(store.file[file]),
                  isOpen: (file) => openFiles.has(file),
                  loadFile: (file) => void load(file, { force: true }),
                  node: tree.node,
                  isDirLoaded: tree.isLoaded,
                  refreshDir: (dir) => void tree.listDir(dir, { force: true }),
                },
              )
            }
            if (!controller.signal.aborted) throw new Error("OMAI workspace watch ended")
          } catch {
            if (controller.signal.aborted) return
            failures += 1
            refresh()
            await wait(Math.min(250 * 2 ** Math.min(failures, 4), 4_000))
          }
        }
      }

      void run()
      onCleanup(() => controller.abort())
    })

    const get = (input: string) => {
      const file = path.normalize(input)
      const state = store.file[file]
      const content = state?.content
      if (!content) return state
      if (hasFileContent(file)) {
        touchFileContent(file)
        return state
      }
      touchFileContent(file, approxBytes(content))
      return state
    }

    function withPath(input: string, action: (file: string) => unknown) {
      return action(path.normalize(input))
    }
    const scrollTop = (input: string) => withPath(input, (file) => view().scrollTop(file))
    const scrollLeft = (input: string) => withPath(input, (file) => view().scrollLeft(file))
    const selectedLines = (input: string) => withPath(input, (file) => view().selectedLines(file))
    const setScrollTop = (input: string, top: number) => withPath(input, (file) => view().setScrollTop(file, top))
    const setScrollLeft = (input: string, left: number) => withPath(input, (file) => view().setScrollLeft(file, left))
    const setSelectedLines = (input: string, range: SelectedLineRange | null) =>
      withPath(input, (file) => view().setSelectedLines(file, range))

    onCleanup(() => {
      viewCache.clear()
    })

    return {
      ready: () => view().ready(),
      revision: watchRevision,
      normalize: path.normalize,
      tab: path.tab,
      pathFromTab: path.pathFromTab,
      tree: {
        list: tree.listDir,
        refresh: (input: string) => tree.listDir(input, { force: true }),
        state: tree.dirState,
        children: tree.children,
        expand: tree.expandDir,
        collapse: tree.collapseDir,
        toggle(input: string) {
          if (tree.dirState(input)?.expanded) {
            tree.collapseDir(input)
            return
          }
          tree.expandDir(input)
        },
      },
      get,
      load,
      scrollTop,
      scrollLeft,
      setScrollTop,
      setScrollLeft,
      selectedLines,
      setSelectedLines,
      searchFiles: (query: string, options?: { limit?: number; signal?: AbortSignal }) =>
        search(query, "false", options),
      searchFilesAndDirectories: (query: string) => search(query, "true"),
    }
  },
})
