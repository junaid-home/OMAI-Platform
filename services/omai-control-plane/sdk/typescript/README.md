# `@omai/sdk-web`

The production browser SDK for the OMAI Go control plane. It is a thin,
framework-neutral layer over generated Protobuf-ES and ConnectRPC clients.
Routing, authorization, model selection, tool policy, workspace ownership and
agent orchestration remain in Go.

## Install in OMAI Web

Use the workspace package while the SDK is private:

```json
{
  "dependencies": {
    "@omai/sdk-web": "file:../../sdk/typescript"
  }
}
```

Create one client for the application lifetime:

```ts
import { createOMAIClient } from "@omai/sdk-web";

export const omai = createOMAIClient({
  baseUrl: import.meta.env.VITE_OMAI_API_BASE_URL,
  voiceGatewayUrl: import.meta.env.VITE_OMAI_VOICE_GATEWAY_URL,
  accessToken: () => authStore.accessToken(),
});
```

`accessToken` returns the token value without the `Bearer` prefix and is called
for every unary or streaming request. Normal browser JWTs derive tenant, actor
and permissions from signed claims. `tenantId` and `actorId` headers are only
needed for explicitly configured development or service tokens.

Production endpoints must use HTTPS. Plain HTTP is rejected unless the host is
loopback or `allowInsecureTransport` is explicitly enabled for a controlled
development environment.

## Services

Every service uses generated request, response and stream types:

```ts
const health = await omai.services.controlPlane.health({});
const workspace = await omai.services.workspace.resolveWorkspace({
  root: "/workspace/project",
});
const status = await omai.services.git.status({
  workspaceId: workspace.workspace?.id,
});
```

Available browser-facing service groups are:

- `projects` (`omai.platform.v1`)
- `sessions` (`omai.platform.v1`)
- `controlPlane`
- `workspaceGateway`
- `workspace`
- `git`
- `terminal`
- `lsp`
- `mcp`
- `runtime`
- `conversations`
- `portal`
- `tools`
- `preview`

Low-level Protobuf types and schemas are available from `@omai/sdk-web/proto`.
The SDK deliberately does not expose the internal model-gateway or agent-runtime
clients as browser service groups.

## Preview

The curated preview facade accepts only a workspace root or a server-issued
workspace ID. It never accepts a command line:

```ts
const running = await omai.preview.start("/workspace/project");

// After an agent code-generation turn:
const replaced = await omai.preview.restart("/workspace/project");
previewFrame.src = replaced.publicUrl;

// A periodic read renews the Go-owned idle lease.
await omai.preview.get(replaced.workspaceId);
await omai.preview.stop(replaced.workspaceId);
```

`start` and `restart` allow up to six minutes for bounded dependency
preparation and readiness. Process logs are a resumable async iterable via
`omai.preview.watchLogs(workspaceId, cursor)`. Detection, argv, ports, process
ownership and public routing remain in Go.

## Models

The models.dev compatibility wire uses `google.protobuf.Struct`. The public SDK
converts it into validated, immutable, camel-case TypeScript records:

```ts
const page = await omai.models.searchModels({
  runtimeId: "go-adk",
  providerId: "openrouter",
  query: "claude sonnet",
  limit: 50,
});

for (const model of page.models) {
  if (model.ready && model.runtimeIds.includes("go-adk")) {
    console.log(model.providerId, model.id, model.limits.context);
  }
}
```

`runtimeId` is the primary Go ADK execution owner retained for compatibility;
`runtimeIds` is the complete immutable set of agent runtimes, including an
enabled coding harness such as `opencode`, that may use the same canonical
provider/model route.

Always send the selected runtime, provider and model together. The curated
facade resolves the Project, creates/adopts the Session and submits the turn as
three explicit application operations:

```ts
const accepted = await omai.sessions.send({
  runtimeId: "go-adk",
  externalSessionId: crypto.randomUUID(),
  root: "/workspace/project",
  text: "Review the current Git diff",
  providerId: "openrouter",
  modelId: "anthropic/claude-sonnet-4.5",
});
```

## Streaming

ConnectRPC server streams are native async iterables. Pass `signal`, `timeoutMs`
or request headers as the optional second argument:

```ts
const controller = new AbortController();
const events = omai.sessions.subscribe({
  sessionId: accepted.sessionId,
  since: 0n,
}, {
  signal: controller.signal,
  timeoutMs: 0,
});

for await (const event of events) {
  // Exhaustive, transport-independent event algebra.
  console.log(event.sequence, event.type);
}
```

Terminal and LSP streams expose monotonically increasing cursors. Resume with
the last applied cursor. `Code.OutOfRange` means the bounded replay window has
expired and the caller must reload current state.

## Voice

The SDK mints a one-time ticket through ConnectRPC, connects to the voice
gateway and waits for the negotiated `ready` envelope before returning:

```ts
const voice = await omai.voice.connect({
  workspaceId: accepted.workspaceId,
  locale: "de-CH",
});

console.log((await voice.ready).inputSampleRateHz);  // 16000 today
console.log((await voice.ready).outputSampleRateHz); // 24000 today

for await (const event of voice) {
  switch (event.type) {
    case "audio":
      playback.enqueue(event.data);
      break;
    case "interrupted":
      playback.clear();
      break;
    case "confirmation_required":
      voice.confirm(event.requestId, await confirmInUI(event.message));
      break;
    case "ui_command": {
      const result = await portalVoiceAdapter.execute(event);
      voice.acknowledgeUI({ requestId: event.requestId, ...result });
      break;
    }
  }
}
```

Send microphone data as PCM16 mono binary frames of at most 64 KiB:

```ts
voice.sendAudio(pcm16Frame);
```

The SDK does not execute UI commands. The SolidJS adapter must allow-list the
action and payload, wait for the router/store/editor mutation to finish, cache
the result by `requestId`, then acknowledge it.

## Errors

Generated clients throw `ConnectError`. Convert at an application boundary
without losing metadata:

```ts
import { asOMAIError } from "@omai/sdk-web";

try {
  await omai.services.git.commit({ workspaceId, message });
} catch (cause) {
  const error = asOMAIError(cause);
  console.error(error.code, error.retryable, error.metadata);
}
```

The SDK does not automatically retry writes or streams. Callers may retry only
operations whose Protobuf policy declares them idempotent.

## Development

The generated source is owned by `api/` and must never be edited by hand:

```sh
buf generate
cd sdk/typescript
npm ci
npm run verify
```

Strict TypeScript, exact optional properties, package build and the SDK test
suite are part of the root `make verify` gate.
