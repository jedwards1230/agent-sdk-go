// Package tool provides the [Tool] interface, a [Registry], and a set of
// importable builtin coding tools (bash, read, edit, write, grep, glob, ls).
// Tools are policy-free — permissions and hooks (M3) wrap them from outside.
// The agent loop consumes the [Tool] interface; this package depends only on the
// stdlib-only leaf [spill] package (bash streams its output to a per-call spill
// sink taken from the context), never on the loop, session, or event machinery.
package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is one capability the agent loop can invoke. Implementations are
// policy-free: permissions and hooks (M3) wrap a Tool from outside, never
// inside it.
type Tool interface {
	// Name is the stable identifier the model calls the tool by. It must be
	// unique within a Registry.
	Name() string
	// Description is the natural-language description shown to the model.
	Description() string
	// Spec is the JSON Schema for the tool's input object, as the model sees it.
	Spec() Schema
	// Run executes the tool with the model-supplied JSON arguments. Cancelling
	// ctx aborts a running tool — for bash this kills the subprocess.
	//
	// The (Result, error) split is precise:
	//   - A non-nil error means the call could not run as asked: malformed
	//     input JSON, a missing required argument, or ctx cancellation
	//     (ctx.Err()). The loop relays these to the model as an error result,
	//     except a context error, which aborts the turn.
	//   - Every operational failure the model should see and react to — file
	//     not found, no/duplicate match, a non-zero exit — is returned as a
	//     Result with IsError set, not as an error.
	Run(ctx context.Context, input json.RawMessage) (Result, error)
}

// Result is the outcome of a [Tool.Run]. Content is the text handed back to
// the model; Metadata carries structured, out-of-band detail that does not
// enter the model's context unless a client chooses to surface it.
type Result struct {
	// Content is the tool output the model reads.
	Content string
	// IsError reports that the tool ran but its operation failed in a way the
	// model should see and correct. It is distinct from a non-nil error
	// return, which signals an invalid or interrupted call (see [Tool.Run]).
	IsError bool
	// FullResult asks the loop to hand the model this Content in full rather
	// than the bounded spill excerpt. It is the escape hatch for a tool whose
	// output is bounded by the operation the model explicitly asked for — the
	// read tool sets it so an explicit file read is never truncated to
	// head+tail. The output is still spilled to disk for durability; only the
	// model-facing/journaled text changes. Streaming tools with unbounded
	// output (bash) must leave it false — that is the memory-safety path.
	FullResult bool
	// Metadata is structured, machine-readable detail about the run.
	Metadata Metadata
}

// Metadata is structured detail about a [Tool.Run] that rides alongside the
// model-facing Content.
type Metadata struct {
	// ExitCode is the subprocess exit status for tools that spawn one (bash).
	// It is nil for tools that do not.
	ExitCode *int
	// Truncated reports that Content was capped at the tool's output limit.
	Truncated bool
	// Diagnostics is a slot the loop fills with LSP diagnostics gathered
	// around the tool call (M3). Tools never populate it themselves.
	Diagnostics []Diagnostic
	// FileChange, when non-nil, records a file the tool created or modified: its
	// path and full content before and after. It lets a client render a
	// structured diff and never enters the model's context. The edit and write
	// tools set it.
	FileChange *FileChange
	// Extra holds tool-specific structured fields for clients that want them.
	Extra map[string]any
}

// FileChange is a structured before/after record of a single file mutation a
// tool performed. OldText is empty when the file did not exist before (a
// creation). It rides in [Metadata] as out-of-band detail a client may render
// as a diff.
type FileChange struct {
	// Path is the file's path as the tool was asked to change it.
	Path string
	// OldText is the file's content before the change, empty for a creation.
	OldText string
	// NewText is the file's content after the change.
	NewText string
}

// Diagnostic is a single LSP-style diagnostic. It is defined here so the
// M3 loop can inject diagnostics into a [Result] without a new type; builtin
// tools never emit them.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"` // "error", "warning", "info", or "hint"
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"` // producing server, e.g. "gopls"
}

// Schema is a JSON Schema description of a tool's input. It marshals to a
// JSON Schema object the model consumes directly; Type is "object" for tool
// inputs.
type Schema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property is one field of a [Schema]. It covers the shapes the builtins
// need; nested objects use Properties, arrays use Items.
type Property struct {
	Type        string              `json:"type,omitempty"`
	Description string              `json:"description,omitempty"`
	Enum        []string            `json:"enum,omitempty"`
	Items       *Property           `json:"items,omitempty"`
	Properties  map[string]Property `json:"properties,omitempty"`
	Default     any                 `json:"default,omitempty"`
}

// ObjectSchema builds a [Schema] of Type "object" from required field names
// and a property map.
func ObjectSchema(required []string, props map[string]Property) Schema {
	return Schema{Type: "object", Properties: props, Required: required}
}

// errorResult builds a [Result] with IsError set and Content formatted from
// format and args, per [fmt.Sprintf]. It is shared across builtins.
func errorResult(format string, args ...any) Result {
	return Result{IsError: true, Content: fmt.Sprintf(format, args...)}
}
