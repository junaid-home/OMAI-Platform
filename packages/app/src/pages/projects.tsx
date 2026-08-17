import { createMemo, createSignal, For, Show } from "solid-js"
import { useLocation, useNavigate } from "@solidjs/router"
import { base64Encode } from "@omai/platform-utils/encode"
import { Button } from "@omai/ui/button"
import { Dialog } from "@omai/ui/dialog"
import { DropdownMenu } from "@omai/ui/dropdown-menu"
import { IconButton } from "@omai/ui/icon-button"
import { Icon } from "@omai/ui/icon"
import { BrandLogo } from "@/components/brand-logo"
import { useDialog } from "@omai/ui/context/dialog"
import { useLanguage } from "@/context/language"
import { useLayout } from "@/context/layout"
import { useServer } from "@/context/server"
import { useServerSync } from "@/context/server-sync"
import { useAuth } from "@/context/auth"
import { useDirectoryPicker } from "@/components/directory-picker"
import { DialogOpenPath } from "@/components/dialog-open-path"
import { openProjectImmediate } from "@/utils/open-project"
import { listOMAISessionViewsForRoot } from "@/omai/sessions"

const INITIAL_SHOW = 5

function formatRelativeTime(
  timestamp: number,
  t: (key: string, params?: Record<string, string | number>) => string,
): string {
  const now = Date.now()
  const diff = now - timestamp
  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)
  const weeks = Math.floor(days / 7)
  const months = Math.floor(days / 30)

  if (seconds < 60) return t("common.time.justNow")
  if (minutes < 60) return t("common.time.minutesAgo.short", { count: minutes })
  if (hours < 24) return t("common.time.hoursAgo.short", { count: hours })
  if (days < 7) return t("common.time.daysAgo.short", { count: days })
  if (weeks < 4) return t("common.time.weeksAgo.short", { count: weeks })
  if (months < 12) return t("common.time.monthsAgo.short", { count: months })
  return new Date(timestamp).toLocaleDateString()
}

export default function ProjectsPage() {
  const navigate = useNavigate()
  const location = useLocation()
  const dialog = useDialog()
  const language = useLanguage()
  const server = useServer()
  const serverSync = useServerSync()
  const pickDirectory = useDirectoryPicker()
  const auth = useAuth()
  const layout = useLayout()

  const [showAll, setShowAll] = createSignal(false)

  const userProjects = createMemo(() => {
    const projects = server.projects.list()
    const username = auth.user()?.username
    if (!username) return projects
    return projects.filter((p) => p.worktree.includes(`/${username}/projects/`))
  })

  const recentProjects = userProjects

  const visibleProjects = createMemo(() => {
    const projects = recentProjects()
    return showAll() ? projects : projects.slice(0, INITIAL_SHOW)
  })

  const openProject = (directory: string) => {
    server.projects.open(directory)
    server.projects.touch(directory)
    layout.sidebar.close()

    openProjectImmediate(
      directory,
      navigate,
      () => location.pathname === `/${base64Encode(directory)}/session`,
      () => listOMAISessionViewsForRoot(directory).then((data) => ({ data })),
      async () => ({}),
    )
  }

  const openOpenProjectDialog = () => {
    const conn = server.current
    if (!conn) return

    pickDirectory({
      server: conn,
      title: language.t("command.project.open"),
      multiple: true,
      onSelect: (result) => {
        if (Array.isArray(result)) {
          result.forEach(openProject)
        } else if (result) {
          openProject(result)
        }
      },
    })
  }

  const openNewProjectDialog = () => {
    dialog.show(
      () => (
        <DialogOpenPath
          onCreated={(path) => {
            openProject(path)
          }}
        />
      ),
      () => {},
    )
  }

  const deleteProject = (directory: string) => {
    const projectName = directory.split("/").filter(Boolean).pop() || directory
    dialog.show(
      () => (
        <Dialog size="normal" class="max-w-[400px] translate-x-[100px]" title={language.t("projects.delete.title")} fit>
          <div class="flex flex-col gap-4 pl-6 pr-2.5 pb-3">
            <div class="flex flex-col gap-1">
              <span class="text-14-regular text-text-strong">
                {language.t("projects.delete.confirm", { name: projectName })}
              </span>
            </div>
            <div class="flex justify-end gap-2">
              <Button variant="ghost" size="large" onClick={() => dialog.close()}>
                {language.t("common.cancel")}
              </Button>
              <Button
                variant="primary"
                size="large"
                class="bg-[#ff6b6b] hover:bg-[#ff4c4c] text-white"
                onClick={() => {
                  server.projects.close(directory)
                  dialog.close()
                }}
              >
                {language.t("common.delete")}
              </Button>
            </div>
          </div>
        </Dialog>
      ),
      () => {},
    )
  }

  return (
    <div class="mx-auto mt-14 w-full px-4">
      <div class="flex justify-center mb-10">
        <BrandLogo size="extra-large" />
      </div>

      <div class="w-full flex flex-col gap-3 max-w-2xl mx-auto">
        <div class="flex gap-2 items-center justify-between w-full pl-1">
          <div class="text-13-regular text-text-weak">{language.t("projects.title")}</div>
          <div class="flex gap-2">
            <Button
              size="normal"
              variant="secondary"
              icon="folder-add-left"
              class="pl-2 pr-3"
              onClick={openNewProjectDialog}
            >
              New Project
            </Button>
            <Button
              icon="folder"
              size="normal"
              variant="secondary"
              class="pl-2 pr-3"
              onClick={() => navigate("/projects/all")}
            >
              All Projects
            </Button>
          </div>
        </div>

        <div class="h-px bg-border-weaker-base" />

        <Show
          when={visibleProjects().length > 0}
          fallback={
            <div class="mt-30 mx-auto flex flex-col items-center gap-3">
              <Icon name="folder-add-left" size="large" />
              <div class="flex flex-col gap-1 items-center justify-center">
                <div class="text-14-medium text-text-strong">{language.t("projects.empty.title")}</div>
                <div class="text-12-regular text-text-weak">{language.t("projects.empty.description")}</div>
              </div>
            </div>
          }
        >
          <ul class="flex flex-col gap-1">
            <For each={visibleProjects()}>
              {(project) => (
                <div class="flex items-center w-full rounded-lg text-13-mono text-text-base hover:bg-surface-raised-base-hover transition-colors group">
                  <button
                    type="button"
                    class="flex items-center gap-3 flex-1 text-left px-3 py-2.5 cursor-pointer"
                    onClick={() => openProject(project.worktree)}
                  >
                    <Icon
                      name="folder"
                      size="small"
                      class="text-icon-weak group-hover:text-icon-base shrink-0 transition-colors"
                    />
                    <span class="truncate grow">{project.worktree}</span>
                  </button>
                  <DropdownMenu>
                    <DropdownMenu.Trigger
                      as={IconButton}
                      icon="dot-grid"
                      variant="ghost"
                      size="small"
                      class="mr-1 shrink-0"
                    />
                    <DropdownMenu.Portal>
                      <DropdownMenu.Content>
                        <DropdownMenu.Item onSelect={() => deleteProject(project.worktree)}>
                          <DropdownMenu.ItemLabel>{language.t("common.delete")}</DropdownMenu.ItemLabel>
                        </DropdownMenu.Item>
                      </DropdownMenu.Content>
                    </DropdownMenu.Portal>
                  </DropdownMenu>
                </div>
              )}
            </For>
          </ul>
        </Show>

        <Show when={recentProjects().length > INITIAL_SHOW}>
          <div class="flex justify-center mt-1">
            <button
              type="button"
              class="text-12-regular cursor-pointer mb-4 text-text-weak hover:text-text-base transition-colors"
              onClick={() => navigate("/projects/all")}
            >
              {language.t("projects.showAll", { count: recentProjects().length })}
            </button>
          </div>
        </Show>
      </div>
    </div>
  )
}
