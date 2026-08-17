import { Match, Show, Switch, createMemo } from "solid-js"
import { useFile } from "@/context/file"
import { useLanguage } from "@/context/language"
import { useSDK } from "@/context/sdk"
import { useLayout } from "@/context/layout"
import { useSessionLayout } from "@/pages/session/session-layout"
import { createOpenSessionFileTab, type Sizing } from "@/pages/session/helpers"
import { normalizeFileTreeV2Path } from "@/components/file-tree-v2-model"
import { displayName } from "@/pages/layout/helpers"
import { pathKey } from "@/utils/path-key"
import FileTree from "@/components/file-tree"
import { ResizeHandle } from "@omai/ui/resize-handle"
import { filterRenderableDiff, type RenderDiff } from "@/pages/session/v2/review-diff-kinds"
import type { SnapshotFileDiff, VcsFileDiff } from "@opencode-ai/sdk/v2"
import { deleteOMAIWorkspacePath, downloadOMAIWorkspaceArchive, moveOMAIWorkspacePath } from "@/omai/workspace-files"

type ReviewDiff = SnapshotFileDiff | VcsFileDiff

export function FileTreePanel(props: { diffs: () => ReviewDiff[]; size: Sizing }) {
  const layout = useLayout()
  const file = useFile()
  const language = useLanguage()
  const sdk = useSDK()
  const { tabs } = useSessionLayout()

  const projectDirectory = createMemo(() => sdk().directory)
  const treeWidth = createMemo(() => `${layout.fileTree.width()}px`)

  const project = createMemo(() => {
    const directory = pathKey(sdk().directory)
    return layout.projects
      .list()
      .find(
        (item) =>
          pathKey(item.worktree) === directory || item.sandboxes?.some((sandbox) => pathKey(sandbox) === directory),
      )
  })
  const projectName = createMemo(() => displayName(project() ?? { worktree: sdk().directory }))
  const downloadProject = async (name: string) => {
    try {
      await downloadOMAIWorkspaceArchive(sdk().directory, name)
    } catch {}
  }

  const nofiles = createMemo(() => {
    const state = file.tree.state("")
    if (!state?.loaded) return false
    return file.tree.children("").length === 0
  })

  const openReviewPanel = () => {}
  const normalizeTab = (tab: string) => {
    if (!tab.startsWith("file://")) return tab
    return file.tab(tab)
  }

  const openTab = createOpenSessionFileTab({
    normalizeTab,
    openTab: tabs().open,
    pathFromTab: file.pathFromTab,
    loadFile: file.load,
    openReviewPanel,
    setActive: tabs().setActive,
  })

  const diffs = createMemo(() => props.diffs().filter(filterRenderableDiff))
  const diffFiles = createMemo(() => diffs().map((d) => d.file))
  const kinds = createMemo(() => {
    const merge = (a: "add" | "del" | "mix" | undefined, b: "add" | "del" | "mix") => {
      if (!a) return b
      if (a === b) return a
      return "mix" as const
    }

    const out = new Map<string, "add" | "del" | "mix">()
    for (const diff of diffs()) {
      const file = normalizeFileTreeV2Path(diff.file)
      const kind = diff.status === "added" ? "add" : diff.status === "deleted" ? "del" : "mix"

      out.set(file, kind)

      const parts = file.split("/")
      for (const [idx] of parts.slice(0, -1).entries()) {
        const dir = parts.slice(0, idx + 1).join("/")
        if (!dir) continue
        out.set(dir, merge(out.get(dir), kind))
      }
    }
    return out
  })

  const empty = (msg: string) => (
    <div class="h-full flex flex-col">
      <div class="h-6 shrink-0" aria-hidden />
      <div class="flex-1 pb-64 flex items-center justify-center text-center">
        <div class="text-12-regular text-text-weak">{msg}</div>
      </div>
    </div>
  )

  return (
    <div
      id="file-tree-panel"
      class="relative shrink-0 h-full bg-v2-background-bg-base flex flex-col min-h-0 overflow-hidden rounded-xl border border-border-weak-base"
      style={{ width: treeWidth() }}
    >
      <div class="flex flex-col min-h-0 min-w-0 flex-1 overflow-hidden">
        <div class="flex justify-center h-[36px] items-center text-13-medium text-text-base border-b border-border-weaker-base shrink-0">
          {language.t("session.files.all")}
        </div>
        <div class="flex-1 min-h-0 overflow-y-auto">
          <div class="px-3 py-0">
            <Switch>
              <Match when={nofiles()}>{empty(language.t("session.files.empty"))}</Match>
              <Match when={true}>
                <FileTree
                  path=""
                  class="pt-3"
                  modified={diffFiles()}
                  kinds={kinds()}
                  root={{ name: projectName(), zip: `${projectName()}.zip` }}
                  onFileClick={(node) => openTab(file.tab(node.path))}
                  onDownloadProject={downloadProject}
                  onRename={async (oldPath: string, newPath: string) => {
                    try {
                      await moveOMAIWorkspacePath(projectDirectory(), oldPath, newPath)
                      file.tree.refresh("")
                    } catch {}
                  }}
                  onDelete={async (path: string) => {
                    try {
                      await deleteOMAIWorkspacePath(projectDirectory(), path)
                      file.tree.refresh("")
                    } catch {}
                  }}
                />
              </Match>
            </Switch>
          </div>
        </div>
      </div>
      <div onPointerDown={() => props.size.start()}>
        <ResizeHandle
          direction="horizontal"
          edge="start"
          size={layout.fileTree.width()}
          min={200}
          max={480}
          onResize={(width) => {
            props.size.touch()
            layout.fileTree.resize(width)
          }}
        />
      </div>
    </div>
  )
}
