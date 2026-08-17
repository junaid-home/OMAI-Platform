# Complete Docker E2E

`RUN_E2E_DOCKER.sh` is the clean-machine acceptance path for this migration
checkpoint. The host needs Linux, Bash, Docker Engine and Docker Compose v2;
it does not need Go, Node, Bun, Buf, grpcurl, scanners or a browser.

## Run

```sh
unzip OMAI-Go-Platform-Complete-E2E.zip
cd OMAI-Platform-Linux-Live-Audit
./RUN_E2E_DOCKER.sh
```

The first run downloads base images and locked module/package dependencies, so
it is intentionally slower. Later runs reuse Docker's content cache.

For the paid provider route:

```sh
./RUN_E2E_DOCKER.sh --real-deepseek-acp
```

Enter a fresh key at the hidden prompt. `DEEPSEEK_API_KEY` and
`DEEPSEEK_API_KEY_FILE` are also supported, but the runner always copies the
value to a temporary mode-0600 file, unsets the environment value, mounts the
file read-only for the one audit container and removes it on exit. No provider
credential is added to a Docker build, image, command argument, Compose
environment or evidence file.

## Pinned audit toolchain

The toolchain lives in `Dockerfile.e2e`:

| Component | Version |
|---|---:|
| Go | 1.26.6 |
| Node.js | 24.19.0 |
| Bun | 1.3.14 |
| Buf | 1.72.0 |
| Staticcheck | 2026.1 (module v0.7.0) |
| gosec | 2.28.0 |
| govulncheck | 1.7.0 |
| grpcurl | 1.9.3 |
| Playwright | repository lockfile |
| Redis | 8.2.8 Alpine image |
| Nginx | 1.28.3 Alpine image |

Both Go modules are protected by `go.sum`; the SDK uses `npm ci` and its
package lock; the Portal uses `bun install --frozen-lockfile`. `GOTOOLCHAIN` is
set to `local`, preventing an unreviewed automatic Go toolchain replacement.
The resolved Go module cache and installed JavaScript dependency trees are
part of the audit image and are reused by the test run. The report records the
final image identity and exact tool versions. Release
deployment may additionally bind the reviewed image identities by digest.

## Acceptance graph

```text
Pinned audit image
  -> formatting / Buf drift / vet / race / staticcheck / gosec / govulncheck
  -> OMAI SDK typecheck + tests + distribution inspection
  -> SolidJS typecheck + real OpenCode source-harness test
  -> compiled Go live server + security denials + load probe
  -> isolated Redis repository test

Production Compose images
  -> Redis healthy
  -> ADK healthy (explicit private-network development cleartext)
  -> executor healthy
  -> Go control plane healthy
  -> voice gateway healthy
  -> workspace bytes write/read/search through the private executor
  -> Git init/status through the Go Git adapter
  -> LSP inventory and Redis-backed MCP upsert/list/delete
  -> static preview analysis/start/private port/public proxy/stop
  -> one-time voice ticket + lease + reflected tool lifecycle in Go/Redis
  -> SolidJS/Nginx healthy
  -> browser renders Portal and reaches Go through CORS + ConnectRPC
  -> terminal command crosses Go control plane -> private gRPC executor
  -> gRPC reflection exposes canonical OMAI services
  -> control plane restarts
  -> Redis-backed conversation remains authoritative
```

## Evidence

Each run writes `e2e-results/<UTC timestamp>-<PID>/`:

- `E2E_SUMMARY.md`: top-level verdict and evidence index;
- `audit/REPORT.md`: every source, test, scanner and live-server gate;
- `audit/live/`: ConnectRPC, reflection, preview, load and optional provider
  evidence;
- `browser/browser.json` and `browser/portal.png`;
- `compose/create/`: service, workspace, Git, preview, terminal and initial
  Redis-backed turn evidence;
- `compose/create/preview-control.json`: redacted runtime port, gateway content,
  stop and retired-capability evidence;
- `compose/create/voice-control.json`: redacted ticket, replay-denial, lease and
  reflected-tool lifecycle evidence;
- `compose/after-restart/`: persisted history after control-plane restart;
- production image and Compose inventories plus complete build logs.

The runner always tears down its exact temporary containers, network and
volumes. Built images and evidence remain for inspection.

## Boundary of the claim

A green result proves the packaged source, dependency graph and exercised
single-host Linux/Compose behavior. Without `--real-deepseek-acp`, it does not
claim that a paid provider account works. It does not replace staging tests for
real mTLS certificates, OpenSandbox/Firecracker isolation, cluster network
policy, Redis failover, PostgreSQL/outbox durability or production identity.
