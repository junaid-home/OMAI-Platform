# Verification

`make verify` performs Buf lint/generation, gofmt, root and ADK `go vet`,
race-enabled root tests, ADK tests, strict SDK typechecking, SDK tests, ESM
package compilation, package-content inspection, and complete builds of the
control plane, workspace executor, voice gateway and ADK runtime. The Docker build repeats Protobuf
generation, both Go module test suites, and static binary builds in a pinned
toolchain.

The following checks were executed in the handoff environment with Go 1.26.6,
Buf 1.72.0, Node.js 24.19.0 and npm 11.9.0:

- Buf lint and generation completed successfully;
- `gofmt`, root and ADK `go vet`, normal tests, race-enabled tests, and builds
  for the control plane, voice gateway and ADK runtime completed successfully;
- `staticcheck`, `gosec` and symbol-level `govulncheck` completed for both Go
  modules; no reachable vulnerability was found;
- `@omai/sdk-web` v1 passed strict TypeScript 7.0.2 checking, 20 targeted tests,
  ESM/declaration compilation and package inspection; the inspected package
  contains 86 files and is approximately 0.49 MiB unpacked;
- the SolidJS application passed its project typecheck, all 7 migrated health
  tests and a full Vite production build;
- an actual SDK-to-server smoke test called health, runtime listing, model
  listing, `ResolveProject` and `CreateSession` through ConnectRPC against the
  compiled Go control plane; it returned a healthy server, the demo runtime,
  the expected seven-model fixture and tenant-scoped Project/Session IDs;

- all 75 annotated public tool names are unique;
- the derived full-permission voice catalog contains exactly the 50 commands
  documented in `COMMANDS.md`;
- all 197 Protobuf message names are unique and every schema has balanced
  structure;
- all enum prefixes/zero values and package-level `go_package` declarations
  satisfy the enabled Buf rules;
- 15 control-plane service handlers, the private workspace-executor handler,
  the optional leaf `AgentRuntimeService`, and 2 authenticated ADK service
  handlers are registered; all three server classes expose their active
  services through gRPC health and reflection where configured;
- Go ADK is pinned to v2.2.0 with GenAI v1.66.0; no older dependency marker
  remains;
- provider/model selection crosses both Protobuf boundaries and is validated by
  the runtime-scoped catalog and the executable ADK allowlist;
- the vendored models.dev snapshot contains exactly 159 providers and 5,634
  models; catalog tests verify rich metadata retention, catalog-only provider
  isolation, defaults, indexed lookup and complete pagination;
- model-gateway conversion covers tools, JSON payload validation, usage,
  thoughts, inline data, function calls/results and bounded streaming;
- executable backend source contains no Farbig/UI code and imports no OpenCode
  SDK. The Go leaf contains a concrete driver for an externally pinned OpenCode
  CLI; the only TypeScript deliverable inside this backend is the OMAI browser
  SDK;
- the underlying Git stage, staged-diff, and unborn-HEAD unstage commands were
  exercised against a temporary repository;
- the real private HTTP/2 gRPC path executed a PTY, replayed/streamed its output,
  ran a bounded one-shot command and rejected wrong bearer/tenant identities;
- the compiled production executor accepted a health request only with both a
  trusted client certificate and the private bearer capability; the TLS 1.3
  listener rejected a client without its certificate before HTTP admission;
- Git stage/status ran through the remote executor command port, and managed
  worktrees remained inside the workspace without polluting repository status;
- the actual OpenCode TypeScript entrypoint ran through the Go supervisor and
  loopback model edge, executed a real `write` tool in a temporary assigned
  workspace, rejected an attempted write outside that workspace, returned both
  tool results through Go, persisted its native session and resumed the same
  session on a second turn;
- 76 root Go test/fuzz declarations, 15 ADK declarations and 20 targeted SDK
  tests were audited.

Docker was not available in the handoff environment, so image construction and
Compose expansion were not executed. The Dockerfile repeats the same successful
generation, test and static-build path in its pinned toolchain, and the included
CI workflow remains the authoritative image release gate. Both Go module
checksum files and the exact SDK `package-lock.json` are included. npm could not
use its default `/root/.npm` cache under the sandbox permission profile, so the
SDK was verified from a temporary dependency tree containing the exact locked
TypeScript 7.0.2 and Vitest 4.1.10 versions; this does not affect the source or
packaged artifact.

Protocol and dependency decisions were checked against the official
[Go ADK v2.2.0 release](https://github.com/google/adk-go/releases/tag/v2.2.0),
[Go ADK model interface](https://github.com/google/adk-go/blob/v2.2.0/model/llm.go),
[models.dev API documentation](https://github.com/anomalyco/models.dev),
[Gemini Live API](https://ai.google.dev/gemini-api/docs/live-api/capabilities),
[Buf STANDARD rules](https://buf.build/docs/lint/rules/),
[Buf code generation](https://buf.build/docs/generate/), and
[Connect for Web](https://connectrpc.com/docs/web/getting-started/). OpenRouter's
Responses compatibility is documented as beta and ADK's OpenAI Responses
adapter as experimental; neither is presented as a universal native-provider
guarantee.
