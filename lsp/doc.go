// Package lsp is the SDK's Language Server Protocol seam: a curated server
// registry and a stdlib-only JSON-RPC-over-stdio client, wired to a neutral
// diagnostics-publishing interface the consuming application maps onto its own
// event stream.
//
// # Registry
//
// [Registry] maps a language ID (e.g. "go", "python") to a [Server] launch
// spec and resolves its command on PATH. [DefaultRegistry] seeds a small,
// hand-curated table of common servers (gopls, typescript-language-server,
// pyright, rust-analyzer, clangd) — not the ~370-server nvim-lspconfig
// dataset. An embedded dataset of that size, a lazy per-file-event manager
// that starts/stops servers as files are opened, and model-facing
// `lsp_diagnostics`/`lsp_references`/`lsp_restart` tools are a later
// consuming layer built on top of this package; they do not belong here (see
// docs/DESIGN.md's "LSP" section for the shipped-vs-future split).
//
// # The diagnostics seam
//
// [Publisher] is the seam: the [Client] hands every server
// textDocument/publishDiagnostics notification to a Publisher as a normalized
// [Batch]. This package defines ONLY that interface — deciding how (or
// whether) diagnostics reach a model or a UI is the consuming application's
// job, exactly like the loop package's Container seam ("The SDK defines ONLY
// this interface — concrete backends live in the consuming application").
// lsp deliberately does not import the event package: [Batch.Strings] renders
// each diagnostic as a one-line string so a consumer can assign the result
// straight onto event.ToolCallFinished.Diagnostics or loop.ToolResult.Diagnostics
// (both already exist, named here for documentation only, never imported)
// without lsp taking a reverse dependency on either. A second application can
// therefore wire Publisher to a wholly different stream, unchanged.
//
// # JSON-RPC-over-stdio client
//
// [Client] speaks the LSP base protocol (Content-Length-framed JSON-RPC 2.0)
// over an [io.ReadWriteCloser] Transport. The framing and message shapes are
// hand-rolled in protocol.go rather than pulled in as a dependency: the LSP
// base protocol is a few dozen lines of header parsing and JSON envelopes, so
// a third-party jsonrpc library would violate this package's stdlib-only rule
// for a trivial amount of code. [Start] spawns a real server via os/exec and
// wires its stdio into a Transport; that path is not exercised in CI (no LSP
// servers are installed in the CI image). Tests instead script a fake server
// over an in-memory io.Pipe Transport, which exercises the framing, request/
// response routing, and diagnostics normalization end to end.
//
// This package is a stdlib-only leaf: it imports nothing from the rest of the
// SDK (event, loop, runner, ...), satisfying the SDK's membership invariant —
// code belongs here only if a second application could use it unchanged.
package lsp
