import {
  createOMAIClient,
  type CatalogModel,
  type ModelProvider,
  type RuntimeHealthResponse,
  type RuntimeInstallation,
  type WorkspaceInfo,
} from "@omai/sdk-web"

let token = import.meta.env.VITE_OMAI_API_TOKEN ?? ""
const workspaces = new Map<string, Promise<WorkspaceInfo>>()

const voiceGatewayUrl = import.meta.env.VITE_OMAI_VOICE_GATEWAY_URL

export const omaiClient = createOMAIClient({
  baseUrl: import.meta.env.VITE_OMAI_API_BASE_URL ?? "/",
  accessToken: () => token,
  ...(voiceGatewayUrl === undefined ? {} : { voiceGatewayUrl }),
})

export function setOMAIAuthToken(value: string) {
  token = value.trim()
  workspaces.clear()
}

export function resolveOMAIWorkspace(root: string): Promise<WorkspaceInfo> {
  const normalized = root.trim()
  if (!normalized) return Promise.reject(new TypeError("Workspace root is required"))
  const existing = workspaces.get(normalized)
  if (existing) return existing
  const pending = omaiClient.workspaces.resolve(normalized).catch((error) => {
    if (workspaces.get(normalized) === pending) workspaces.delete(normalized)
    throw error
  })
  workspaces.set(normalized, pending)
  return pending
}

export function health(options?: { signal?: AbortSignal }) {
  return omaiClient.services.controlPlane.health({}, options)
}

export function healthAt(
  baseUrl: string,
  fetch: typeof globalThis.fetch | undefined,
  options?: { signal?: AbortSignal },
) {
  return createOMAIClient({
    baseUrl,
    accessToken: () => token,
    useBinaryFormat: false,
    ...(fetch === undefined ? {} : { fetch }),
  }).services.controlPlane.health({}, options)
}

export function listRuntimes() {
  return omaiClient.services.controlPlane.listRuntimes({})
}

export function listRuntimeHealth() {
  return omaiClient.services.runtime.listHealth({})
}

export async function listProviders(input: { runtime_id?: string; limit?: number }) {
  const page = await omaiClient.models.listProviders({
    ...(input.runtime_id === undefined ? {} : { runtimeId: input.runtime_id }),
    ...(input.limit === undefined ? {} : { limit: input.limit }),
  })
  return {
    providers: page.providers.map(providerView),
    connected: page.connectedProviderIds,
    default: page.defaults,
  }
}

export async function listModels(input: { provider_id?: string; runtime_id?: string; limit?: number }) {
  const page = await omaiClient.models.listModels({
    ...(input.provider_id === undefined ? {} : { providerId: input.provider_id }),
    ...(input.runtime_id === undefined ? {} : { runtimeId: input.runtime_id }),
    ...(input.limit === undefined ? {} : { limit: input.limit }),
  })
  return {
    models: page.models.map(modelView),
    total: page.total,
    offset: page.offset,
    next_offset: page.nextOffset,
  }
}

function providerView(provider: ModelProvider) {
  return {
    id: provider.id,
    name: provider.name,
    model_count: provider.modelCount,
    connected: provider.connected,
  }
}

function modelView(model: CatalogModel) {
  return {
    id: model.id,
    name: model.name,
    description: model.description,
    provider_id: model.providerId,
    runtime_id: model.runtimeId,
    ready: model.ready,
    free: model.free,
    unavailable_reason: model.unavailableReason,
    last_updated: model.lastUpdated,
    limits: { context: model.limits.context },
  }
}

export type { RuntimeHealthResponse, RuntimeInstallation }
