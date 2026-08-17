export { createOMAIClient, createOMAIClientFromTransport } from "./client.js"
export type { OMAIClient, OMAIClientOptions, OMAIServices } from "./client.js"
export { CatalogSchemaError, parseCatalogPage, parseProviderPage } from "./catalog.js"
export type {
  CatalogModel,
  CatalogPage,
  GetModelInput,
  ListModelsInput,
  ListProvidersInput,
  ModelCost,
  ModelCostBand,
  ModelCostTier,
  ModelExperimentalMode,
  ModelLimits,
  ModelModalities,
  ModelProvider,
  ModelProviderOverride,
  ModelReasoningOption,
  ModelUnitCost,
  OMAIModelCatalog,
  ProviderPage,
  SearchModelsInput,
} from "./catalog.js"
export { asOMAIError, OMAIError } from "./errors.js"
export { decodeJsonBytes, encodeJsonBytes } from "./json.js"
export { parseSessionEvent, parseTypedSessionEvent } from "./events.js"
export type {
  OMAIEventBase,
  OMAIMessageDeltaEvent,
  OMAIPermissionRequestedEvent,
  OMAIPermissionResolvedEvent,
  OMAIQuestionRequestedEvent,
  OMAIQuestionResolvedEvent,
  OMAISessionErrorEvent,
  OMAISessionEvent,
  OMAISessionState,
  OMAISessionStateEvent,
  OMAIToolCallEvent,
  OMAIToolUpdateEvent,
  OMAIUnknownEvent,
} from "./events.js"
export { createMetadataInterceptor } from "./metadata.js"
export type { AccessTokenProvider, HeaderProvider, MaybePromise, MetadataOptions } from "./metadata.js"
export { createGit } from "./git.js"
export type { OMAIGit, OMAIGitFileDiff, OMAIGitFileStatus, OMAIGitStatus, OMAIWorktree } from "./git.js"
export { createLSP } from "./lsp.js"
export type { OMAILanguageServer, OMAILSP, OMAILSPInstance } from "./lsp.js"
export { createMCP } from "./mcp.js"
export type { OMAIMCP, OMAIMCPServer, OMAIMCPServerInput, OMAIMCPTransport } from "./mcp.js"
export { createSessions } from "./sessions.js"
export { createPreview } from "./preview.js"
export type { OMAIPreview } from "./preview.js"
export { createProjects } from "./projects.js"
export type { OMAIProject, OMAIProjectPatch, OMAIProjects } from "./projects.js"
export { createTerminals } from "./terminals.js"
export type { OMAIShell, OMAITerminalCreateInput, OMAITerminals } from "./terminals.js"
export { createWorkspaces } from "./workspaces.js"
export type { OMAIFileContent, OMAIWorkspaces } from "./workspaces.js"
export { createPermissions, createQuestions } from "./interactions.js"
export type {
  OMAIInteractionFilter,
  OMAIPermission,
  OMAIPermissions,
  OMAIQuestion,
  OMAIQuestionOption,
  OMAIQuestionRequest,
  OMAIQuestions,
  OMAIToolReference,
} from "./interactions.js"
export type {
  OMAIConversationMessage,
  OMAIPlatformSession,
  OMAISendInput,
  OMAISessionPatch,
  OMAISessionCreateInput,
  OMAISessionSubmitInput,
  OMAISessionHandle,
  OMAISessionRoute,
  OMAISessions,
  OMAISubscribeInput,
} from "./sessions.js"
export { OMAIVoiceSession, parseServerMessage, VoiceProtocolError, VoiceSessionClosedError } from "./voice.js"
export type {
  OMAIVoice,
  VoiceAudioEvent,
  VoiceConfirmationEvent,
  VoiceConnectInput,
  VoiceEvent,
  VoiceReadyEvent,
  VoiceToolResultEvent,
  VoiceTranscriptEvent,
  VoiceUICommandEvent,
  VoiceUIResult,
  WebSocketFactory,
} from "./voice.js"
export * as PlatformV1 from "./gen/omai/platform/v1/platform_pb.js"
export * from "./gen/uab/v1/annotations_pb.js"
export * from "./gen/uab/v1/model_catalog_pb.js"
export * from "./gen/uab/v1/model_gateway_pb.js"
export * from "./gen/uab/v1/native_pb.js"
export * from "./gen/uab/v1/portal_pb.js"
export * from "./gen/uab/v1/preview_pb.js"
export * from "./gen/uab/v1/reflection_pb.js"
export * from "./gen/uab/v1/runtime_pb.js"
export * from "./gen/uab/v1/uab_pb.js"
export * from "./gen/uab/v1/voice_pb.js"
