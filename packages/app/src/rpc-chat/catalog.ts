import { Persist } from "@/utils/persist"
import type { RuntimeInstallation } from "@omai/sdk-web"

export type CatalogProvider = {
  id: string
  name: string
  model_count?: number
  connected?: boolean
}

export type CatalogModel = {
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

export type CatalogSelection = {
  provider_id: string
  id: string
  name: string
  runtime_id: string
}

export const CatalogSelectionKey = "agents-selected-model"
export const CatalogSelectionVersion = ["agents-selected-model.v1"]

export const RpcChatSessionsKey = "rpc-chat-sessions"
export const RpcChatSessionsVersion = ["rpc-chat-sessions.v3"]

export type RpcSessionMeta = {
  id: string
  externalId: string
  projectId: string
  workspaceId: string
  runtimeId: string
  providerId: string
  modelId: string
  root: string
  title: string
  updatedUnixMillis: number
}

export const selectionPersist = () => Persist.global(CatalogSelectionKey, CatalogSelectionVersion)

export const rpcSessionsPersist = () => Persist.global(RpcChatSessionsKey, RpcChatSessionsVersion)

export function runtimeIconID(runtime: RuntimeInstallation): string | undefined {
  if (runtime.id.startsWith("go-adk-")) return runtime.id.slice("go-adk-".length)
  if (runtime.id === "opencode-acp") return "opencode"
  return undefined
}
