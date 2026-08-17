# OMAI Go ADK runtime

This module is the independently deployable model boundary for OMAI. It keeps
Google ADK and provider types outside the hexagonal core and serves two stable
ConnectRPC contracts:

- `uab.v1.AgentRuntimeService` runs the built-in OMAI ADK agent;
- `uab.v1.ModelGatewayService` gives additional coding/workflow harnesses the
  same provider-neutral model stream without exposing provider SDKs or keys.

The adapter is pinned to Go ADK v2.2.0 and its matching GenAI v1.66 line.

Implemented capabilities:

- per-turn provider/model routing stored in the Go context, without rebuilding
  the ADK runner or losing its session;
- Google Gemini through ADK's native driver;
- OpenAI and compatible Responses endpoints through ADK's experimental
  `openaimodel` driver;
- DeepSeek and other reviewed OpenAI-compatible Chat Completions endpoints
  through OMAI's Go `model.LLM` adapter, including function calls, reasoning
  content, usage and bounded credential-safe errors;
- OpenRouter model fan-out through its Responses endpoint;
- provider/model allowlists, bounded HTTP clients, TLS enforcement, loopback-
  only anonymous HTTP, and bounded least-recently-used model-client reuse;
- normalized multi-message model input, text/inline media/function parts,
  JSON-schema tools, generation settings, usage, errors, and streaming output;
- streamed assistant and thought chunks;
- normalized function-call and function-response events;
- one active turn per OMAI session;
- real session cancellation through `AgentRuntimeService.Cancel`;
- bearer-token authentication on every RPC.
- authenticated standard gRPC health plus v1/v1alpha reflection, with native
  Go 1.26 unencrypted HTTP/2 support for private-network deployments.
- TLS 1.3 and verified client certificates are mandatory when
  `OMAI_ADK_ENV=production`.

Generate the root Protobuf code first, then run the adapter:

```sh
export GOOGLE_API_KEY='...'
export OMAI_RUNTIME_TOKEN='replace-with-at-least-32-characters'
export OMAI_ADK_PROVIDERS_FILE='./config/providers.example.json'
make run-adk
```

`OPENAI_API_KEY`, `OPENROUTER_API_KEY` and `DEEPSEEK_API_KEY` are required only
when their routes are selected. The example's default is Google. Provider JSON
contains only the names of secret environment variables, never secret values.

For an internal reflection check (the token is mandatory):

```sh
grpcurl -plaintext \
  -H "authorization: Bearer ${OMAI_RUNTIME_TOKEN}" \
  127.0.0.1:8790 list
```

Enable the matching entry in `configs/runtimes.example.json`, set
`OMAI_ADK_RUNTIME_TOKEN` to the same token for the control plane, and configure
`OMAI_RUNTIMES_FILE` with that file. In a container or cluster, change the
runtime endpoint from loopback to the private service address.

A coding harness does not call a provider directly. The leaf Go supervisor
issues a per-turn loopback capability and bridges the harness protocol to this
module's `ModelGatewayService`. See `../../docs/HARNESS.md` for the complete
path and production mTLS configuration.

The `ModelGatewayService` transports tool declarations and tool-call results;
it does not execute a tool. Authorization and execution remain in the OMAI
descriptor registry and control plane. A harness must call that boundary and
must never duplicate permissions or business logic in this module.

Coverage is intentionally honest: ADK v2.2.0 does not ship a native adapter for
every vendor. Direct Google and OpenAI Responses are supported here. DeepSeek
uses OMAI's reviewed Chat Completions `model.LLM` adapter because DeepSeek does
not expose the Responses contract. Additional models remain available through
a configured Responses-compatible gateway such as OpenRouter. Each provider
still needs its own live contract and failure test before production rollout.

`ModelGatewayService` itself is stateless and can scale horizontally. The
built-in `AgentRuntimeService` currently uses ADK's `NewInMemory` runner, so its
conversation sessions require one replica/sticky routing and do not survive a
restart. Before multi-replica production, construct `runner.New` with ADK's
database-backed `session.Service` (PostgreSQL is the recommended deployment)
and a durable artifact/memory adapter. This limitation is explicit rather than
hidden behind a misleading scalability claim.
