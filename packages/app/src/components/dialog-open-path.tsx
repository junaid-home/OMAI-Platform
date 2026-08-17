import { createMemo, createSignal, Show } from "solid-js"
import { Button } from "@omai/ui/button"
import { Dialog } from "@omai/ui/dialog"
import { TextField } from "@omai/ui/text-field"
import { Icon } from "@omai/ui/icon"
import { Progress } from "@omai/ui/progress"
import { useDialog } from "@omai/ui/context/dialog"
import { useServerSync } from "@/context/server-sync"
import { useLanguage } from "@/context/language"
import { useAuth } from "@/context/auth"
import { BrandLogo } from "@/components/brand-logo"
import { createOMAIWorkspaceDirectory, deleteOMAIWorkspaceDirectory } from "@/omai/workspace-browser"
import { importOMAIWorkspaceArchive } from "@/omai/workspace-files"

type DialogOpenPathProps = {
  onCreated: (path: string) => void
}

function sanitizeProjectName(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-_]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
}

export function DialogOpenPath(props: DialogOpenPathProps) {
  const dialog = useDialog()
  const serverSync = useServerSync()
  const language = useLanguage()
  const auth = useAuth()

  const homeDir = createMemo(() => serverSync().data.path.home || "/root")
  const projectsBase = createMemo(() => {
    const username = auth.user()?.username || "default"
    return `${homeDir()}/workspace/${username}/projects`
  })

  const [name, setName] = createSignal("")
  const [error, setError] = createSignal("")
  const [loading, setLoading] = createSignal(false)
  const [loadingMessage, setLoadingMessage] = createSignal("")
  const [uploadPercent, setUploadPercent] = createSignal<number | null>(null)
  const [zipFile, setZipFile] = createSignal<File | null>(null)

  const ZIP_MAX_MB = 200

  const handleFileChange = (e: Event) => {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    if (file && file.name.endsWith(".zip")) {
      if (file.size > ZIP_MAX_MB * 1024 * 1024) {
        setError(language.t("dialog.createProject.zipTooLarge", { size: ZIP_MAX_MB }))
        return
      }
      setZipFile(file)
      setError("")
    }
  }

  const handleDrop = (e: DragEvent) => {
    e.preventDefault()
    const file = e.dataTransfer?.files[0]
    if (file && file.name.endsWith(".zip")) {
      if (file.size > ZIP_MAX_MB * 1024 * 1024) {
        setError(language.t("dialog.createProject.zipTooLarge", { size: ZIP_MAX_MB }))
        return
      }
      setZipFile(file)
      setError("")
    }
  }

  const handleDragOver = (e: DragEvent) => {
    e.preventDefault()
  }

  const rollback = async (projectPath: string) => {
    await deleteOMAIWorkspaceDirectory(projectPath).catch(() => {})
  }

  const handleSubmit = async (e: SubmitEvent) => {
    e.preventDefault()
    const projectName = sanitizeProjectName(name())
    if (!projectName) {
      setError(language.t("dialog.createProject.nameRequired"))
      return
    }

    // Check for duplicate project name
    const existingProjects = serverSync().data.project ?? []
    const projectPath = `${projectsBase()}/${projectName}`
    const duplicate = existingProjects.find((p) => p.worktree === projectPath)
    if (duplicate) {
      setError(language.t("dialog.createProject.duplicate", { path: projectPath }))
      return
    }

    setLoading(true)
    setError("")
    let directoryCreated = false

    try {
      const zip = zipFile()
      if (zip) {
        setLoadingMessage(language.t("dialog.createProject.step.zip"))
        setUploadPercent(0)
        await createOMAIWorkspaceDirectory(projectPath)
        directoryCreated = true
        setUploadPercent(25)
        await importOMAIWorkspaceArchive(projectPath, zip)
        setUploadPercent(100)
        setUploadPercent(null)
      } else {
        setLoadingMessage(language.t("dialog.createProject.step.directory"))
        await createOMAIWorkspaceDirectory(projectPath)
        directoryCreated = true
      }

      setLoadingMessage(language.t("dialog.createProject.step.opening"))
      props.onCreated(projectPath)
      dialog.close()
    } catch (err) {
      if (directoryCreated) {
        setLoadingMessage(language.t("dialog.createProject.step.cleanup"))
        await rollback(projectPath)
      }
      setError(err instanceof Error ? err.message : language.t("dialog.createProject.error.createFailed"))
    } finally {
      setLoading(false)
      setLoadingMessage("")
      setUploadPercent(null)
    }
  }

  return (
    <Dialog class="translate-y-[20%]" title={language.t("dialog.createProject.title")} transition>
      <form onSubmit={handleSubmit} class="flex flex-col gap-4 px-6 pb-5">
        <Show
          when={!loading()}
          fallback={
            <div class="flex flex-col items-center gap-3 py-12">
              <BrandLogo size="large" class="opacity-50 animate-pulse" />
              <div class="text-14-regular text-text-base">{loadingMessage()}</div>
              <Show when={uploadPercent() !== null}>
                <div class="w-full max-w-64">
                  <Progress value={uploadPercent() ?? 0} showValueLabel>
                    {language.t("dialog.createProject.step.uploading")}
                  </Progress>
                </div>
              </Show>
              <div class="text-11-regular text-text-weak">{language.t("dialog.createProject.creating")}</div>
            </div>
          }
        >
          <TextField
            autofocus
            label={language.t("dialog.createProject.nameLabel")}
            placeholder={language.t("dialog.createProject.namePlaceholder")}
            value={name()}
            onChange={(v) => {
              setName(v)
              setError("")
            }}
            validationState={error() ? "invalid" : undefined}
            error={error()}
          />

          <div class="text-11-regular text-text-weak -mt-2">{language.t("dialog.createProject.nameHint")}</div>

          <div class="flex flex-col gap-2">
            <div class="text-12-regular text-text-weak">{language.t("dialog.createProject.zipLabel")}</div>
            <Show
              when={zipFile()}
              fallback={
                <div
                  class="border-2 border-dashed border-border-base rounded-lg p-5 text-center cursor-pointer hover:border-border-strong hover:bg-surface-base-hover transition-colors"
                  onDrop={handleDrop}
                  onDragOver={handleDragOver}
                  onClick={() => {
                    const input = document.createElement("input")
                    input.type = "file"
                    input.accept = ".zip"
                    input.onchange = handleFileChange
                    input.click()
                  }}
                >
                  <Icon name="folder-add-left" size="small" class="text-icon-weak mb-1.5 mx-auto block" />
                  <div class="text-12-regular text-text-weak">{language.t("dialog.createProject.zipDropzone")}</div>
                  <div class="text-11-regular text-text-weaker mt-1">
                    {language.t("dialog.createProject.zipMaxSize")}
                  </div>
                </div>
              }
            >
              <div class="flex items-center gap-2 border border-border-base rounded-lg p-3">
                <Icon name="folder" size="small" class="text-icon-base shrink-0" />
                <span class="text-12-regular text-text-base truncate flex-1">{zipFile()?.name}</span>
                <button
                  type="button"
                  class="p-1 rounded hover:bg-surface-base-active transition-colors"
                  onClick={() => setZipFile(null)}
                >
                  <Icon name="close-small" size="small" class="text-icon-base" />
                </button>
              </div>
            </Show>
          </div>

          <div class="flex justify-end gap-2 mt-1">
            <Button type="button" variant="ghost" size="large" onClick={() => dialog.close()}>
              {language.t("common.cancel")}
            </Button>
            <Button type="submit" variant="primary" size="large">
              {language.t("common.create")}
            </Button>
          </div>
        </Show>
      </form>
    </Dialog>
  )
}
