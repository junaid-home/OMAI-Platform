# OMAI command catalog

The full-permission voice lease currently exposes 50 unary commands. Production users see only commands allowed by their immutable lease permissions. Streaming RPCs and internal voice/runtime lifecycle methods are never sent to the voice provider. A Go regression test derives and pins this count from the compiled descriptors.

## Agent and workspace

- `list_agent_runtimes`, `prompt_agent`, `cancel_agent`
- `resolve_workspace`, `list_workspaces`, `list_files`, `create_directory`
- `read_file`, `write_file`, `move_workspace_path`, `delete_workspace_path`
- `search_files`, `search_text`

The control plane binds `workspace_id` and `root` to the voice lease. These fields are removed from the provider schema and cannot be redirected by the model.

## Git and processes

- `git_init`, `git_status`, `git_diff`, `git_diff_files`, `git_stage`, `git_unstage`, `git_commit`
- `list_git_worktrees`, `create_git_worktree`, `remove_git_worktree`, `git_merge`
- `create_terminal`, `list_terminals`

`create_terminal` always requires explicit user confirmation. Terminal input, resize, removal and output watching remain direct ConnectRPC operations and are not exposed as model voice tools. LSP lifecycle is also a direct Portal operation rather than an LLM voice tool.

## Platform data

- `list_mcp_servers`, `upsert_mcp_server`, `delete_mcp_server`
- `get_runtime_health`, `list_runtime_health`
- `list_conversation_messages`
- `get_model_catalog`, `list_model_providers`, `list_models`, `get_model`, `search_models`
- `analyze_workspace_preview`, `get_workspace_preview`, `start_workspace_preview`, `restart_workspace_preview`, `stop_workspace_preview`

## Portal control

- `navigate_portal`
- `open_portal_workspace`
- `open_project_dialog`
- `open_portal_file`
- `set_portal_panel`
- `show_portal_preview`
- `select_portal_runtime`
- `open_portal_command_palette`

Portal commands complete only after SolidJS returns a matching `ui_result` acknowledgement.

## Direct ConnectRPC services

Non-voice clients additionally have health, event/preview streams, reflection, the full terminal lifecycle, the full LSP lifecycle and the internal voice admission/dispatch contract. `AgentRuntimeService` and `ModelGatewayService` are implemented by the isolated Go ADK runtime and are not mounted as control-plane or voice tools.
