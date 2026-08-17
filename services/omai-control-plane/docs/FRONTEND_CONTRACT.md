# SolidJS frontend contract

The sole schema source of truth is `services/omai-control-plane/api`. Buf
generates Go and Protobuf-ES bindings from those files; the frontend owns no
copied Proto tree.

The established frontend schema also exposes descriptive message type names
such as `GitStatusRequest` and direct streaming chunk responses. `buf.yaml`
therefore exempts only Buf's three RPC request/response naming rules; every
other `STANDARD` rule remains enabled. New services, including
`PortalControlService`, use unique method-standard request and response names.

The client passes `VITE_OMAI_API_BASE_URL` to `createOMAIClient()` and sends
Connect requests to the OMAI server. Event payload bytes contain JSON shaped as
`{"payload": ...}`. Supported normalized updates are:

- `session.status`: payload status `running` or `idle`.
- `acp.session/update`: `agent_message_chunk`, `agent_thought_chunk`, `tool_call`, or `tool_call_update`.

The migrated core data plane does not require an OpenCode server or OpenCode
runtime transport. OMAI Web consumes the generated and tested `@omai/sdk-web`
package under `sdk/typescript`; frontend code must not copy generated files or
introduce a second handwritten schema. `buf generate` owns both the Go bindings
and the SDK's Protobuf-ES bindings. Existing `@opencode-ai` UI components and
view-model types may remain while they are presentation-only. Provider setup,
advanced session actions and full MCP execution are still
explicit compatibility scope and are not described as migrated here. File and
directory dialogs and MCP configuration no longer use OpenCode procedures.

Preview is fully migrated: `preview-lifecycle.ts` and `PreviewPanel` call
`omai.preview`, never `serverSDK().client.global.preview`. SolidJS supplies the
workspace root and renders the returned capability URL. Go detects the runtime,
prepares dependencies according to operator policy, starts it through the
workspace executor, proves the listener and HTTP readiness, supervises logs and
crashes, and invalidates the route on restart/stop. The agent-turn transition
from working to idle triggers `restart`, which reanalyzes generated code before
the iframe URL changes.

## SDK boundary

Create one SDK client for the Portal lifetime. The SDK owns Connect transport,
fresh auth metadata, typed RPC clients, catalog `Struct` validation, bounded
JSON helpers and the ticketed voice WebSocket. SolidJS owns reactive state,
rendering and the allow-listed Portal voice adapter. All routing, authorization,
model selection, tool policy, sessions and workspace operations remain in Go.

The public SDK exposes only browser-facing service groups. The internal
`ModelGatewayService`, `AgentRuntimeService`, voice ticket redemption and tool
dispatch remain private network contracts even though their Protobuf
descriptors are generated from the same source. Server authorization is always
authoritative; SDK surface reduction is defense in depth, not an access-control
boundary.

## Runtime and model selection

The frontend reads runtimes and the model catalog from OMAI. When it submits a
turn, `PromptRequest` carries all three selected identifiers:

```text
runtime_id   = "go-adk"
provider_id  = "openrouter"
model_id     = "anthropic/claude-sonnet-4.5"
```

For a workspace coding turn the same tuple may select `runtime_id =
"opencode"`. That changes the private runtime adapter only: SolidJS still uses
the same OMAI Project/Session/Prompt contract, and Go still resolves the model
route and owns the event stream.

`provider_id` and `model_id` must be supplied together. Omitting both retains
temporary backwards compatibility and uses the runtime's configured default;
new frontend code should always send the explicit pair. Never infer a provider
from a model ID: model IDs are unique only within a provider. Never send an API
key to the browser or store it in frontend state.

The current OMAI model picker already tracks provider, model and runtime. Its
prompt adapter must pass all three values to the generated `PromptRequest`.
Regenerate the client after adopting the new fields; do not edit generated
TypeScript manually.

`ModelCatalogService` retains the existing `google.protobuf.Struct` wire
boundary. Provider requests now honor `runtime_id`, `query`, `connected_only`
and `limit`. Model list/search requests accept `provider_id`, `runtime_id`,
`query`, `offset` and `limit`; responses add `total`, `offset` and
`next_offset` without removing the existing `providers` and `models` arrays.
Models contain the current models.dev discovery metadata in normalized Go
records, including costs, modalities, capabilities and reasoning options. The
frontend must use `ready`, not catalog presence alone, to enable selection.
`runtime_ids` lists every agent runtime allowed to use the canonical model;
`runtime_id` remains the primary Go ADK execution owner for backwards
compatibility. Filter by the selected runtime instead of duplicating model
records in SolidJS.

## Voice connection

The SolidJS client calls `omai.voice.connect()` after resolving a workspace.
The SDK mints the ticket with normal Connect authentication, constructs the
voice-gateway WebSocket, waits for `ready`, bounds frames and exposes a typed
async event stream. Microphone frames are PCM16, 16 kHz, mono, little-endian
binary WebSocket messages. Returned audio is PCM16, 24 kHz, mono; the
authoritative rates remain the negotiated values in the typed ready event. JSON
is used only for the bounded control messages documented in `VOICE.md`. The
frontend must display `confirmation_required`, call `voice.confirm()`, and
clear queued provider audio on `interrupted`.

When the gateway sends `ui_command`, the SolidJS adapter dispatches only one of the allow-listed actions described in `PORTAL_VOICE_ADAPTER.md`. It returns `ui_result` with the same `request_id` only after the router/store/editor operation succeeds. Receiving a command is not success.

Terminal output and LSP output are server streams with monotonically increasing cursors. Reconnect with the last applied cursor. `OUT_OF_RANGE` means the bounded replay window expired and the client must reload terminal/LSP state instead of silently skipping output.

Workspace file watching is also a Go server stream. Its first event is
`resync`, which proves the executor installed every requested watch; the Portal
then refreshes its authoritative file state. Permission and question resources
are loaded and resolved through the OMAI SDK, and live changes arrive as typed
cases on the platform Session stream.

File search carries an explicit `file`, `directory` or `any` discriminator;
the private executor receives the same intent instead of inferring it from UI
state. Directory creation is idempotent and contained beneath a resolved Go
workspace. The Portal must never concatenate a host path into an ad-hoc HTTP
request.

Every file read returns a `sha256:` content revision. Editor saves send the
last observed revision and Go rejects a stale save with Connect code `ABORTED`.
Move/Rename and Delete are typed RPCs; deleting the workspace root and mutating
symlinks are forbidden. Monaco uses `omai/monaco-workspace.ts` and never reads
the browser host filesystem directly.

ZIP import is a bounded unary ConnectRPC operation because browsers cannot
portably provide a client-streaming request body. Go validates the compressed
archive, extracts into a private staging directory and publishes only into an
empty workspace. ZIP export is server-streamed. The SDK is the only frontend
entry point for both operations.

MCP List/Upsert/Delete configure durable Go resources. Configuration is not a
connectivity claim: until the Go MCP runtime has started the process/transport,
completed authentication and discovered capabilities, the UI reports the
server as unavailable and exposes no resources.
