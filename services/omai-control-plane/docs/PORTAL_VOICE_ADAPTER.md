# SolidJS Portal voice adapter

The frontend adapter is intentionally small. It receives a validated command from the voice WebSocket, executes one allow-listed SolidJS operation, and acknowledges the final result. It does not interpret arbitrary routes, selectors, JavaScript, shell text, or model-generated event names.

| Action | Payload | SolidJS responsibility |
|---|---|---|
| `navigate` | `view` | Map the canonical view to an application-owned route and navigate. |
| `open_workspace` | `workspace_id` | Select an already resolved workspace and wait for its route/store to settle. |
| `open_project_dialog` | none | Open the existing project picker/creation dialog. |
| `open_file` | `workspace_id`, `path`, optional `line`, `column` | Open the file through the normal file/editor store and position Monaco. |
| `set_panel` | `panel`, `mode` (`show`, `hide`, `toggle`, `focus`) | Apply the panel state through the existing layout store. |
| `show_preview` | `workspace_id`, `path`, `reload` | Open/focus preview and optionally refresh through the normal preview lifecycle. |
| `select_runtime` | `runtime_id` | Select an item already present in the runtime catalog. |
| `open_command_palette` | none | Open the existing command palette. |

Required adapter behavior:

1. Reject unknown actions and unknown payload fields.
2. Verify that workspace/runtime identifiers are already present in authenticated frontend state.
3. Cache the final `ui_result` by `request_id` for the current WebSocket session.
4. On a duplicate request with the same payload, resend the cached result without repeating the action.
5. On a duplicate ID with a different payload, return `IDEMPOTENCY_CONFLICT`.
6. Send success only after the router/store/editor action completes; timeout and thrown errors are failures.
7. Clear the cache when the voice WebSocket closes.

The adapter sends:

```json
{"type":"ui_result","request_id":"call-id","success":true,"payload":{"applied":true}}
```

or:

```json
{"type":"ui_result","request_id":"call-id","success":false,"code":"UI_EXECUTION_FAILED","message":"The editor could not open the file"}
```
