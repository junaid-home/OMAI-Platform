import { useMutation, useQueryClient } from "@tanstack/solid-query"
import { showToast } from "@/utils/toast"
import { useServerSync } from "@/context/server-sync"
import { useServerSDK } from "@/context/server-sdk"
import { useLanguage } from "@/context/language"
import { directoryKey } from "@/context/global-sync/utils"
import { For, Show, createMemo, createSignal, type Component } from "solid-js"
import { Switch } from "@omai/ui/v2/switch-v2"
import { ButtonV2 } from "@omai/ui/v2/button-v2"
import { IconButtonV2 } from "@omai/ui/v2/icon-button-v2"
import { useDialog } from "@omai/ui/context/dialog"
import { Dialog, DialogBody, DialogFooter, DialogHeader, DialogTitle } from "@omai/ui/v2/dialog-v2"
import { DividerV2 } from "@omai/ui/v2/divider-v2"
import { TextInputV2 } from "@omai/ui/v2/text-input-v2"
import { Dialog as DialogV1 } from "@omai/ui/dialog"
import { Button } from "@omai/ui/button"
import { Icon } from "@omai/ui/icon"
import type { McpStatus } from "@opencode-ai/sdk/v2/client"
import { addOMAIMCP, deleteOMAIMCP } from "@/omai/mcp"

const statusColor = (status: string | undefined) => {
  switch (status) {
    case "connected":
      return "bg-icon-success-base"
    case "failed":
      return "bg-icon-critical-base"
    case "needs_auth":
    case "needs_client_registration":
      return "bg-icon-warning-base"
    default:
      return "bg-border-weak-base"
  }
}

const McpAddDialog: Component<{
  directory: string
}> = (props) => {
  const dialog = useDialog()
  const language = useLanguage()
  const serverSDK = useServerSDK()
  const queryClient = useQueryClient()

  const [name, setName] = createSignal("")
  const [type, setType] = createSignal<"local" | "remote">("local")
  const [command, setCommand] = createSignal("")
  const [url, setUrl] = createSignal("")
  const [error, setError] = createSignal("")
  const [busy, setBusy] = createSignal(false)

  const add = async () => {
    if (!name().trim()) return
    setBusy(true)
    setError("")
    try {
      const config =
        type() === "local"
          ? { type: "local" as const, command: command().split(" ").filter(Boolean) }
          : { type: "remote" as const, url: url() }
      const dir = props.directory
      await addOMAIMCP(dir, { name: name(), ...config })
      const key = directoryKey(dir)
      const scope = serverSDK().scope
      await queryClient.refetchQueries({ queryKey: [scope, key, "mcp"] })
      dialog.close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const canSubmit = () => name().trim() && (type() === "local" ? command().trim() : url().trim()) && !busy()

  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "Enter" || event.isComposing) return
    event.preventDefault()
    if (!canSubmit()) return
    void add()
  }

  return (
    <Dialog fit class="settings-v2-server-dialog">
      <DialogHeader hideClose>
        <DialogTitle>Add MCP Server</DialogTitle>
      </DialogHeader>
      <DividerV2 />
      <DialogBody class="flex w-full min-w-0 flex-1 flex-col px-4 pt-4 pb-2">
        <div class="flex w-full min-w-0 flex-col gap-6">
          <div class="flex flex-col gap-2">
            <label class="settings-v2-server-dialog-label">Name</label>
            <TextInputV2
              type="text"
              appearance="large"
              class="!w-full self-stretch"
              value={name()}
              placeholder="my-mcp-server"
              disabled={busy()}
              autofocus
              onInput={(event) => setName(event.currentTarget.value)}
              onKeyDown={handleKeyDown}
            />
          </div>
          <div class="flex flex-col gap-2">
            <label class="settings-v2-server-dialog-label">Type</label>
            <div class="flex gap-2">
              <ButtonV2
                size="small"
                variant={type() === "local" ? "contrast" : "neutral"}
                onClick={() => setType("local")}
              >
                Local
              </ButtonV2>
              <ButtonV2
                size="small"
                variant={type() === "remote" ? "contrast" : "neutral"}
                onClick={() => setType("remote")}
              >
                Remote
              </ButtonV2>
            </div>
          </div>
          <Show when={type() === "local"}>
            <div class="flex flex-col gap-2">
              <label class="settings-v2-server-dialog-label">Command</label>
              <TextInputV2
                type="text"
                appearance="large"
                class="!w-full self-stretch"
                value={command()}
                placeholder="npx -y @modelcontextprotocol/server-filesystem /path"
                disabled={busy()}
                onInput={(event) => setCommand(event.currentTarget.value)}
                onKeyDown={handleKeyDown}
              />
            </div>
          </Show>
          <Show when={type() === "remote"}>
            <div class="flex flex-col gap-2">
              <label class="settings-v2-server-dialog-label">URL</label>
              <TextInputV2
                type="text"
                appearance="large"
                class="!w-full self-stretch"
                value={url()}
                placeholder="http://localhost:8080/mcp"
                disabled={busy()}
                onInput={(event) => setUrl(event.currentTarget.value)}
                onKeyDown={handleKeyDown}
              />
            </div>
          </Show>
          <Show when={error()}>
            <span class="settings-v2-server-dialog-error">{error()}</span>
          </Show>
        </div>
      </DialogBody>
      <DialogFooter>
        <ButtonV2 variant="neutral" disabled={busy()} onClick={() => dialog.close()}>
          {language.t("common.cancel")}
        </ButtonV2>
        <ButtonV2 variant="contrast" disabled={!canSubmit()} onClick={() => void add()}>
          {busy() ? "Adding..." : "Add"}
        </ButtonV2>
      </DialogFooter>
    </Dialog>
  )
}

const DeleteMcpDialog: Component<{
  name: string
  directory: string
}> = (props) => {
  const dialog = useDialog()
  const language = useLanguage()
  const serverSDK = useServerSDK()
  const queryClient = useQueryClient()
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal("")

  const handleDelete = async () => {
    setBusy(true)
    setError("")
    try {
      const dir = props.directory
      const name = props.name

      await deleteOMAIMCP(dir, name)

      const key = directoryKey(dir)
      const scope = serverSDK().scope
      await queryClient.refetchQueries({ queryKey: [scope, key, "mcp"] })
      await queryClient.refetchQueries({ queryKey: [scope, key, "mcpResources"] })
      dialog.close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <DialogV1 size="normal" class="max-w-[400px] translate-x-[100px]" title="Delete MCP Server" fit>
      <div class="flex flex-col gap-4 pl-6 pr-2.5 pb-3">
        <div class="flex flex-col gap-1">
          <span class="text-14-regular text-text-strong">
            Delete MCP server "{props.name}"? This will disconnect the server and remove it from the configuration.
          </span>
          <Show when={error()}>
            <span class="text-14-regular text-text-critical mt-1">{error()}</span>
          </Show>
        </div>
        <div class="flex justify-end gap-2">
          <Button variant="ghost" size="large" disabled={busy()} onClick={() => dialog.close()}>
            {language.t("common.cancel")}
          </Button>
          <Button
            variant="primary"
            size="large"
            class="bg-[#ff6b6b] hover:bg-[#ff4c4c] text-white"
            disabled={busy()}
            onClick={handleDelete}
          >
            {busy() ? "Deleting..." : language.t("common.delete")}
          </Button>
        </div>
      </div>
    </DialogV1>
  )
}

export const SettingsMcp: Component<{
  sessionID?: string
}> = (props) => {
  const language = useLanguage()
  const serverSync = useServerSync()
  const dialog = useDialog()
  const directory = createMemo(() => {
    if (!props.sessionID) return undefined
    return serverSync().session.lineage.peek(props.sessionID)?.session.directory
  })

  const mcpData = createMemo(() => {
    const dir = directory()
    if (!dir) return {} as Record<string, McpStatus>
    return serverSync().peek(dir, { mcp: true, bootstrap: false })[0].mcp ?? {}
  })
  const names = createMemo(() => Object.keys(mcpData()).sort())

  const toggleMutation = useMutation(() => ({
    mutationFn: async (name: string) => {
      const dir = directory()
      if (!dir) return
      await serverSync().mcp.toggle(dir, name)
    },
    onError: (error) =>
      showToast({
        variant: "error",
        title: language.t("common.requestFailed"),
        description: error instanceof Error ? error.message : String(error),
      }),
  }))

  const openAdd = () => {
    const dir = directory()
    if (!dir) return
    dialog.show(() => <McpAddDialog directory={dir} />)
  }

  const openDelete = (name: string) => {
    const dir = directory()
    if (!dir) return
    dialog.show(() => <DeleteMcpDialog name={name} directory={dir} />)
  }

  return (
    <>
      <div class="settings-v2-tab-header settings-v2-servers-header">
        <div class="settings-v2-tab-header-row">
          <h2 class="settings-v2-tab-title">{language.t("settings.mcp.title")}</h2>
          <Show when={directory()}>
            <ButtonV2 size="small" variant="neutral" onClick={openAdd}>
              Add MCP Server
            </ButtonV2>
          </Show>
        </div>
      </div>

      <div class="settings-v2-tab-body settings-v2-servers">
        <Show
          when={directory()}
          fallback={
            <div class="settings-v2-servers-status">
              <span>{language.t("dialog.mcp.hint")}</span>
            </div>
          }
        >
          <Show
            when={names().length > 0}
            fallback={
              <div class="settings-v2-servers-status">
                <span>{language.t("dialog.mcp.empty")}</span>
              </div>
            }
          >
            <div data-component="settings-v2-list">
              <For each={names()}>
                {(name) => {
                  const status = () => mcpData()[name]?.status
                  const error = () => {
                    const s = mcpData()[name]
                    if (s?.status === "failed" || s?.status === "needs_client_registration") return s.error
                  }
                  const enabled = () => status() === "connected"
                  return (
                    <div class="settings-v2-servers-row">
                      <div class="settings-v2-servers-lead">
                        <div
                          classList={{
                            "size-1.5 rounded-full shrink-0 mt-0.5": true,
                            [statusColor(status())]: true,
                          }}
                        />
                        <div class="settings-v2-servers-copy">
                          <span class="settings-v2-servers-name">{name}</span>
                          <Show when={status()}>
                            <span class="settings-v2-servers-meta">{status()}</span>
                          </Show>
                          <Show when={error()}>
                            <span class="settings-v2-servers-meta">{error()}</span>
                          </Show>
                        </div>
                      </div>
                      <div class="settings-v2-servers-actions">
                        <IconButtonV2
                          variant="ghost-muted"
                          size="small"
                          icon={<Icon name="trash" size="small" />}
                          onClick={() => openDelete(name)}
                        />
                        <Switch
                          checked={enabled()}
                          disabled={toggleMutation.isPending && toggleMutation.variables === name}
                          onChange={() => {
                            if (toggleMutation.isPending) return
                            toggleMutation.mutate(name)
                          }}
                        />
                      </div>
                    </div>
                  )
                }}
              </For>
            </div>
          </Show>
        </Show>
      </div>
    </>
  )
}
