# Monaco on the Go-owned workspace

Monaco is an editor view, not a filesystem owner. The integration module is
`packages/app/src/omai/monaco-workspace.ts`.

```mermaid
sequenceDiagram
  participant M as Monaco model
  participant S as @omai/sdk-web
  participant G as OMAI Go control plane
  participant E as Private Go executor
  participant F as Workspace filesystem

  M->>S: open(root, path)
  S->>G: ReadFile(workspace_id, path)
  G->>E: private gRPC ReadFile
  E->>F: contained read
  F-->>M: UTF-8 data + sha256 revision
  M->>S: save(value, last revision)
  S->>G: WriteFile(expected_revision)
  G->>E: private gRPC CAS write
  E->>F: verify revision + temp/fsync/rename
  alt file unchanged
    F-->>M: new revision
  else agent, terminal or editor changed it
    F-->>M: ABORTED stale-revision conflict
  end
```

Rules:

1. Keep the revision beside each Monaco model.
2. Save only with that revision. A new file uses `createOnly`.
3. Never auto-retry `ABORTED`; offer reload/compare to the user.
4. Subscribe to `WatchFiles`. `resync` means reload authoritative state.
5. Rename, Delete, search, Git, terminal, LSP and preview always use the same
   workspace ID and OMAI SDK; no browser filesystem or legacy HTTP endpoint is
   allowed.
