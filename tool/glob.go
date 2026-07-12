package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// globResultCap is the maximum number of results Glob collects before it
// stops scanning.
const globResultCap = 1000

// errGlobCap is an internal sentinel returned from the WalkDir callback to
// stop the walk once globResultCap is reached; it is not surfaced as an
// error.
var errGlobCap = errors.New("tool: glob: result cap reached")

// Glob finds files by a "**"-aware path pattern (see matchGlob).
type Glob struct {
	root string
}

// NewGlob returns a Glob that searches under root by default.
func NewGlob(root string) *Glob {
	return &Glob{root: root}
}

// Name returns "glob".
func (t *Glob) Name() string { return "glob" }

// Description returns the model-facing description of Glob.
func (t *Glob) Description() string {
	return "Find files by path pattern (supports \"**\" for any number of " +
		"directories, e.g. \"**/*.go\"). Results are sorted lexicographically, " +
		"not by modification time."
}

// Spec returns the JSON Schema for Glob's input.
func (t *Glob) Spec() Schema {
	return ObjectSchema([]string{"pattern"}, map[string]Property{
		"pattern": {
			Type:        "string",
			Description: "\"**\"-aware glob pattern to match, relative to path, e.g. \"**/*.go\".",
		},
		"path": {
			Type:        "string",
			Description: "Directory to search from (default: the tool's working directory).",
		},
	})
}

// globInput is the decoded shape of Glob's Run argument.
type globInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

// Run walks the search path, skipping ".git" directories, and returns
// matching file paths relative to root, forward-slash separated, sorted
// lexicographically, capped at globResultCap. No matches returns Content
// "no matches" with IsError false.
func (t *Glob) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in globInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("tool: glob: pattern is required")
	}

	searchRoot := t.root
	if in.Path != "" {
		searchRoot = resolvePath(t.root, in.Path)
	}
	if _, err := os.Stat(searchRoot); err != nil {
		return errorResult("glob: %s: %v", in.Path, err), nil
	}

	var results []string
	capped := false

	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, don't abort the walk
		}
		if cErr := ctxErr(ctx); cErr != nil {
			return cErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(results) >= globResultCap {
			capped = true
			return errGlobCap
		}

		relSearch, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return nil
		}
		ok, mErr := matchGlob(in.Pattern, filepath.ToSlash(relSearch))
		if mErr != nil {
			return mErr
		}
		if !ok {
			return nil
		}

		relRoot, err := filepath.Rel(t.root, path)
		if err != nil {
			return nil
		}
		results = append(results, filepath.ToSlash(relRoot))
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, errGlobCap) {
		if ctxE := ctx.Err(); ctxE != nil {
			return Result{}, ctxE
		}
		return errorResult("glob: invalid pattern: %v", walkErr), nil
	}

	if len(results) == 0 {
		return Result{Content: "no matches"}, nil
	}

	sort.Strings(results)
	content := strings.Join(results, "\n")
	if capped {
		content += fmt.Sprintf("\n… (truncated at %d results)", globResultCap)
	}

	return Result{
		Content:  content,
		Metadata: Metadata{Truncated: capped, Extra: map[string]any{"count": len(results)}},
	}, nil
}
