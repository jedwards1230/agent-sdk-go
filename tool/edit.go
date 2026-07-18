package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Edit performs an exact string replacement within a single file.
type Edit struct {
	root string
}

// NewEdit returns an Edit that resolves relative paths against root.
func NewEdit(root string) *Edit {
	return &Edit{root: root}
}

// Name returns "edit".
func (t *Edit) Name() string { return "edit" }

// Description returns the model-facing description of Edit.
func (t *Edit) Description() string {
	return "Replace an exact string in a file with another string. old_string must " +
		"match exactly once unless replace_all is set; include enough surrounding " +
		"context to make it unique."
}

// Spec returns the JSON Schema for Edit's input.
func (t *Edit) Spec() Schema {
	return ObjectSchema([]string{"path", "old_string", "new_string"}, map[string]Property{
		"path": {
			Type:        "string",
			Description: "File path to edit (absolute, or relative to the tool's working directory).",
		},
		"old_string": {
			Type:        "string",
			Description: "The exact text to replace. Must be unique in the file unless replace_all is set.",
		},
		"new_string": {
			Type:        "string",
			Description: "The text to replace old_string with.",
		},
		"replace_all": {
			Type:        "boolean",
			Description: "Replace every occurrence of old_string instead of requiring a unique match.",
		},
	})
}

// editInput is the decoded shape of Edit's Run argument.
type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// Run applies the replacement and writes the file back with its existing
// permissions. A missing file, identical old/new strings, an empty
// old_string, a zero-match, or a non-unique match without replace_all are all
// returned as IsError [Result] values, never an error.
func (t *Edit) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in editInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Path == "" {
		return Result{}, fmt.Errorf("tool: edit: path is required")
	}

	p := resolvePath(t.root, in.Path)
	info, err := os.Stat(p)
	switch {
	case os.IsNotExist(err):
		return errorResult("edit: file not found"), nil
	case err != nil:
		return errorResult("edit: %s: %v", in.Path, err), nil
	case info.IsDir():
		return errorResult("edit: %s is a directory", in.Path), nil
	}

	if in.OldString == in.NewString {
		return errorResult("old_string and new_string are identical"), nil
	}
	if in.OldString == "" {
		return errorResult("old_string must not be empty (use write to create a file)"), nil
	}

	data, err := os.ReadFile(p) // #nosec G304 -- path resolution is intentionally unconfined at this layer (see resolvePath)
	if err != nil {
		return errorResult("edit: %s: %v", in.Path, err), nil
	}
	content := string(data)

	count := strings.Count(content, in.OldString)
	if count == 0 {
		return errorResult("old_string not found in %s", in.Path), nil
	}
	if count > 1 && !in.ReplaceAll {
		return errorResult("old_string is not unique (%d matches); add surrounding context or pass replace_all", count), nil
	}

	replacements := 1
	updated := strings.Replace(content, in.OldString, in.NewString, 1)
	if in.ReplaceAll {
		updated = strings.ReplaceAll(content, in.OldString, in.NewString)
		replacements = count
	}

	if err := os.WriteFile(p, []byte(updated), info.Mode().Perm()); err != nil {
		return errorResult("edit: writing %s: %v", in.Path, err), nil
	}

	plural := "s"
	if replacements == 1 {
		plural = ""
	}
	return Result{
		Content: fmt.Sprintf("edited %s (%d replacement%s)", in.Path, replacements, plural),
		Metadata: Metadata{FileChange: &FileChange{
			Path:    in.Path,
			OldText: content,
			NewText: updated,
		}},
	}, nil
}
