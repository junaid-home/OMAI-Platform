# OMAI Web SDK

`@omai/sdk-web` v1 is the only supported browser entry point for the OMAI Go
control plane. It replaces the transport-facing portion of the old OpenCode
SDK without moving platform decisions into the browser.

## Ownership

| Concern | SDK | Go control plane |
|---|---:|---:|
| Protobuf request/response types | generated | generated |
| Connect transport and auth metadata | yes | verifies |
| Stream cancellation and deadlines | yes | enforces |
| models.dev response shape | validates/maps | owns/normalizes |
| Model and provider routing | no | yes |
| Model runtime compatibility and token limits | validates/displays | owns |
| Session and event truth | no | yes |
| Workspace, Git, terminal and LSP policy | no | yes |
| Tool permissions and confirmation | no | yes |
| Voice binary/control framing | yes | owns session/policy |
| Preview trigger/status/log stream | yes | owns detection/process/route |
| Provider credentials | never | private runtime only |

## Package layout

```text
sdk/typescript
├── src/gen/       Buf-owned Protobuf-ES output
├── src/client.ts  transport and browser service groups
├── src/sessions.ts Project/Session lifecycle facade
├── src/events.ts  stable typed event algebra and legacy projection
├── src/catalog.ts validated models.dev compatibility mapping
├── src/voice.ts   ticketed bounded realtime transport
├── src/preview.ts typed Go-owned preview lifecycle facade
├── src/workspaces.ts revision-safe files, archives and workspace streams
├── src/errors.ts  Connect error normalization
├── src/json.ts    bounded event/tool JSON helpers
├── src/proto.ts   low-level generated type export
└── test/          in-process RPC and protocol tests
```

`omai.platform.v1` is the stable Project/Session contract. The legacy `uab.v1`
name remains only for infrastructure and runtime wire compatibility during the
cutover. The public NPM package, curated facade, source comments and runtime
behavior are OMAI-owned; both namespaces are served concurrently until the
measured compatibility backlog reaches zero.

## Release contract

- Protobuf field numbers and method cardinality are compatibility boundaries.
- Buf lint and code generation run before SDK verification.
- Protobuf-ES, Connect-ES, TypeScript and test dependencies are exactly pinned
  in `package-lock.json`.
- Strict TypeScript, exact optional properties, targeted SDK tests, ESM package
  compilation and package-content inspection are release gates.
- Generated source is never edited manually.
- Writes and streams are never retried automatically. A caller may retry only
  a procedure whose reflected policy declares it idempotent.
- Browser JWT identity comes from signed claims. Development/service identity
  headers are explicit and cannot be overridden through the generic header
  provider.
- Editor writes use the revision returned by `readFile`; `ABORTED`
  means the UI must reload or compare. Archive import is bounded and archive
  export is assembled from a Go server stream.

The package-level README contains SolidJS, model selection, streaming, voice
and error-handling examples.
