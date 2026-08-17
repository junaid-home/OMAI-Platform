# Go-owned workspace executor

## Ownership

Go remains the platform owner. SolidJS calls only the public OMAI SDK and
ConnectRPC surface. The control plane authenticates the user, checks the
descriptor-derived permission and owns workspace/process identity. It delegates
only the final OS process operation to a private Go executor deployed inside the
workspace sandbox.

```text
SolidJS + @omai/sdk-web
       │ public ConnectRPC / gRPC-Web
       ▼
OMAI Go control plane
  auth · policy · OMAI sessions · workspace placement
       │ private gRPC + mTLS + bearer capability
       ▼
Go workspace executor (one tenant/workspace)
  WorkspaceExecutorService · optional AgentRuntimeService
       │ direct argv, bounded env/CWD/PTY/stdio
       ├──────────────► bash · git · curl · LSP
       └── Go harness supervisor ──► pinned coding harness
                                      │ loopback model capability
                                      ▼
                              Go ADK ModelGatewayService
```

The executor protocol supports contained file reads, revision-checked atomic
writes, Move/Delete, safe ZIP import, streamed ZIP export, directory/search/
watch operations, start, get, list, input, terminal resize,
stop/remove, replayable output streaming and bounded one-shot argv execution.
Git uses the one-shot command port, so production Git commands and hooks execute
inside the same sandbox rather than the control-plane process. Process
identifiers are opaque and tenant scoped. A slow consumer is disconnected and
must resume with its last cursor; retained data, command output, runtime and
process history are bounded.

Archive import accepts at most the configured compressed request size
(`OMAI_EXECUTOR_MAX_ARCHIVE_BYTES`, 200 MiB by default), rejects more than
100,000 entries, a single file over 256 MiB or more than 1 GiB expanded data,
and publishes only after staging validation. Export omits `.git`, dependency
and generated build directories and propagates cancellation through the gRPC
stream.

When one executor mount contains several project directories, the control
plane does not collapse them into the mount root. `OMAI_EXECUTOR_CONTROL_ROOT`
names the configured root corresponding to the executor mount and every
private request carries a validated `relative_root`. The executor resolves
that value beneath its own root, rejects absolute paths, traversal and symlink
escapes, and then applies the tenant/workspace pin. Consequently `/workspaces/a`
cannot read or mutate `/workspaces/b`, even though both are visible to the
executor process. A production scheduler should still prefer one leaf per
workspace for OS-level isolation.

## Deployment modes

- Development: omit `OMAI_EXECUTOR_URL` for the local Go adapter, or run the
  Compose executor over private cleartext container networking.
- Production control plane: `OMAI_EXECUTOR_URL` is mandatory and must use
  HTTPS. `OMAI_EXECUTOR_CA_CERT`, `OMAI_EXECUTOR_CLIENT_CERT`,
  `OMAI_EXECUTOR_CLIENT_KEY` and a 32-byte-or-longer executor token are
  mandatory. Set `OMAI_EXECUTOR_CONTROL_ROOT` when more than one configured
  workspace root shares the executor; with one root it defaults to that root.
- Production leaf: `OMAI_EXECUTOR_TENANT_ID` and
  `OMAI_EXECUTOR_WORKSPACE_ID` pin the sandbox. Server certificate/key and the
  control-plane client CA are mandatory. Insecure transport is rejected.
- Harness leaf: set `OMAI_HARNESS_DRIVER=opencode`, an absolute/pinned harness
  installation, state outside the workspace and a private ADK Model Gateway.
  The model edge is always loopback. Production requires mTLS on the executor
  to ADK hop. Register this runtime with
  `configs/runtimes.harness.example.json`.

A scheduler or OpenSandbox adapter provisions one leaf per workspace and routes
the private protocol to it. That placement layer may change without modifying
the domain, public Protobuf contract, SolidJS application or harness adapters.

## Sandbox profile

The microVM/container should mount only the assigned workspace plus an
immutable reviewed toolchain. Run as non-root, drop capabilities, enable
no-new-privileges/seccomp/AppArmor, apply cgroup CPU/memory/PID limits, bound
disk and wall time, and deny egress by default. Provider, JWT, Redis and database
credentials remain in the control plane or ADK runtime and are never copied to
the workspace executor. A harness-enabled leaf holds only its private executor
credential, an mTLS identity for ADK and the ADK service bearer; the guest
receives only a revocable per-turn loopback capability. See `HARNESS.md`.
