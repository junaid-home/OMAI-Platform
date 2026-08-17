# Linux live audit

`scripts/live-audit-linux.sh` is the executable release-acceptance harness. It
is intentionally Linux-only and leaves a complete evidence directory instead
of reducing the result to a single green line.

From the complete OMAI repository:

```sh
./RUN_LIVE_AUDIT.sh --bootstrap --docker
```

For a clean machine, prefer the repository-level containerized runner:

```sh
./RUN_E2E_DOCKER.sh
```

That path needs only Linux, Docker Engine and Docker Compose v2. Its audit
image pins and contains the complete test toolchain, Chromium and all locked
Go/JavaScript dependencies. It also starts the full production Compose graph,
exercises Redis-backed state across a control-plane restart and runs a real
browser-to-ConnectRPC smoke test. The native-host command remains useful for CI
agents that intentionally manage their own toolchain.

To add the opt-in, paid real-provider acceptance path, enter a fresh DeepSeek
key without writing it into the repository or an argument:

```sh
read -rsp 'DeepSeek API key: ' DEEPSEEK_API_KEY && printf '\n'
export DEEPSEEK_API_KEY
./RUN_LIVE_AUDIT.sh --bootstrap --real-deepseek-acp
unset DEEPSEEK_API_KEY
```

`RUN_E2E_DOCKER.sh --real-deepseek-acp` additionally accepts the key at a
hidden prompt or through `DEEPSEEK_API_KEY_FILE`. It mounts a mode-0600 file
read-only and never stores the key in an image, Docker argument, Compose
environment or report.

The default is the current `deepseek-v4-flash` model. Override it with
`--deepseek-model deepseek-v4-pro`. The legacy `deepseek-chat` alias is not the
default because DeepSeek discontinued it on 24 July 2026. The paid step is
never run implicitly, so an ordinary audit cannot spend provider credit.

From a standalone control-plane checkout:

```sh
./scripts/live-audit-linux.sh --bootstrap
```

The standalone form records the Portal and real OpenCode-source checks as
skipped because those sources are not part of the backend-only archive. Use the
complete repository for the end-to-end release gate. `--bootstrap` downloads Go
modules, installs the exact SDK lockfile and runs the root Bun lockfile install;
omit it when the environment has already been prepared.

## Required tools

- Linux with Bash, GNU coreutils, curl and Python 3;
- Go 1.26 or newer;
- Buf 1.72 or newer;
- Node.js 24 or newer, npm and Bun 1.3 or newer;
- `staticcheck`, `gosec`, `govulncheck` and `grpcurl` on `PATH`;
- Docker with the Compose plugin when `--docker` is selected.

The script fails preflight when a full-audit tool is absent. For a diagnostic
run only, `--allow-missing-tools` records unavailable optional scanners instead.
Such a run is not a release acceptance result.

## What it executes

1. Records the kernel and exact tool versions without dumping environment
   variables or credentials.
2. Creates a SHA-256 manifest of the audited source files.
3. Verifies Go formatting, module checksums, Buf lint/code generation drift,
   `go vet`, race-enabled tests and static Linux builds.
4. Runs `staticcheck`, `gosec` and symbol-level `govulncheck` against both Go
   modules.
5. Runs strict SDK typechecking, all SDK tests, package build and package
   inspection.
6. Typechecks the SolidJS Portal and starts the actual OpenCode TypeScript
   entrypoint through the Go harness. That test proves model routing through Go,
   workspace escape denial, an allowed workspace write and native-session
   resume.
7. With `--real-deepseek-acp`, starts the compiled Go ADK runtime with the
   DeepSeek Chat Completions adapter, connects to the real OpenCode source via
   ACP stdio JSON-RPC, and performs one real provider turn through
   `OpenCode ACP -> Go capability edge -> ModelGatewayService -> Go ADK ->
   DeepSeek`. The provider key is scoped only to ADK; the probe asserts that it
   is absent from OpenCode and scans the generated logs/evidence for leakage.
8. Starts the compiled Go control plane on ephemeral loopback ports and checks:
   authenticated ConnectRPC health/runtime/model calls, a real deterministic
   agent turn and stored assistant response, unauthenticated denial, workspace
   escape denial, untrusted-origin rejection, native gRPC reflection, metrics
   and a concurrent ConnectRPC load probe.
9. With `--docker`, validates Compose and builds the server, executor, voice and
   ADK production image targets.

The Docker E2E wrapper adds full-stack health convergence, Portal Chromium
rendering, browser CORS/ConnectRPC, an actual terminal command crossing the
control-plane-to-executor gRPC boundary and conversation recovery after
restarting the Redis-backed control plane.

The default load probe uses 500 requests and 32 workers. Increase it without
editing the script:

```sh
./RUN_LIVE_AUDIT.sh --bootstrap --requests 10000 --concurrency 128
```

## Evidence directory

The default output is
`services/omai-control-plane/audit-results/<UTC timestamp>-<PID>/` and contains:

- `REPORT.md`: human-readable PASS/FAIL table with links to every log;
- `summary.tsv`: machine-readable step summary;
- `environment.txt`: kernel and toolchain provenance;
- `source-files.sha256`: exact audited-source manifest;
- `logs/`: complete output for every check;
- `live/`: server log, API responses, reflection inventory, metrics and load
  latency/throughput JSON;
- `live/deepseek-acp.json`: credential-safe ACP/provider evidence when the paid
  probe was selected;
- `bin/`: the four binaries built from the audited source.

A green local report proves this source and the exercised Linux execution path.
Without `--real-deepseek-acp`, it intentionally makes no claim about a live
provider account. It cannot certify real production certificates, PostgreSQL
or Redis failover, the final cluster network policy, or the syscall boundary of
the deployed OpenSandbox/Firecracker image. Those require staging tests against
the actual infrastructure.
