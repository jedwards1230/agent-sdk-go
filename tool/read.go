package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Read reads a file's contents as line-numbered text, similar to "cat -n".
type Read struct {
	root string
}

// NewRead returns a Read that resolves relative paths against root.
func NewRead(root string) *Read {
	return &Read{root: root}
}

// Name returns "read".
func (t *Read) Name() string { return "read" }

// Description returns the model-facing description of Read.
func (t *Read) Description() string {
	return "Read a file's contents with line numbers. For large files, use offset " +
		"and limit to read a window instead of the whole file."
}

// Spec returns the JSON Schema for Read's input.
func (t *Read) Spec() Schema {
	return ObjectSchema([]string{"path"}, map[string]Property{
		"path": {
			Type:        "string",
			Description: "File path to read (absolute, or relative to the tool's working directory).",
		},
		"offset": {
			Type:        "integer",
			Description: "1-based line number to start reading at (default 1).",
		},
		"limit": {
			Type:        "integer",
			Description: "Maximum number of lines to return (default 2000).",
		},
	})
}

// readInput is the decoded shape of Read's Run argument.
type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Run reads the requested window of the file and returns it as
// "%6d\t<line>" rows. A path that does not exist, is a directory, or an
// offset past the end of the file is returned as an IsError [Result], never
// an error.
func (t *Read) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in readInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("tool: read: path is required")
	}

	p := resolvePath(t.root, in.Path)
	info, err := os.Stat(p)
	switch {
	case os.IsNotExist(err):
		return errorResult("read: file not found: %s", in.Path), nil
	case err != nil:
		return errorResult("read: %s: %v", in.Path, err), nil
	case info.IsDir():
		return errorResult("read: %s is a directory", in.Path), nil
	}

	data, err := os.ReadFile(p) // #nosec G304 -- path resolution is intentionally unconfined at this layer (see resolvePath)
	if err != nil {
		return errorResult("read: %s: %v", in.Path, err), nil
	}
	if len(data) == 0 {
		return Result{Content: "", Metadata: Metadata{Extra: map[string]any{"lines": 0}}}, nil
	}

	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		// Trailing "\n" produces a synthetic empty final element; drop it so
		// line counts match the file's actual line count.
		lines = lines[:len(lines)-1]
	}
	total := len(lines)

	offset := in.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 2000
	}

	if offset > total {
		return errorResult("offset %d is past end of file (%d lines)", offset, total), nil
	}

	start := offset - 1
	end := total
	if limit < total-start {
		end = start + limit
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}

	return Result{
		Content:    b.String(),
		FullResult: true, // an explicit read is never capped to the spill excerpt
		Metadata:   Metadata{Extra: map[string]any{"lines": total}},
	}, nil
}
