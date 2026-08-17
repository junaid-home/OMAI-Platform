# OMAI Voice

Voice is a platform channel, not a feature hidden in one frontend. It reuses the same tenant identity, workspace, permissions, Protobuf descriptors, confirmation policy, and ConnectRPC handlers as text clients.

## Components

1. An authenticated browser calls `VoiceControlService.MintTicket` on the control plane.
2. The browser opens `/omai/voice/ws?ticket=...` on any voice-gateway replica.
3. The gateway atomically redeems the opaque one-time ticket. The control plane creates a short distributed lease and returns the immutable user claims.
4. The gateway requests only unary reflected tools allowed by those claims.
5. Binary PCM16/16 kHz/mono microphone frames flow directly between browser and gateway. The gateway bridges them to Gemini Live and returns PCM16/24 kHz/mono provider audio as binary frames.
6. Model tool calls go to `VoiceControlService.Dispatch`. The control plane validates the exact Protobuf input, permissions, risk, confirmation and idempotency policy, then invokes the same local Connect handler used by non-voice clients.
7. For a `client.portal` result, the gateway sends `ui_command` and waits for the matching `ui_result`. Only that acknowledgement completes the provider tool call.
8. A heartbeat refreshes the lease. Disconnect, timeout, or process loss releases it explicitly or by TTL.

WebSocket text messages are small control envelopes:

- server: `ready`, `transcript`, `interrupted`, `confirmation_required`, `ui_command`, `tool_result`, `turn_complete`, `error`, `pong`;
- client: `interrupt`, `confirm`, `ui_result`, `ping`.

The `ready` envelope includes `input_sample_rate_hz` and
`output_sample_rate_hz`; clients must configure capture and playback from those
values rather than assuming both directions use the same rate. On barge-in the
browser immediately clears queued playback and resumes microphone streaming.
Gemini's VAD then emits `interrupted`, which confirms that the server turn and
any pending provider calls were cancelled. A client `interrupt` also flushes
the current input stream. Transcript envelopes include `role: "user"` or
`role: "assistant"`. Gemini context-window compression is enabled for active
sessions; audio itself is never persisted by OMAI.

Portal command example:

```json
{"type":"ui_command","request_id":"call-17","tool":"open_portal_file","action":"open_file","timeout_ms":5000,"payload":{"workspace_id":"wsp_...","path":"cmd/omai-server/main.go","line":42,"column":1}}
```

The browser replies only after the action has actually committed in UI state:

```json
{"type":"ui_result","request_id":"call-17","success":true,"payload":{"opened":true}}
```

Failures use a bounded machine code such as `INVALID_PAYLOAD`, `UNSUPPORTED_ACTION`, or `UI_EXECUTION_FAILED`. The frontend must cache final acknowledgements by `request_id` for the lifetime of the WebSocket so a replayed idempotent command does not repeat the UI side effect.

Audio is never base64 encoded on the browser boundary. This removes JSON/base64 overhead and bounds every frame to 64 KiB.

## Horizontal scaling

Voice gateways are stateless between connections. A load balancer may send each new upgrade to any healthy replica; an established WebSocket naturally remains on that replica. Redis owns one-time ticket redemption, per-actor admission limits, expiring leases and atomic dispatch fingerprints/results. No provider session or audio is persisted. Control-plane replicas share the production Redis implementations of the Project, Session, conversation, event, MCP, permission, question and turn-lease ports.

Production configuration refuses to start without Redis. Development may use the memory lease store. Use a private network between gateway and control plane, a distinct service token, TLS at the edge, WebSocket-aware idle timeouts, and log redaction for the `ticket` query parameter.

Metrics use the separate `OMAI_VOICE_METRICS_ADDR` listener (default
`127.0.0.1:9092`). They are never exposed on the browser-facing WebSocket
listener; production ingress must keep the metrics listener private.

## Provider portability

`internal/voice/provider` is the hexagonal real-time provider port. Gemini Live is the first adapter. Qwen Omni or another duplex provider can be added without changing ticketing, browser protocol, tool policy or session orchestration.
