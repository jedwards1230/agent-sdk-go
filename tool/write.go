package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Write creates or overwrites a file with the given content, creating any
// missing parent directories.
type Write struct {
	root string
}

// NewWrite returns a Write that resolves relative paths against root.
func NewWrite(root string) *Write {
	return &Write{root: root}
}

// Name returns "write".
func (t *Write) Name() string { return "write" }

// Description returns the model-facing description of Write.
func (t *Write) Description() string {
	return "Write content to a file, creating it (and any missing parent " +
		"directories) or overwriting it if it already exists."
}

// Spec returns the JSON Schema for Write's input.
func (t *Write) Spec() Schema {
	return ObjectSchema([]string{"path", "content"}, map[string]Property{
		"path": {
			Type:        "string",
			Description: "File path to write (absolute, or relative to the tool's working directory).",
		},
		"content": {
			Type:        "string",
			Description: "The full content to write to the file.",
		},
	})
}

// writeInput is the decoded shape of Write's Run argument.
type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Run creates parent directories (0o755) and writes the file (0o644),
// overwriting any existing content.
func (t *Write) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in writeInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("tool: write: path is required")
	}

	p := resolvePath(t.root, in.Path)
	// Capture the prior content (if the file exists) so the change surfaces as a
	// before/after diff; a read miss leaves oldText empty, marking a creation.
	var oldText string
	if prior, err := os.ReadFile(p); err == nil { // #nosec G304 -- path resolution is intentionally unconfined at this layer (see resolvePath)
		oldText = string(prior)
	}
	if dir := filepath.Dir(p); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- directories are meant to be traversable by the running user
			return errorResult("write: creating %s: %v", dir, err), nil
		}
	}
	if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil { // #nosec G306 -- source files are not secrets
		return errorResult("write: %s: %v", in.Path, err), nil
	}

	return Result{
		Content: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path),
		Metadata: Metadata{FileChange: &FileChange{
			Path:    in.Path,
			OldText: oldText,
			NewText: in.Content,
		}},
	}, nil
}
