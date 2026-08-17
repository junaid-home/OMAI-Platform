import { createMemo, createResource, createSignal, For, Show } from "solid-js"
import { createStore } from "solid-js/store"
import { Button } from "@omai/ui/button"
import { Dialog } from "@omai/ui/dialog"
import { Icon } from "@omai/ui/icon"
import { IconButton } from "@omai/ui/icon-button"
import { ProviderIcon } from "@omai/ui/provider-icon"
import { Spinner } from "@omai/ui/spinner"
import { Tag } from "@omai/ui/tag"
import { Tooltip } from "@omai/ui/tooltip"
import { useDialog } from "@omai/ui/context/dialog"
import {
  listModels,
  listProviders,
  listRuntimeHealth,
  listRuntimes,
  type RuntimeHealthResponse,
  type RuntimeInstallation,
} from "@/omai/client"
import { useLanguage } from "@/context/language"
import { Persist, persisted } from "@/utils/persist"

type CatalogProvider = {
  id: string
  name: string
  model_count?: number
  connected?: boolean
}

type CatalogModel = {
  id: string
  name: string
  description?: string
  provider_id: string
  runtime_id: string
  ready?: boolean
  free?: boolean
  unavailable_reason?: string
  last_updated?: string
  limits?: { context?: number }
}

type CatalogSelection = {
  provider_id: string
  id: string
  name: string
  runtime_id: string
}

function runtimeIconID(runtime: RuntimeInstallation): string | undefined {
  if (runtime.id.startsWith("go-adk-")) return runtime.id.slice("go-adk-".length)
  if (runtime.id === "opencode-acp") return "opencode"
  return undefined
}

type RuntimeStatus = { label: string; tone: "positive" | "neutral" | "negative" }

function runtimeStatus(runtime: RuntimeInstallation, health: RuntimeHealthResponse | undefined): RuntimeStatus {
  if (!runtime.enabled) return { label: "Disabled", tone: "neutral" }
  if (health?.available) return { label: "Available", tone: "positive" }
  return { label: "Error", tone: "negative" }
}

function Badge(props: { value: string; tone?: "neutral" | "positive" | "negative" }) {
  return (
    <span
      class="shrink-0 rounded-md px-1.5 py-0.5 text-11-medium tracking-wide"
      classList={{
        "bg-surface-base text-text-weak": props.tone === "neutral" || !props.tone,
        "bg-accent-base/15 text-accent-strong text-surface-success-strong": props.tone === "positive",
        "bg-danger-base/15 text-danger-strong text-surface-warning-strong": props.tone === "negative",
      }}
    >
      {props.value}
    </span>
  )
}

function relativeDaysAgo(date: string | undefined): number | undefined {
  if (!date) return undefined
  return Math.max(0, Math.floor((Date.now() - new Date(date).getTime()) / 86_400_000))
}

function ModelsDialog(props: {
  runtime: RuntimeInstallation
  displayName: string
  selection: () => CatalogSelection | undefined
  onSelect: (selection: CatalogSelection) => void
}) {
  const language = useLanguage()
  const [query, setQuery] = createSignal("")
  const [selectedProvider, setSelectedProvider] = createSignal<CatalogProvider | null>(null)

  const [providers, { refetch: refetchProviders }] = createResource(
    () => props.runtime.id,
    (runtimeID) => (runtimeID ? listProviders({ runtime_id: runtimeID, limit: 500 }) : undefined),
  )

  const providerList = createMemo(() => (providers()?.providers as CatalogProvider[] | undefined) ?? [])

  const filteredProviders = createMemo(() => {
    const term = query().toLowerCase().trim()
    const list = providerList()
    if (!term) return list
    return list.filter(
      (provider) => provider.name.toLowerCase().includes(term) || provider.id.toLowerCase().includes(term),
    )
  })

  const [models, { refetch: refetchModels }] = createResource(selectedProvider, (provider) =>
    provider ? listModels({ provider_id: provider.id, limit: 500 }) : undefined,
  )

  const modelList = createMemo(() => (models()?.models as CatalogModel[] | undefined) ?? [])

  const filteredModels = createMemo(() => {
    const term = query().toLowerCase().trim()
    const list = modelList()
    if (!term) return list
    return list.filter(
      (model) =>
        model.name.toLowerCase().includes(term) ||
        model.id.toLowerCase().includes(term) ||
        (model.description ?? "").toLowerCase().includes(term),
    )
  })

  const updatedDays = createMemo(() => {
    let latest: string | undefined
    for (const model of modelList()) {
      if (model.last_updated && (!latest || model.last_updated > latest)) latest = model.last_updated
    }
    return relativeDaysAgo(latest)
  })

  const selectProvider = (provider: CatalogProvider) => {
    setQuery("")
    setSelectedProvider(provider)
  }

  const backToProviders = () => {
    setQuery("")
    setSelectedProvider(null)
  }

  const refresh = () => {
    if (selectedProvider()) refetchModels()
    else refetchProviders()
  }

  return (
    <Dialog title={props.displayName} size="large">
      <div class="flex min-h-0 flex-1 w-full flex-col gap-2 px-3">
        <Show
          when={!selectedProvider()}
          fallback={
            <div class="flex min-h-0 flex-1 flex-col gap-2">
              <div class="flex items-center gap-2">
                <IconButton
                  icon="arrow-left"
                  variant="ghost"
                  size="small"
                  aria-label="Back to providers"
                  onClick={backToProviders}
                />
                <span class="truncate text-13-medium text-text-strong">{selectedProvider()?.name}</span>
              </div>

              <div class="relative">
                <div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-icon-weak">
                  <Icon name="magnifying-glass" size="small" />
                </div>
                <input
                  type="text"
                  placeholder="Search models"
                  value={query()}
                  onInput={(e) => setQuery(e.currentTarget.value)}
                  class="w-full rounded-lg border border-border-weaker-base bg-surface-raised-base py-2 pl-9 pr-3 text-13-regular text-text-base placeholder:text-text-weak focus:outline-none focus:ring-2 focus:ring-accent-base"
                />
              </div>

              <div class="min-h-0 flex-1 overflow-y-auto py-1">
                <Show when={models.loading}>
                  <div class="flex items-center justify-center py-10">
                    <Spinner class="size-5" />
                  </div>
                </Show>

                <Show when={models.state === "ready"}>
                  <Show
                    when={filteredModels().length > 0}
                    fallback={
                      <div class="flex flex-col items-center gap-2 py-8 text-center">
                        <Icon name="models" size="large" class="text-icon-weak" />
                        <div class="text-13-regular text-text-weak">
                          {query() ? "No models match your search." : "No models returned for this provider."}
                        </div>
                      </div>
                    }
                  >
                    <ul class="flex flex-col">
                      <For each={filteredModels()}>
                        {(model) => {
                          const unavailable = model.ready === false
                          const isSelectedProvider = createMemo(() => selectedProvider()?.id === model.provider_id)
                          const isSelectedModel = createMemo(() => props.selection()?.id === model.id)
                          const isSelected = createMemo(() => isSelectedProvider() && isSelectedModel())

                          return (
                            <li class="border-t border-border-weaker-base">
                              <Tooltip
                                class="w-full"
                                inactive={!unavailable}
                                value={
                                  <div class="flex max-w-[260px] flex-col gap-0.5 py-1">
                                    <div class="text-13-medium text-text-invert-base">Model unavailable</div>
                                    <Show when={model.unavailable_reason}>
                                      <div class="whitespace-pre-wrap text-12-regular text-text-invert-base">
                                        {model.unavailable_reason}
                                      </div>
                                    </Show>
                                  </div>
                                }
                              >
                                <button
                                  type="button"
                                  disabled={unavailable}
                                  onClick={() => {
                                    props.onSelect({
                                      provider_id: model.provider_id,
                                      name: model.name,
                                      id: model.id,
                                      runtime_id: model.runtime_id,
                                    })
                                  }}
                                  class="flex w-full cursor-pointer flex-col gap-0.5 px-1 py-2 text-left transition-colors hover:bg-surface-raised-base-hover disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:bg-transparent"
                                >
                                  <div class="flex min-w-0 items-baseline gap-2">
                                    <span class="truncate text-13-medium text-text-strong">{model.name}</span>
                                    <Show when={(model.limits?.context ?? 0) >= 1_000_000}>
                                      <span class="shrink-0 rounded bg-surface-base px-1 py-0.5 text-11-medium text-text-weak">
                                        1M
                                      </span>
                                    </Show>
                                    <Show when={model.free}>
                                      <Tag class="shrink-0">{language.t("model.tag.free")}</Tag>
                                    </Show>
                                    <Show when={unavailable}>
                                      <Tag class="shrink-0">Unavailable</Tag>
                                    </Show>
                                    <span class="ml-auto shrink-0 font-mono text-11-regular text-text-weak">
                                      {model.id}
                                    </span>
                                    <Show when={isSelected()}>
                                      <Icon name="check-small" size="small" class="shrink-0 text-accent-strong" />
                                    </Show>
                                  </div>
                                  <Show when={model.description}>
                                    <div class="truncate text-12-regular text-text-weak">{model.description}</div>
                                  </Show>
                                </button>
                              </Tooltip>
                            </li>
                          )
                        }}
                      </For>
                    </ul>
                  </Show>
                </Show>
              </div>
            </div>
          }
        >
          <div class="relative mt-0.5">
            <div class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-icon-weak">
              <Icon name="magnifying-glass" size="small" />
            </div>
            <input
              type="text"
              placeholder="Search providers"
              value={query()}
              onInput={(e) => setQuery(e.currentTarget.value)}
              class="w-full rounded-lg border border-border-weaker-base bg-surface-raised-base py-2 pl-9 pr-3 text-13-regular text-text-base placeholder:text-text-weak focus:outline-none focus:ring-2 focus:ring-accent-base"
            />
          </div>

          <div class="min-h-0 flex-1 overflow-y-auto py-1">
            <Show when={providers.loading}>
              <div class="flex items-center justify-center py-10">
                <Spinner class="size-5" />
              </div>
            </Show>

            <Show when={providers.state === "ready"}>
              <Show
                when={filteredProviders().length > 0}
                fallback={
                  <div class="flex flex-col items-center gap-2 py-8 text-center">
                    <Icon name="boxes" size="large" class="text-icon-weak" />
                    <div class="text-13-regular text-text-weak">
                      {query() ? "No providers match your search." : "No providers returned for this runtime."}
                    </div>
                  </div>
                }
              >
                <ul class="flex flex-col">
                  <For each={filteredProviders()}>
                    {(provider) => (
                      <li class="border-t border-border-weaker-base">
                        <button
                          type="button"
                          class="flex w-full cursor-pointer items-center gap-3 px-1 py-2 text-left transition-colors hover:bg-surface-raised-base-hover"
                          onClick={() => selectProvider(provider)}
                        >
                          <div class="min-w-0 flex-1">
                            <div class="truncate text-13-medium text-text-strong">{provider.name}</div>
                            <Show when={provider.model_count !== undefined}>
                              <div class="text-11-regular text-text-weak">{provider.model_count} models</div>
                            </Show>
                          </div>
                          <Icon name="chevron-right" size="small" class="shrink-0 text-icon-weak" />
                        </button>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            </Show>
          </div>
        </Show>

        <div class="flex items-center w-full justify-between border-t border-border-weaker-base py-2">
          <Show when={updatedDays() !== undefined}>
            <span class="text-12-regular text-text-weak">Updated {updatedDays()}d ago</span>
          </Show>
          <div class="flex items-center justify-end">
            <Button size="small" variant="ghost" icon="refresh" onClick={refresh}>
              Refresh
            </Button>
          </div>
        </div>
      </div>
    </Dialog>
  )
}

export default function AgentsPage() {
  const dialog = useDialog()

  const [loaded, setLoaded] = createSignal(false)

  const [selectionStore, setSelectionStore] = persisted(
    Persist.global("agents-selected-model", ["agents-selected-model.v1"]),
    createStore<{ selection?: CatalogSelection }>({}),
  )

  const selectModel = (selection: CatalogSelection) => setSelectionStore("selection", selection)

  const [data] = createResource(
    async () => {
      const [runtimesResult, healthResult] = await Promise.all([listRuntimes(), listRuntimeHealth()])
      const health = new Map(healthResult.runtimes.map((h) => [h.runtimeId, h]))
      setLoaded(true)
      return { runtimes: runtimesResult.runtimes, health }
    },
    {
      initialValue: {
        runtimes: [] as RuntimeInstallation[],
        health: new Map<string, RuntimeHealthResponse>(),
      },
    },
  )

  const [expandedId, setExpandedId] = createSignal<string | null>(null)

  const handleRowClick = (
    runtime: RuntimeInstallation,
    displayName: string,
    health: RuntimeHealthResponse | undefined,
  ) => {
    if (health?.available) {
      dialog.show(() => (
        <ModelsDialog
          runtime={runtime}
          displayName={displayName}
          selection={() => selectionStore.selection}
          onSelect={selectModel}
        />
      ))
      return
    }
    setExpandedId(expandedId() === runtime.id ? null : runtime.id)
  }

  return (
    <div class="mx-auto mt-14 w-full max-w-xl px-4 pb-10">
      <div class="mb-4 flex items-center gap-1">
        <Icon name="boxes" />
        <h1 class="text-18-semibold text-text-strong">Agents</h1>
      </div>

      <Show
        when={loaded()}
        fallback={
          <div class="flex items-center justify-center py-16">
            <Spinner class="size-6" />
          </div>
        }
      >
        <Show
          when={data().runtimes.length > 0}
          fallback={
            <div class="mt-20 flex flex-col items-center gap-3">
              <Icon name="subagent" size="large" class="text-icon-weak" />
              <div class="text-14-medium text-text-strong">No agents available</div>
              <div class="text-12-regular text-text-weak">Runtimes are reported by the backend</div>
            </div>
          }
        >
          <ul class="flex flex-col rounded-lg overflow-hidden border border-border-weaker-base">
            <For each={data().runtimes}>
              {(runtime, index) => {
                const health = data().health.get(runtime.id)
                const status = runtimeStatus(runtime, health)
                const displayName = runtime.label
                const isLast = index() === data().runtimes.length - 1

                return (
                  <li
                    classList={{
                      "border-b border-border-weaker-base": !isLast,
                    }}
                    class="overflow-hidden bg-surface-raised-base"
                  >
                    <button
                      type="button"
                      class="flex w-full cursor-pointer items-center gap-3 p-4 text-left transition-colors hover:bg-surface-raised-base-hover"
                      onClick={() => handleRowClick(runtime, displayName, health)}
                      aria-expanded={expandedId() === runtime.id}
                    >
                      <Icon
                        name={expandedId() === runtime.id ? "chevron-down" : "chevron-right"}
                        size="small"
                        class="shrink-0 text-icon-weak"
                      />
                      <ProviderIcon id={runtimeIconID(runtime) ?? "opencode"} width={18} height={18} class="shrink-0" />
                      <div class="min-w-0 flex-1">
                        <div class="truncate text-13-medium text-text-strong">{displayName}</div>
                      </div>
                      <Badge value={status.label} tone={status.tone} />
                    </button>
                    <Show when={expandedId() === runtime.id}>
                      <div class="border-t border-border-weaker-base px-3 py-3">
                        <div class="flex items-center gap-2">
                          <Icon name="warning" size="small" />
                          <span class="text-13-medium">
                            {!runtime.enabled ? "Agent is disabled" : "Agent unavailable"}
                          </span>
                        </div>
                        <Show when={health?.reason}>
                          <div class="mt-1 text-icon-warning-active whitespace-pre-wrap text-13-regular">
                            {health?.reason}
                          </div>
                        </Show>
                      </div>
                    </Show>
                  </li>
                )
              }}
            </For>
          </ul>
        </Show>
      </Show>
    </div>
  )
}
