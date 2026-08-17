# Go-owned preview runtime

OMAI preview is a control-plane capability, not a frontend dev-server helper.
The SolidJS portal selects a workspace through `@omai/sdk-web`; only Go
analyzes the repository, decides what may run, starts the process, supervises
it and publishes the browser route.

```text
SolidJS PreviewPanel
  |  @omai/sdk-web: start / restart / get / stop / watchLogs
  v
ConnectRPC + Protobuf  uab.v1.PreviewService
  |
  v
PreviewManager (application core)
  |-- ProjectDetector ---- bounded manifests / .omai/runtime.json
  |-- ProcessManager ----- private omai.executor.v1 gRPC boundary
  |                         |
  |                         `--> dev server in the assigned Linux sandbox
  |-- TCP + HTTP readiness <----- private runtime address
  `-- Publisher ---------- 192-bit capability route + HTTP/WebSocket proxy
                              |
                              `--> sandboxed browser iframe
```

## Runtime decision

Detection is deterministic and does not execute project code. It scans at most
five directory levels, skips dependency/build/VCS trees, rejects symlinked
manifests and caps every manifest read. Native plans cover:

- Node package scripts, including Vite, Next, Astro, Angular, Svelte, Solid and React;
- Go main modules;
- Django, FastAPI and Flask;
- Rust binary crates;
- Maven/Gradle Spring applications;
- PHP Composer applications; and
- static `index.html` projects.

For an unsupported or multi-service application, commit an explicit
`.omai/runtime.json` (or `omai.runtime.json`). Commands remain an argv array;
there is intentionally no shell string:

```json
{
  "version": 1,
  "primary": "web",
  "services": [
    {
      "id": "api",
      "name": "API",
      "workingDir": "apps/api",
      "runtime": "go",
      "run": {
        "command": "go",
        "args": ["run", "."],
        "env": { "HOST": "{{host}}", "PORT": "{{port}}" }
      },
      "preview": false,
      "expectedPorts": [8080]
    },
    {
      "id": "web",
      "name": "Web",
      "workingDir": "apps/web",
      "runtime": "node",
      "framework": "vite",
      "packageManager": "pnpm",
      "install": { "command": "pnpm", "args": ["install", "--frozen-lockfile"] },
      "run": {
        "command": "pnpm",
        "args": ["run", "dev", "--", "--host", "{{host}}", "--port", "{{port}}", "--strictPort"]
      },
      "preview": true,
      "dependsOn": ["api"]
    }
  ]
}
```

Only the primary service and its dependency closure start. Dependencies start
in topological order; any failure rolls back new processes in reverse order.
A restart validates and starts the replacement before retiring the previous
instance. The frontend invokes restart when an agent turn completes, so code
generation is followed by Go-owned re-analysis and readiness before the iframe
receives the new URL.

## Execution and publication

The private executor chooses the candidate port through
`AllocatePreviewPort`; the control plane substitutes only `{{host}}` and
`{{port}}`, then calls the generic argv-based `StartProcess(kind=preview)`.
Stdout and stderr share the existing bounded replay stream. Process groups are
killed as one unit. A crash invalidates the public route and emits
`preview.failed`; an unrenewed browser lease is reaped after the configured
idle timeout.

Dependency preparation defaults to `never`, because package installation runs
repository-controlled lifecycle code. The supplied remote-executor Compose
profile opts into `auto` inside its sandbox; local in-process development must
do so explicitly and only for a trusted workspace.

Public URLs contain 192 random bits. The gateway strips browser Authorization
and Cookie headers before proxying, strips preview `Set-Cookie`, supports
WebSocket upgrades and returns 404 after replacement or stop. Private runtime
addresses are not present in Protobuf responses.

For Vite/Astro HMR in production, configure a wildcard origin:

```text
OMAI_PREVIEW_PUBLIC_BASE_URL=https://omai.example
OMAI_PREVIEW_PUBLIC_URL_TEMPLATE=https://{id}.preview.omai.example/
```

DNS and TLS must cover `*.preview.omai.example`. Path routing remains useful
for simple/local previews, but a separate origin is the durable choice for
frameworks that use root-relative HMR and asset paths. Production startup
therefore rejects a missing URL template; this also ensures repository code in
the sandboxed iframe never shares the Portal's origin.

With a remote executor, bind inside the sandbox and point the gateway at the
executor network identity:

```text
OMAI_PREVIEW_BIND_HOST=0.0.0.0
OMAI_PREVIEW_RUNTIME_HOST=workspace-executor.internal
```

Network policy, filesystem quotas, CPU/memory/PID limits and egress isolation
remain responsibilities of the OpenSandbox/Firecracker deployment. The
control plane deliberately has no production fallback to local execution.
The supplied `executor` image, unlike the minimal control-plane image, carries
the reviewed Node/Bun/pnpm/yarn, Go, Python, Rust, Java/Maven/Gradle and
PHP/Composer toolchains required by the native detectors. Release automation
should pin its base images by digest and rebuild on the normal security cadence.

## Verification

`RUN_LIVE_AUDIT.sh` now creates a real static project, calls Preview `Start`,
fetches its capability URL, changes generated code, calls `Restart`, proves the
old route returns 404, fetches the new content and finally proves `Stop`
invalidates the replacement route. Unit/race tests additionally cover native
detection, explicit-config rejection, symlink containment, credential/header
stripping and concurrent process lifecycle.
