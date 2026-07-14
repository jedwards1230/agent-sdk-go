package lsp

import (
	"context"
	"fmt"
)

// Severity mirrors the LSP DiagnosticSeverity (1..4), highest-first.
type Severity int

const (
	SeverityError Severity = iota + 1
	SeverityWarning
	SeverityInformation
	SeverityHint
)

// String returns the lowercase severity name ("error", "warning",
// "information", "hint"), or "severity(N)" for a value outside 1..4 (e.g. a
// server that omits severity, which decodes as the Severity zero value).
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "information"
	case SeverityHint:
		return "hint"
	default:
		return fmt.Sprintf("severity(%d)", int(s))
	}
}

// Position is a zero-based line/character offset, LSP semantics.
type Position struct {
	Line      int
	Character int
}

// Range is a span between two Positions.
type Range struct {
	Start Position
	End   Position
}

// Diagnostic is one normalized LSP diagnostic.
type Diagnostic struct {
	Range    Range
	Severity Severity
	Code     string
	Source   string
	Message  string
}

// String renders d as a single line, without file context: "<severity>
// <line>:<col>: <message> [<source>]" — 1-based line/column for human/editor
// convention (Range itself stays 0-based, LSP wire semantics). Source is
// appended only when non-empty. Use [Batch.Strings] to render a full batch
// with its URI included, matching the mapping this package's doc comment
// describes.
func (d Diagnostic) String() string {
	s := fmt.Sprintf("%s %d:%d: %s", d.Severity, d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message)
	if d.Source != "" {
		s += " [" + d.Source + "]"
	}
	return s
}

// Batch is one server's diagnostics for one document version — the payload of
// a textDocument/publishDiagnostics notification, normalized.
type Batch struct {
	URI     string
	Version int
	Items   []Diagnostic
}

// Strings renders every Diagnostic in b as a one-line string, e.g. "error
// file:///repo/foo.go:12:3: undefined: x [gopls]" (severity, URI, 1-based
// line:column, message, source). This is the intended convenience for
// assigning a Batch straight onto event.ToolCallFinished.Diagnostics or
// loop.ToolResult.Diagnostics (both []string; named here for documentation
// only — this package never imports either) without the consumer hand-rolling
// the format itself.
func (b Batch) Strings() []string {
	out := make([]string, len(b.Items))
	for i, d := range b.Items {
		loc := fmt.Sprintf("%d:%d", d.Range.Start.Line+1, d.Range.Start.Character+1)
		s := fmt.Sprintf("%s %s:%s: %s", d.Severity, b.URI, loc, d.Message)
		if d.Source != "" {
			s += " [" + d.Source + "]"
		}
		out[i] = s
	}
	return out
}

// Publisher is the diagnostics seam. [Client] hands every server
// textDocument/publishDiagnostics notification to a Publisher as a
// normalized Batch; the consuming application decides how to render it — e.g.
// onto event.ToolCallFinished.Diagnostics or a dedicated event of its own.
// The SDK defines ONLY this interface — the lsp package never imports the
// event package, so the seam stays neutral: a second application can wire
// Publish to a different stream unchanged. This mirrors the loop package's
// Container seam.
type Publisher interface {
	Publish(ctx context.Context, session string, batch Batch)
}

// PublisherFunc adapts an ordinary func to a Publisher.
type PublisherFunc func(ctx context.Context, session string, batch Batch)

// Publish calls f.
func (f PublisherFunc) Publish(ctx context.Context, session string, batch Batch) {
	f(ctx, session, batch)
}
