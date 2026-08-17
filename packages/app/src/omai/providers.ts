import type { Model, Provider } from "@opencode-ai/sdk/v2/client"
import type { CatalogModel, CatalogPage, ModelProvider } from "@omai/sdk-web"
import type { NormalizedProviderListResponse } from "@omai/session-ui/context"
import { omaiClient } from "./client"

export async function loadOMAIProviderView(): Promise<NormalizedProviderListResponse> {
  const catalog = await omaiClient.models.getCatalog()
  const models = groupModels(catalog)
  return {
    all: new Map(
      catalog.providers.map((provider) => [provider.id, providerView(provider, models.get(provider.id) ?? [])]),
    ),
    connected: [...catalog.connectedProviderIds],
    default: { ...catalog.defaults },
  }
}

function groupModels(catalog: CatalogPage) {
  const result = new Map<string, CatalogModel[]>()
  for (const model of catalog.models) {
    const values = result.get(model.providerId)
    if (values) values.push(model)
    else result.set(model.providerId, [model])
  }
  return result
}

function providerView(provider: ModelProvider, models: readonly CatalogModel[]): Provider {
  return {
    id: provider.id,
    name: provider.name,
    source: "api",
    env: [...provider.environmentVariables],
    options: { runtimeId: provider.runtimeId, runtimeIds: [...provider.runtimeIds] },
    models: Object.fromEntries(models.map((model) => [model.id, modelView(provider, model)])),
  }
}

function modelView(provider: ModelProvider, model: CatalogModel): Model {
  const input = new Set(model.modalities.input)
  const output = new Set(model.modalities.output)
  return {
    id: model.id,
    providerID: provider.id,
    api: {
      id: model.id,
      url: model.provider?.api || provider.api,
      npm: model.provider?.npm || provider.npm,
    },
    name: model.name,
    ...(model.family ? { family: model.family } : {}),
    capabilities: {
      temperature: model.temperature,
      reasoning: model.reasoning,
      attachment: model.attachment,
      toolcall: model.toolCall,
      input: {
        text: input.has("text"),
        audio: input.has("audio"),
        image: input.has("image"),
        video: input.has("video"),
        pdf: input.has("pdf"),
      },
      output: {
        text: output.has("text"),
        audio: output.has("audio"),
        image: output.has("image"),
        video: output.has("video"),
        pdf: output.has("pdf"),
      },
      interleaved: interleaved(model.interleaved),
    },
    cost: {
      input: model.cost?.input ?? 0,
      output: model.cost?.output ?? 0,
      cache: { read: model.cost?.cacheRead ?? 0, write: model.cost?.cacheWrite ?? 0 },
    },
    limit: {
      context: model.limits.context,
      ...(model.limits.input > 0 ? { input: model.limits.input } : {}),
      output: model.limits.output,
    },
    status: modelStatus(model.status),
    options: { ...model.options, runtimeId: model.runtimeId, runtimeIds: [...model.runtimeIds], ready: model.ready },
    headers: { ...model.headers },
    release_date: model.releaseDate,
  }
}

function modelStatus(value: string): Model["status"] {
  if (value === "alpha" || value === "beta" || value === "deprecated") return value
  return "active"
}

function interleaved(value: CatalogModel["interleaved"]): Model["capabilities"]["interleaved"] {
  if (typeof value === "boolean") return value
  if (!value || typeof value !== "object" || Array.isArray(value)) return false
  const field = value.field
  if (field === "reasoning" || field === "reasoning_content" || field === "reasoning_details") return { field }
  return false
}
