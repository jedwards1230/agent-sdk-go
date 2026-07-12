package tool

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// LS lists a directory's immediate entries.
type LS struct {
	root string
}

// NewLS returns an LS that resolves relative paths against root and lists
// root itself when no path is given.
func NewLS(root string) *LS {
	return &LS{root: root}
}

// Name returns "ls".
func (t *LS) Name() string { return "ls" }

// Description returns the model-facing description of LS.
func (t *LS) Description() string {
	return "List a directory's immediate entries, one per line, sorted; " +
		"subdirectories are suffixed with \"/\"."
}

// Spec returns the JSON Schema for LS's input.
func (t *LS) Spec() Schema {
	return ObjectSchema(nil, map[string]Property{
		"path": {
			Type:        "string",
			Description: "Directory to list (default: the tool's working directory).",
		},
	})
}

// lsInput is the decoded shape of LS's Run argument.
type lsInput struct {
	Path string `json:"path"`
}

// Run lists the directory's entries. A path that does not exist or is not a
// directory is returned as an IsError [Result]; an empty directory returns
// Content "".
func (t *LS) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in lsInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}

	p := t.root
	label := in.Path
	if label == "" {
		label = "."
	} else {
		p = resolvePath(t.root, in.Path)
	}

	info, err := os.Stat(p)
	switch {
	case os.IsNotExist(err):
		return errorResult("ls: not found: %s", label), nil
	case err != nil:
		return errorResult("ls: %s: %v", label, err), nil
	case !info.IsDir():
		return errorResult("ls: %s is not a directory", label), nil
	}

	entries, err := os.ReadDir(p)
	if err != nil {
		return errorResult("ls: %s: %v", label, err), nil
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)

	return Result{Content: strings.Join(names, "\n")}, nil
}
