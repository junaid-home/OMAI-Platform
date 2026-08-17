import { createEffect, createMemo, createSignal, For, Show, Suspense, type ParentProps } from "solid-js"
import { createStore } from "solid-js/store"
import { useLocation, useNavigate, useParams } from "@solidjs/router"
import { TabsInfoPopup } from "@/components/help-button"
import { DropdownMenu } from "@omai/ui/dropdown-menu"
import { useAuth } from "@/context/auth"
import { useLayout } from "@/context/layout"
import { useSettings } from "@/context/settings"
import { useGlobal } from "@/context/global"
import { useServer } from "@/context/server"
import { useServerSync } from "@/context/server-sync"
import { setNavigate } from "@/utils/notification-click"
import { setV2Toast, ToastRegion } from "@/utils/toast"
import { IconButton } from "@omai/ui/icon-button"
import { Tooltip, TooltipKeybind } from "@omai/ui/tooltip"
import { Spinner } from "@omai/ui/spinner"
import { useCommand } from "@/context/command"
import { useLanguage } from "@/context/language"
import { BrandLogo } from "@/components/brand-logo"
import { sessionTitle } from "@/utils/session-title"
import { Button } from "@omai/ui/button"
import { ResizeHandle } from "@omai/ui/resize-handle"
import { getFilename } from "@omai/platform-utils/path"
import { base64Encode } from "@omai/platform-utils/encode"
import { pathKey } from "@/utils/path-key"
import { sortedRootSessions } from "./layout/helpers"
import type { Session } from "@opencode-ai/sdk/v2/client"
import { listOMAISessionViewsForRoot } from "@/omai/sessions"

function decode64Dir(value: string): string {
  try {
    return atob(value.replace(/-/g, "+").replace(/_/g, "/"))
  } catch {
    return ""
  }
}

export default function Layout(props: ParentProps) {
  const navigate = useNavigate()
  const params = useParams()
  const location = useLocation()
  const layout = useLayout()
  const command = useCommand()
  const language = useLanguage()
  const global = useGlobal()
  const server = useServer()
  const serverSync = useServerSync()
  const auth = useAuth()
  const settings = useSettings()
  setNavigate(navigate)

  createEffect(() => setV2Toast(true))

  const sessionKey = createMemo(() => `${params.dir}${params.id ? "/" + params.id : ""}`)
  const sessionView = createMemo(() => layout.view(sessionKey()))

  const [sessions, setSessions] = createStore<{ list: Session[] }>({ list: [] })
  const resolvedDir = createSignal("")
  const dirResolving = createSignal(false)
  let dirResolveTimer: ReturnType<typeof setTimeout> | undefined

  // Resolve directory from URL or session data
  createEffect(() => {
    clearTimeout(dirResolveTimer)
    // For /:dir routes
    const dir = params.dir
    if (dir) {
      const decoded = decode64Dir(dir)
      if (decoded) {
        resolvedDir[1](decoded)
        dirResolving[1](false)
        return
      }
    }

    // For /server/:serverKey/session/:id routes — fetch session to get directory
    const sessionId = params.id
    if (sessionId) {
      const conn = server.current
      if (!conn) return
      dirResolving[1](true)
      const ctx = global.ensureServerCtx(conn)
      ctx.sync.session
        .resolve(sessionId)
        .then((session) => {
          if (session?.directory) {
            resolvedDir[1](session.directory)
          }
          dirResolving[1](false)
        })
        .catch(() => {
          dirResolving[1](false)
        })
      // A hanging resolve must not pin the full-screen loader forever.
      dirResolveTimer = setTimeout(() => dirResolving[1](false), 15_000)
      return
    }

    resolvedDir[1]("")
    dirResolving[1](false)
  })

  // Fetch sessions when directory changes
  createEffect(() => {
    const dir = resolvedDir[0]()
    if (!dir) {
      setSessions("list", [])
      return
    }

    const conn = server.current
    if (!conn) return

    const ctx = global.ensureServerCtx(conn)
    ctx.sync.child(dir, { bootstrap: true })
    ctx.sync.project.loadSessions(dir)

    const [store] = ctx.sync.child(dir, { bootstrap: false })
    let retries = 0
    const check = () => {
      const rootSessions = sortedRootSessions(store, Date.now())
      if (rootSessions.length > 0) {
        setSessions("list", rootSessions)
      } else if (retries < 25) {
        retries++
        setTimeout(check, 200 * retries)
      }
    }
    check()
  })

  const currentDir = resolvedDir[0]

  const isOnProjectsPage = createMemo(() => {
    const path = location.pathname
    return path === "/projects" || path === "/projects/all"
  })

  const isOnAgentsPage = createMemo(() => {
    const path = location.pathname
    return path === "/agents"
  })

  const isOnAuthPage = createMemo(() => {
    const path = location.pathname
    return path === "/login" || path === "/signup-secret-route"
  })

  const showSessionPanel = createMemo(() => !!currentDir() && !isOnProjectsPage() && !isOnAuthPage())

  const [lastProjectLoading, setLastProjectLoading] = createSignal(false)

  const openLastProject = async () => {
    if (lastProjectLoading()) return
    const username = auth.user()?.username
    const last = username ? server.projects.last() : null
    if (!last) {
      navigate("/projects")
      return
    }

    // Don't navigate if already on the same project
    const currentDirectory = currentDir()
    if (currentDirectory && pathKey(currentDirectory) === pathKey(last)) {
      return
    }

    setLastProjectLoading(true)
    try {
      const sessions = await listOMAISessionViewsForRoot(last)
      if (sessions.length > 0 && sessions[0].id) {
        layout.sidebar.close()
        navigate(`/${base64Encode(last)}/session/${sessions[0].id}`)
      }
    } catch {
      // Do nothing on error
    } finally {
      setLastProjectLoading(false)
    }
  }

  const currentProject = createMemo(() => {
    const dir = currentDir()
    if (!dir) return undefined
    const key = pathKey(dir)
    return layout.projects
      .list()
      .find((p) => pathKey(p.worktree) === key || p.sandboxes?.some((s) => pathKey(s) === key))
  })

  const projectName = createMemo(() => {
    const project = currentProject()
    if (!project) return ""
    return project.name || getFilename(project.worktree)
  })

  const currentSessions = () => sessions.list

  const side = createMemo(() => Math.max(layout.sidebar.width(), 244))
  const [sessionsPanelWidth, setSessionsPanelWidth] = createSignal(280)

  function navigateSessionByOffset(offset: number) {
    const sessionsList = currentSessions()
    if (sessionsList.length === 0) return

    const sessionIndex = params.id ? sessionsList.findIndex((s) => s.id === params.id) : -1

    let targetIndex: number
    if (sessionIndex === -1) {
      targetIndex = offset > 0 ? 0 : sessionsList.length - 1
    } else {
      targetIndex = (sessionIndex + offset + sessionsList.length) % sessionsList.length
    }

    const session = sessionsList[targetIndex]
    if (!session) return

    const dir = session.directory || currentDir()
    if (dir) {
      navigate(`/${base64Encode(dir)}/session/${session.id}`)
    }
  }

  function isSessionWorking(session: Session): boolean {
    return serverSync().session.data.session_working(session.id)
  }

  return (
    <div
      class="relative bg-background-base flex-1 min-h-0 min-w-0 flex flex-col select-none [&_input]:select-text [&_textarea]:select-text [&_[contenteditable]]:select-text"
      style={{
        "padding-top": "env(safe-area-inset-top, 0px)",
        "padding-bottom": "env(safe-area-inset-bottom, 0px)",
      }}
    >
      {/* Full screen loading overlay */}
      <Show when={dirResolving[0]() || lastProjectLoading() || global.sessionLoading.value}>
        <div class="absolute inset-0 z-50 bg-background-base flex items-center justify-center">
          <BrandLogo size="large" class="opacity-50 animate-pulse" />
        </div>
      </Show>

      {/* Auth pages: skip sidebar and flex layout, render children full-screen */}
      <Show when={isOnAuthPage()}>
        <div class="flex-1 min-h-0 min-w-0">{props.children}</div>
      </Show>

      <Show when={!isOnAuthPage()}>
        <div class="flex-1 min-h-0 min-w-0 flex">
          {/* Sidebar Rail */}
          <Show when={(!layout.session.collapsed() || isOnProjectsPage()) && !isOnAuthPage()}>
            <div
              class="w-12 shrink-0 bg-background-base flex flex-col items-center overflow-hidden z-10"
              classList={{ "border-r border-border-weaker-base": isOnProjectsPage() || isOnAgentsPage() }}
            >
              <div class="flex-1 min-h-0 w-full flex flex-col items-center">
                <div class="mt-2 mb-2">
                  <BrandLogo class="rounded-lg" size="small" />
                </div>
                <div class="h-full w-full flex flex-col items-center gap-2 px-2 py-2 overflow-y-auto no-scrollbar">
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.session.new")}
                    keybind={command.keybind("session.new")}
                  >
                    <IconButton
                      icon="new-session"
                      variant="ghost"
                      size="large"
                      onClick={async () => {
                        const dir = currentDir()
                        if (dir) {
                          navigate(`/${base64Encode(dir)}/session`)
                        } else {
                          command.trigger("session.new")
                        }
                      }}
                      aria-label={language.t("command.session.new")}
                    />
                  </TooltipKeybind>
                  <div class="h-4" />
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.project.previous") || "Open last session"}
                    keybind={command.keybind("project.previous")}
                  >
                    <IconButton
                      icon="wheel-globe"
                      variant="ghost"
                      size="large"
                      onClick={openLastProject}
                      aria-label={language.t("command.project.previous") || "Open last session"}
                    />
                  </TooltipKeybind>
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.project.open")}
                    keybind={command.keybind("project.open")}
                  >
                    <IconButton
                      icon="folder"
                      variant="ghost"
                      size="large"
                      onClick={() => navigate("/projects")}
                      aria-label={language.t("command.project.open")}
                    />
                  </TooltipKeybind>
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.terminal.toggle")}
                    keybind={command.keybind("terminal.toggle")}
                  >
                    <IconButton
                      icon={sessionView().terminal.opened() ? "terminal-active" : "terminal"}
                      variant="ghost"
                      size="large"
                      onClick={() => sessionView().terminal.toggle()}
                      aria-label={language.t("command.terminal.toggle")}
                      aria-expanded={sessionView().terminal.opened()}
                      aria-controls="terminal-panel"
                    />
                  </TooltipKeybind>
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.review.toggle")}
                    keybind={command.keybind("review.toggle")}
                  >
                    <IconButton
                      icon={sessionView().reviewPanel.opened() ? "sidebar-active" : "sidebar"}
                      variant="ghost"
                      size="large"
                      onClick={() => sessionView().reviewPanel.toggle()}
                      aria-label={language.t("command.review.toggle")}
                      aria-expanded={sessionView().reviewPanel.opened()}
                      aria-controls="review-panel"
                    />
                  </TooltipKeybind>
                  <TooltipKeybind
                    placement="right"
                    title={language.t("command.fileTree.toggle")}
                    keybind={command.keybind("fileTree.toggle")}
                  >
                    <IconButton
                      icon={layout.fileTree.opened() ? "file-tree-active" : "file-tree"}
                      variant="ghost"
                      size="large"
                      onClick={() => {
                        if (!settings.general.showFileTree()) settings.general.setShowFileTree(true)
                        layout.fileTree.toggle()
                      }}
                      aria-label={language.t("command.fileTree.toggle")}
                      aria-expanded={layout.fileTree.opened()}
                      aria-controls="file-tree-panel"
                    />
                  </TooltipKeybind>
                  <Show when={currentDir()}>
                    <Tooltip placement="right" value={language.t("command.session.previous")}>
                      <IconButton
                        icon="chevron-left"
                        variant="ghost"
                        size="large"
                        onClick={() => navigateSessionByOffset(-1)}
                        aria-label={language.t("command.session.previous")}
                      />
                    </Tooltip>
                    <Tooltip placement="right" value={language.t("command.session.next")}>
                      <IconButton
                        icon="chevron-right"
                        variant="ghost"
                        size="large"
                        onClick={() => navigateSessionByOffset(1)}
                        aria-label={language.t("command.session.next")}
                      />
                    </Tooltip>
                  </Show>
                </div>
              </div>
              <div class="shrink-0 w-full pt-2 pb-4 flex flex-col items-center gap-2">
                <Tooltip placement="right" value="Agents">
                  <IconButton
                    icon="boxes"
                    variant="ghost"
                    size="large"
                    onClick={() => navigate("/agents")}
                    aria-label="Agents"
                  />
                </Tooltip>
                <TooltipKeybind
                  placement="right"
                  title={language.t("sidebar.settings")}
                  keybind={command.keybind("settings.open")}
                >
                  <IconButton
                    icon="settings-gear"
                    variant="ghost"
                    size="large"
                    onClick={() => command.trigger("settings.open")}
                    aria-label={language.t("sidebar.settings")}
                  />
                </TooltipKeybind>
                <TooltipKeybind
                  placement="right"
                  title={language.t("command.sidebar.toggle")}
                  keybind={showSessionPanel() ? (command.keybind("sidebar.toggle") ?? "") : ""}
                >
                  <IconButton
                    icon={showSessionPanel() && layout.sidebar.opened() ? "sidebar-active" : "sidebar"}
                    variant="ghost"
                    size="large"
                    onClick={showSessionPanel() ? () => layout.sidebar.toggle() : undefined}
                    aria-label={language.t("command.sidebar.toggle")}
                    aria-expanded={showSessionPanel() ? layout.sidebar.opened() : undefined}
                  />
                </TooltipKeybind>
                <div class="w-8 h-8 rounded-full bg-accent-base flex items-center justify-center cursor-pointer text-white text-12-semibold select-none">
                  <DropdownMenu>
                    <DropdownMenu.Trigger>
                      <div class="w-8 h-8 rounded-full border border-border-weak-base bg-accent-base flex items-center justify-center cursor-pointer text-white text-12-semibold select-none hover:opacity-80 transition-opacity">
                        {auth.user()?.username?.charAt(0).toUpperCase() || "?"}
                      </div>
                    </DropdownMenu.Trigger>
                    <DropdownMenu.Portal>
                      <DropdownMenu.Content>
                        <DropdownMenu.Group>
                          <DropdownMenu.GroupLabel>{auth.user()?.username || "User"}</DropdownMenu.GroupLabel>
                        </DropdownMenu.Group>
                        <DropdownMenu.Separator />
                        <DropdownMenu.Item
                          class="cursor-pointer"
                          onSelect={() => {
                            auth.logout()
                            window.location.href = "/login"
                          }}
                        >
                          <DropdownMenu.ItemLabel>{language.t("auth.logout")}</DropdownMenu.ItemLabel>
                        </DropdownMenu.Item>
                      </DropdownMenu.Content>
                    </DropdownMenu.Portal>
                  </DropdownMenu>
                </div>
              </div>
            </div>
          </Show>

          {/* Sessions Panel */}
          <Show when={layout.sidebar.opened() && showSessionPanel() && !layout.session.collapsed()}>
            <div
              class="shrink-0 bg-v2-background-bg-base flex flex-col min-h-0 overflow-hidden rounded-xl border border-border-weak-base relative ml-1 my-1"
              style={{ width: `${sessionsPanelWidth()}px` }}
            >
              <div class="flex flex-col min-h-0 min-w-0 flex-1 px-3 py-2">
                <Show
                  when={currentProject()}
                  fallback={
                    <div class="flex-1 min-h-0 -mt-4 flex items-center justify-center px-6 pb-64 text-center">
                      <div class="mt-8 flex max-w-60 flex-col items-center gap-6 text-center">
                        <div class="flex flex-col gap-3">
                          <div class="text-14-medium text-text-strong">{language.t("sidebar.empty.title")}</div>
                          <div
                            class="text-14-regular text-text-base"
                            style={{ "line-height": "var(--line-height-normal)" }}
                          >
                            {language.t("sidebar.empty.description")}
                          </div>
                        </div>
                        <Button size="large" icon="folder-add-left" onClick={() => command.trigger("project.open")}>
                          {language.t("command.project.open")}
                        </Button>
                      </div>
                    </div>
                  }
                  keyed
                >
                  {(project) => (
                    <>
                      <div class="shrink-0 pl-1 py-1">
                        <div class="py-2 pl-2 pr-0">
                          <div class="text-14-medium text-text-strong truncate">{projectName()}</div>
                          <span class="text-12-regular text-text-base truncate select-text block">
                            {project.worktree}
                          </span>
                        </div>
                      </div>
                      <div class="shrink-0 py-2">
                        <Button
                          size="large"
                          icon="new-session"
                          class="w-full"
                          onClick={async () => {
                            const dir = project.worktree
                            if (dir) {
                              navigate(`/${base64Encode(dir)}/session`)
                            }
                          }}
                        >
                          {language.t("command.session.new")}
                        </Button>
                      </div>
                      <div class="flex-1 min-h-0 overflow-y-auto no-scrollbar">
                        <For each={currentSessions()}>
                          {(session) => {
                            const working = createMemo(() => {
                              return serverSync().session.data.session_working(session.id)
                            })
                            return (
                              <div
                                class="flex items-center gap-2 px-2 py-1.5 rounded-md cursor-pointer hover:bg-surface-base-active"
                                classList={{
                                  "bg-surface-base-active": session.id === params.id,
                                }}
                                onClick={() => {
                                  const dir = session.directory || project.worktree
                                  navigate(`/${base64Encode(dir)}/session/${session.id}`)
                                }}
                              >
                                <Show when={working()}>
                                  <div class="shrink-0 size-5 flex items-center justify-center">
                                    <Spinner class="size-3.5" />
                                  </div>
                                </Show>
                                <div class="flex-1 min-w-0">
                                  <div class="text-13-medium text-text-strong truncate">
                                    {sessionTitle(session.title) || language.t("command.session.new")}
                                  </div>
                                </div>
                              </div>
                            )
                          }}
                        </For>
                        <Show when={currentSessions().length === 0}>
                          <div class="px-2 py-4 text-center text-13-regular text-text-weak">
                            {language.t("sidebar.empty.sessions")}
                          </div>
                        </Show>
                      </div>
                    </>
                  )}
                </Show>
              </div>
              <div>
                <ResizeHandle
                  direction="horizontal"
                  size={sessionsPanelWidth()}
                  min={200}
                  max={500}
                  onResize={(width) => {
                    setSessionsPanelWidth(width)
                  }}
                />
              </div>
            </div>
          </Show>

          {/* Main Content */}
          <main class="flex-1 min-h-0 min-w-0 overflow-x-hidden flex flex-col items-start contain-strict bg-background-base">
            <Suspense>{props.children}</Suspense>
          </main>
        </div>
      </Show>
      <TabsInfoPopup />
      <ToastRegion v2 />
    </div>
  )
}
