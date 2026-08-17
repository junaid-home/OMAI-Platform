# OMAI Platform

OMAI is a Go-owned coding workspace platform with a SolidJS Portal. The
browser is a presentation layer; Go owns identity, authorization, sessions,
workspace access, Git, terminals, LSP, previews, voice commands, model routing
and coding-harness supervision.

## Repository layout

```text
packages/app                 SolidJS Portal
packages/ui                  OMAI component library
packages/session-ui          Session and message presentation
packages/platform-utils      Dependency-free browser utilities
packages/omai-sdk-web        Portal workspace adapter for the canonical SDK
packages/sdk/js              Temporary OpenCode wire-type compatibility package
services/omai-control-plane  Go control plane, executor, voice and Go ADK
```

Only the six packages required by the Portal are Bun workspaces. Historical
OpenCode backend packages, websites, consoles, prompts, stories and benchmark
fixtures are not part of this repository.

## Architecture

```text
SolidJS Portal
      |
      | @omai/sdk-web / ConnectRPC / Protobuf
      v
Go control plane
      |-- auth, policy, sessions and reflection
      |-- Go ADK 2.2 and model gateway
      |-- voice command dispatch
      |
      | private gRPC / mTLS / capability scope
      v
Go workspace executor
      |-- files, revisions and archives
      |-- Git, PTY, LSP and preview processes
      v
isolated tenant workspace
```

OpenCode is supported only as an external, replaceable coding harness. Its
source code and provider credentials are not bundled. Go gives a harness an
assigned workspace and a short-lived loopback model capability.

## Requirements

- Go 1.26.x
- Bun 1.3.14
- Node.js 24.x
- Buf 1.72.x

## Development

```sh
bun install --frozen-lockfile
cp .env.sample .env

# terminal 1
bun run dev:backend

# terminal 2
bun run dev:web
```

The Portal connects to the Go control plane through the values in `.env`.

## Verification

Fast local verification:

```sh
bun run verify:portal
npm --prefix services/omai-control-plane/sdk/typescript run verify
cd services/omai-control-plane && make verify
```

Complete Linux audit:

```sh
./RUN_LIVE_AUDIT.sh --bootstrap --docker
```

The external OpenCode source harness probe is optional. Supply absolute paths
when that adapter must be tested:

```sh
export OMAI_TEST_OPENCODE_COMMAND=/absolute/path/to/bun
export OMAI_TEST_OPENCODE_ENTRY=/absolute/path/to/opencode/src/index.ts
./RUN_LIVE_AUDIT.sh
```

Provider secrets must be injected at runtime. They must never be committed to
the repository or written into an image.

## Contracts

Canonical Protobuf definitions live only in
`services/omai-control-plane/api`. Generated Go and TypeScript code must be
regenerated with Buf and never edited manually.

See [architecture](docs/ARCHITECTURE.md),
[compatibility boundary](docs/COMPATIBILITY.md), and the backend
[security model](services/omai-control-plane/docs/SECURITY.md).
