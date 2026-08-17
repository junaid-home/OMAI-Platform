# Security model

- Production requires a strong service token or HS256 key. Dev tokens are rejected in production.
- JWT validation pins algorithm, issuer, audience, expiration, optional not-before, tenant, actor, and bounded permissions.
- Every annotated RPC permission is derived from the compiled descriptor registry and checked before dispatch.
- Allowed origins and workspace roots are explicit. Filesystem operations reject traversal and escaping symlinks; writes are bounded and atomic.
- Git is invoked directly with argv, never through a shell. Refs, relative paths, staging targets, output size, and managed-worktree locations are validated.
- Terminal commands are direct argv execution in the selected workspace, never interpolated shell text. PTY/LSP buffers, input frames, environment overrides, process counts and working directories are bounded. Production refuses an in-process executor: it requires the private executor endpoint, HTTPS, client certificate/CA configuration and a separate bearer capability. The leaf executor pins one tenant/workspace, requires mTLS, and must run inside the assigned microVM/container sandbox.
- The optional coding-harness driver starts a fixed operator command with direct
  argv and prompt stdin, a minimal environment, a killed-as-one process group
  and bounded JSONL/stderr. Native session mappings are atomic mode-`0600`
  state outside the workspace. Auto-approval is safe only inside the disposable
  OS sandbox. The OpenCode driver injects a hard `external_directory=deny`
  policy, but the harness is still arbitrary-code execution by design and the
  OS boundary remains authoritative.
- Harness model access terminates at a loopback-only Go edge. Each turn receives
  a 256-bit capability bound to tenant, actor, session, provider and model; only
  its SHA-256 lookup key is retained and the lease is revoked on completion.
  Provider keys are never placed in the harness environment. The capability may
  be visible to guest child processes, so the microVM must deny unreviewed
  egress and enforce resource limits.
- The preview adapter accepts only a configured HTTP(S) origin, blocks absolute targets and credential/forwarding headers, and disables redirects.
- Runtime and model-provider credentials are loaded indirectly from validated environment-variable names. Provider configuration rejects credentials in URLs, remote cleartext endpoints, unbounded timeouts, invalid model IDs, and routes outside explicit allowlists.
- The ADK runtime applies a 48 MiB transport cap plus tighter message, part, tool, inline-data and JSON-schema limits. Its agent, model-gateway, health and reflection endpoints require a distinct bearer token and belong only on a private network. Production startup requires TLS 1.3 server identity and verified client certificates.
- Voice uses opaque one-time tickets stored only as SHA-256 digests, atomic Redis redemption, expiring leases, per-actor limits, exact origin checks, bounded binary frames, replica-safe tool-call idempotency and control-plane-owned tool authorization.
- Production Project, Session, conversation, event, MCP and interaction state is tenant-keyed in Redis. Turn admission uses compare-owner leases; losing renewal cancels execution. Permission/question decisions are immutable after their first successful response and conflicting retries fail closed.
- Browser-owned voice actions use an allow-listed Portal Protobuf service and require a matching `ui_result`; the model never treats command acceptance as execution success.
- `@omai/sdk-web` obtains a fresh access token for every RPC, rejects control characters and conflicting identity headers, rejects non-loopback cleartext endpoints by default, bounds model-catalog/JSON/voice inputs, fails closed on unknown voice envelopes and never stores provider credentials. The server still derives production tenant, actor and permissions only from verified JWT claims.

Terminate TLS at this process or a trusted same-host proxy. Do not expose development mode, the demo runtime, metrics, or reflection publicly. Production refuses the memory-store assembly and requires Redis. Treat models.dev as discovery metadata, not an authorization source: executable routes remain governed by the independently validated ADK provider configuration. A catalog provider's `connected` flag means it is assigned to an enabled runtime route; it is deliberately not proof that a provider credential is present or accepted.
