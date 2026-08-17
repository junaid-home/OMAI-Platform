# ADR 0001: Go owns the platform contract

Status: accepted

## Context

The inherited SolidJS Portal used an OpenCode-generated SDK as transport,
domain model and UI view model. A parallel ConnectRPC proof of concept copied
backend Proto files into the frontend and drifted from the Go schema.

## Decision

The OMAI Go control plane owns every first-party platform contract.

- Canonical Proto sources live beside the Go control plane.
- Buf generates Go and TypeScript descriptors from the same source.
- `@omai/sdk-web` is the only browser transport package.
- The SDK may expose curated, framework-neutral facades but no SolidJS state.
- SolidJS owns rendering and local view state only.
- Go domain types remain independent from generated Proto messages.
- Harness and provider integrations are outbound adapters.
- `uab.v1` is compatibility-only; new stable resources use
  `omai.platform.v1` and are dual-served during migration.

## Consequences

There is one schema and one authorization boundary. Browser and internal gRPC
clients evolve together, while the domain and application layers remain
testable without a transport. The migration requires explicit Project,
Session, Permission and workspace-lifecycle APIs before the legacy backend can
be deleted; package renaming is not considered decoupling.
