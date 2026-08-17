import { createClient, type Client, type Interceptor, type Transport } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web"
import {
  PermissionService,
  ProjectService,
  QuestionService,
  SessionService,
} from "./gen/omai/platform/v1/platform_pb.js"
import { ModelCatalogService } from "./gen/uab/v1/model_catalog_pb.js"
import {
  ConversationService,
  GitService,
  LSPService,
  MCPService,
  RuntimeService,
  TerminalService,
  WorkspaceService,
} from "./gen/uab/v1/native_pb.js"
import { PortalControlService } from "./gen/uab/v1/portal_pb.js"
import { PreviewService } from "./gen/uab/v1/preview_pb.js"
import { ToolRegistryService } from "./gen/uab/v1/reflection_pb.js"
import { ControlPlaneService, WorkspaceGatewayService } from "./gen/uab/v1/uab_pb.js"
import { VoiceControlService } from "./gen/uab/v1/voice_pb.js"
import { createModelCatalog, type OMAIModelCatalog } from "./catalog.js"
import { createMetadataInterceptor, type MetadataOptions } from "./metadata.js"
import { createGit, type OMAIGit } from "./git.js"
import { createLSP, type OMAILSP } from "./lsp.js"
import { createSessions, type OMAISessions } from "./sessions.js"
import { createPreview, type OMAIPreview } from "./preview.js"
import { createProjects, type OMAIProjects } from "./projects.js"
import { createTerminals, type OMAITerminals } from "./terminals.js"
import { createVoiceClient, type OMAIVoice, type WebSocketFactory } from "./voice.js"
import { createWorkspaces, type OMAIWorkspaces } from "./workspaces.js"
import { createPermissions, createQuestions, type OMAIPermissions, type OMAIQuestions } from "./interactions.js"
import { createMCP, type OMAIMCP } from "./mcp.js"

export interface OMAIClientOptions extends MetadataOptions {
  readonly baseUrl: string
  readonly voiceGatewayUrl?: string
  readonly credentials?: RequestCredentials
  readonly useBinaryFormat?: boolean
  readonly defaultTimeoutMs?: number
  readonly interceptors?: readonly Interceptor[]
  readonly fetch?: typeof globalThis.fetch
  readonly webSocketFactory?: WebSocketFactory
  readonly allowInsecureTransport?: boolean
}

export interface OMAIServices {
  readonly projects: Client<typeof ProjectService>
  readonly sessions: Client<typeof SessionService>
  readonly permissions: Client<typeof PermissionService>
  readonly questions: Client<typeof QuestionService>
  readonly controlPlane: Client<typeof ControlPlaneService>
  readonly workspaceGateway: Client<typeof WorkspaceGatewayService>
  readonly workspace: Client<typeof WorkspaceService>
  readonly git: Client<typeof GitService>
  readonly terminal: Client<typeof TerminalService>
  readonly lsp: Client<typeof LSPService>
  readonly mcp: Client<typeof MCPService>
  readonly runtime: Client<typeof RuntimeService>
  readonly conversations: Client<typeof ConversationService>
  readonly portal: Client<typeof PortalControlService>
  readonly tools: Client<typeof ToolRegistryService>
  readonly preview: Client<typeof PreviewService>
}

export interface OMAIClient {
  readonly transport: Transport
  readonly services: OMAIServices
  readonly models: OMAIModelCatalog
  readonly sessions: OMAISessions
  readonly voice: OMAIVoice
  readonly preview: OMAIPreview
  readonly projects: OMAIProjects
  readonly workspaces: OMAIWorkspaces
  readonly terminals: OMAITerminals
  readonly git: OMAIGit
  readonly lsp: OMAILSP
  readonly permissions: OMAIPermissions
  readonly questions: OMAIQuestions
  readonly mcp: OMAIMCP
}

export function createOMAIClient(options: OMAIClientOptions): OMAIClient {
  const baseUrl = checkedBaseUrl(options.baseUrl, options.allowInsecureTransport ?? false)
  const metadata = createMetadataInterceptor(options)
  const voiceOptions: { gatewayUrl?: string; webSocketFactory?: WebSocketFactory; allowInsecureTransport?: boolean } = {
    allowInsecureTransport: options.allowInsecureTransport ?? false,
  }
  if (options.voiceGatewayUrl !== undefined) {
    voiceOptions.gatewayUrl = options.voiceGatewayUrl
  }
  if (options.webSocketFactory !== undefined) {
    voiceOptions.webSocketFactory = options.webSocketFactory
  }
  const transport = createConnectTransport({
    baseUrl,
    useBinaryFormat: options.useBinaryFormat ?? true,
    defaultTimeoutMs: checkedTimeout(options.defaultTimeoutMs ?? 30_000),
    interceptors: [metadata, ...(options.interceptors ?? [])],
    fetch: credentialedFetch(options.fetch, options.credentials ?? "same-origin"),
  })
  return createOMAIClientFromTransport(transport, voiceOptions)
}

export function createOMAIClientFromTransport(
  transport: Transport,
  voiceOptions: {
    readonly gatewayUrl?: string
    readonly webSocketFactory?: WebSocketFactory
    readonly allowInsecureTransport?: boolean
  } = {},
): OMAIClient {
  const modelCatalog = createClient(ModelCatalogService, transport)
  const voiceControl = createClient(VoiceControlService, transport)
  const controlPlane = createClient(ControlPlaneService, transport)
  const conversations = createClient(ConversationService, transport)
  const projects = createClient(ProjectService, transport)
  const sessions = createClient(SessionService, transport)
  const permissions = createClient(PermissionService, transport)
  const questions = createClient(QuestionService, transport)
  const services: OMAIServices = Object.freeze({
    projects,
    sessions,
    permissions,
    questions,
    controlPlane,
    workspaceGateway: createClient(WorkspaceGatewayService, transport),
    workspace: createClient(WorkspaceService, transport),
    git: createClient(GitService, transport),
    terminal: createClient(TerminalService, transport),
    lsp: createClient(LSPService, transport),
    mcp: createClient(MCPService, transport),
    runtime: createClient(RuntimeService, transport),
    conversations,
    portal: createClient(PortalControlService, transport),
    tools: createClient(ToolRegistryService, transport),
    preview: createClient(PreviewService, transport),
  })
  return Object.freeze({
    transport,
    services,
    models: createModelCatalog(modelCatalog),
    sessions: createSessions(projects, sessions),
    voice: createVoiceClient(voiceControl, voiceOptions),
    preview: createPreview(services.preview),
    projects: createProjects(projects),
    workspaces: createWorkspaces(services.workspace),
    terminals: createTerminals(services.terminal),
    git: createGit(services.git),
    lsp: createLSP(services.lsp),
    permissions: createPermissions(permissions),
    questions: createQuestions(questions),
    mcp: createMCP(services.mcp),
  })
}

function checkedBaseUrl(value: string, allowInsecure: boolean): string {
  const trimmed = value.trim()
  const normalized = trimmed === "/" ? "/" : trimmed.replace(/\/+$/u, "")
  if (normalized.length === 0 || normalized.length > 2_048 || /[\r\n\0]/u.test(normalized)) {
    throw new TypeError("Invalid OMAI base URL")
  }
  if (normalized.startsWith("/")) {
    return normalized
  }
  const parsed = new URL(normalized)
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    throw new TypeError("OMAI base URL must use HTTPS or HTTP")
  }
  if (parsed.username !== "" || parsed.password !== "" || parsed.search !== "" || parsed.hash !== "") {
    throw new TypeError("OMAI base URL cannot contain credentials, query parameters, or fragments")
  }
  if (parsed.protocol === "http:" && !allowInsecure && !isLoopback(parsed.hostname)) {
    throw new TypeError("Plain HTTP is allowed only for loopback development; use HTTPS in production")
  }
  return normalized
}

function isLoopback(hostname: string): boolean {
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "[::1]" || hostname === "::1"
}

function checkedTimeout(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1 || value > 10 * 60_000) {
    throw new RangeError("Default OMAI timeout must be between 1 and 600000 milliseconds")
  }
  return value
}

function credentialedFetch(
  source: typeof globalThis.fetch | undefined,
  credentials: RequestCredentials,
): typeof globalThis.fetch {
  const implementation = source ?? globalThis.fetch
  if (implementation === undefined) {
    throw new TypeError("fetch is unavailable; provide a fetch implementation explicitly")
  }
  // Bun augments the global fetch type with runtime-specific helpers. The SDK
  // only wraps the standards-compatible call signature and deliberately does
  // not expose those process-local extensions.
  return ((input, init) => implementation(input, { ...init, credentials })) as typeof globalThis.fetch
}
