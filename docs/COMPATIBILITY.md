# Compatibility boundary

The workspace cutover is complete: file operations, Git, terminal, LSP,
preview and project browsing contain no direct OpenCode runtime calls.

One small compatibility package, `packages/sdk/js`, remains because parts of
the inherited Portal message view still consume OpenCode wire types. Seven
Portal files retain audited provider or advanced-session calls. The live audit
fails if that allowlist expands.

This compatibility code must be removed by adding equivalent OMAI Protobuf
contracts for:

1. provider authentication and OAuth;
2. session fork, summarize and command operations;
3. attachment and context fallback.

The external OpenCode ACP adapter is not a compatibility backend. It is an
optional harness implementation behind the Go runtime port and can be replaced
without changing the Portal or workspace contracts.
