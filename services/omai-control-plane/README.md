# OMAI Go Backend

OMAI's backend is a Go control plane built from the API contract used by the existing SolidJS client. It contains no Farbig frontend, embedded OpenCode SDK, Node control-plane runtime, or UI code. An optional leaf driver can supervise an externally pinned OpenCode CLI inside the workspace sandbox.

## What is implemented

- Hexagonal core: domain, application services, ports, inbound ConnectRPC, and outbound filesystem/Git/runtime adapters.
- Canonical Protobuf contracts served as Connect, gRPC, and gRPC-Web by ConnectRPC.
- Stable `omai.platform.v1` Project/Session resources with pagination, basic
  lifecycle operations and a typed `oneof` event stream; `uab.v1` remains the
  explicitly named compatibility surface for infrastructure and runtimes.
- Standard gRPC health and optional gRPC reflection.
- Descriptor-driven OMAI tool registry. Permissions, risk, confirmation, executor, timeout, and JSON input schemas come from compiled Protobuf method options.
- Exact `uab.v1` frontend wire compatibility for control-plane, workspace gateway, native workspace/Git/MCP/runtime/conversation services, and the Struct-based model catalog.
- Production `@omai/sdk-web` v1: pinned Protobuf-ES/Connect-ES generation, one authenticated transport, typed service groups, validated camel-case model catalog, bounded JSON helpers, normalized errors, server streams and a ticketed binary voice client. No platform business logic is duplicated in TypeScript.
- Tenant isolation, method-level permissions, HS256 validation, service/dev tokens, explicit CORS, rate limiting, bounded I/O, symlink containment, atomic writes, and safe argv-based Git execution.
- Stable `AgentRuntimeService` boundary plus an isolated Go ADK v2.2.0 runtime under `integration/adk`.
- Production-oriented Go coding-harness supervisor with one concrete OpenCode
  reference driver: direct argv/stdin execution, process-group cancellation,
  bounded JSONL normalization, atomic native-session resume and a loopback-only
  per-turn model capability. OpenCode calls the Go `ModelGatewayService`; it
  never receives a provider credential.
- Central `ModelGatewayService`: all text/coding-agent model calls use one provider-neutral ConnectRPC stream backed by the Go ADK `model.LLM` interface. Provider SDKs and credentials never enter the core, browser, or another harness.
- Config-driven Google, OpenAI Responses, OpenRouter Responses, DeepSeek Chat
  Completions, and loopback routing with explicit provider/model allowlists and
  bounded clients.
- Full vendored models.dev catalog (159 providers / 5,634 models in the included snapshot), followed by an hourly atomic live refresh. A failed refresh retains the last valid snapshot.
- Complete current models.dev metadata mapping for costs and tiers, cache/audio/reasoning prices, token limits, modalities, capabilities, reasoning options, provider overrides, release status, knowledge dates and experimental modes.
- Platform-wide live voice: binary browser WebSocket, Gemini Live adapter, distributed one-time admission and the same reflected OMAI tools used by text clients.
- Current Gemini 3.1 Live wire format with explicit 16 kHz input/24 kHz output negotiation and barge-in signalling.
- Real PTY terminals, bounded replay, stdio language-server sessions, Git stage/unstage and registered reflection/health entries.
- Go-owned preview runtime: deterministic Node/Go/Python/Rust/Java/PHP/static
  detection, explicit multi-service DAGs, dependency preparation, executor-local
  port allocation, process/log supervision, atomic restart after agent code
  changes and 192-bit HTTP/WebSocket capability routes. The SolidJS portal no
  longer calls OpenCode's preview lifecycle.
- Private `omai.executor.v1` gRPC boundary plus a dedicated Go workspace-executor binary. In production the control plane refuses to start without a remote executor; the leaf executor is pinned to one tenant/workspace, requires mTLS and a separate bearer capability, and is designed to run inside OpenSandbox/Firecracker.
- Browser-owned portal commands with a mandatory SolidJS acknowledgement before NEO can report success.
- Redis-backed voice tool-call idempotency across control-plane and gateway replicas.
- Redis-backed Project, Session, conversation, event, MCP, permission and
  question resources, plus distributed turn leases and cross-replica event
  fan-out. The supplied Redis deployment uses AOF with `noeviction`.

The `uab.v1` name is deliberately a wire-compatibility namespace. All product and implementation names are OMAI.

## Generate, verify, run

Requirements: Go 1.26.x, Buf 1.72.x and Node.js 24.x.

```sh
cp .env.example .env
make bootstrap
make verify
make run
```

The browser creates one `@omai/sdk-web` client with
`VITE_OMAI_API_BASE_URL=http://127.0.0.1:8787` and, when voice is enabled,
`VITE_OMAI_VOICE_GATEWAY_URL=ws://127.0.0.1:8791`. Generated Go and TypeScript
sources are committed and must be regenerated from `api/`, never edited by
hand.

For a reproducible build without host tooling:

```sh
docker compose up --build omai
```

For the complete Linux release gate, run the live audit from the full platform
archive. It produces a linked Markdown report, individual logs, source hashes,
compiled binaries, live ConnectRPC/gRPC evidence, security denials and load
metrics:

```sh
./RUN_LIVE_AUDIT.sh --bootstrap --docker
```

One real, paid OpenCode-ACP-to-DeepSeek turn can be added with
`--real-deepseek-acp`; the key is read only from `DEEPSEEK_API_KEY`, remains in
Go ADK and is never passed to OpenCode. See the live-audit guide before running
it.

The backend-only equivalent is `./scripts/live-audit-linux.sh --bootstrap`;
Portal and real OpenCode-source checks require the full archive. See
[Linux live audit](docs/LIVE_AUDIT.md).

## Runtime configuration

`configs/runtimes.example.json` configures the direct Go ADK runtime;
`configs/runtimes.harness.example.json` shows a production mTLS registration for
both the leaf OpenCode harness and Go ADK. Secrets are referenced by
environment-variable name and are never stored in JSON.
`configs/models.example.json` is the minimal normalized fallback.
`configs/models.dev.snapshot.json` is the complete offline source snapshot, and
`configs/model-sync.example.json` maps it into the Go domain before attempting
a live models.dev refresh. `integration/adk/config/providers.example.json` is
the executable routing allowlist. Catalog exposure and executable routing are
deliberately separate: unrouted providers remain discoverable but unavailable,
while a callable model must be ready in the catalog and independently allowed
by the ADK provider configuration.

The image includes `/etc/omai/models.dev.snapshot.json` and `/etc/omai/model-sync.json`. With `OMAI_MODEL_SYNC_FILE` enabled, startup prefers the last valid normalized cache, falls back to the offline source snapshot, then refreshes models.dev in the background and every hour. Remove that environment variable when deployment policy forbids catalog egress. Docker stores the atomically replaced normalized cache in the `omai-model-cache` volume at `/var/lib/omai/models.generated.json`.

Go ADK v2.2.0 supplies the model abstraction. Google uses ADK's native Gemini
model. OpenAI routes use ADK's experimental Responses adapter; OpenRouter's
Responses endpoint is currently beta. DeepSeek uses OMAI's Go Chat Completions
`model.LLM` adapter because its public API is Chat-Completions-compatible. Add
a provider only by implementing a reviewed adapter, extending allowlist
validation and shipping contract plus live acceptance tests.

The live voice gateway remains a separate bidirectional Gemini Live transport because the provider-neutral `ModelGatewayService` is a generation stream, not a realtime audio-session contract. Voice-discovered commands still execute through the same reflected OMAI authorization and application core as text clients.

Enable the deterministic local runtime only for integration work with `OMAI_ENABLE_DEMO_RUNTIME=true`.

With full permissions the reflected voice catalog exposes 50 executable commands. The catalog is permission-filtered per lease and hides server-bound workspace/root arguments from the voice model.

When Redis is configured, the control plane persists Projects, Sessions,
conversations, bounded event replay, MCP configuration, permission requests,
questions, Voice admission/idempotency and distributed turn ownership in the
shared store. Production refuses to start without Redis; the memory adapters
are development-only. Redis AOF supplies restart durability for this release.
The stores intentionally remain behind ports so a later PostgreSQL plus
transactional-outbox adapter can provide multi-record atomicity without
changing handlers or the SDK. The current prompt/message/event writes are
separate durable operations, not a claimed relational transaction. A deployed
ADK runtime must separately configure a production session service when its
agent graph retains provider-side state.
Workspace processes are local only when `OMAI_ENV` is not production and no
executor URL is configured. Production requires `OMAI_EXECUTOR_URL`; the
control plane then proxies terminal and LSP lifecycle over the private gRPC
executor contract. The leaf executor must be provisioned inside the assigned
microVM with only that workspace and the reviewed toolchain mounted. There is
no production fallback to `os/exec` in the control-plane process.

See [Architecture](docs/ARCHITECTURE.md), [Coding harness](docs/HARNESS.md),
[Go-owned preview](docs/PREVIEW.md),
[Linux live audit](docs/LIVE_AUDIT.md),
[Workspace executor](docs/EXECUTOR.md), [Web SDK](docs/SDK.md),
[Voice](docs/VOICE.md), [command catalog](docs/COMMANDS.md),
[Portal voice adapter](docs/PORTAL_VOICE_ADAPTER.md),
[Frontend contract](docs/FRONTEND_CONTRACT.md), [Security](docs/SECURITY.md), the
[real-life security acceptance](docs/SECURITY_ACCEPTANCE_2026-08-15.md), and
[migration notes](docs/MIGRATION_FROM_FARBIG.md).
