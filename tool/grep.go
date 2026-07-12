package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// grepMatchCap is the maximum number of matches Grep collects before it stops
// scanning.
const grepMatchCap = 1000

// errGrepCap is an internal sentinel returned from the WalkDir callback to
// stop the walk once grepMatchCap is reached; it is not surfaced as an error.
var errGrepCap = errors.New("tool: grep: match cap reached")

// Grep searches file contents with a Go regular expression. It never shells
// out to an external grep/ripgrep binary.
type Grep struct {
	root string
}

// NewGrep returns a Grep that searches under root by default.
func NewGrep(root string) *Grep {
	return &Grep{root: root}
}

// Name returns "grep".
func (t *Grep) Name() string { return "grep" }

// Description returns the model-facing description of Grep.
func (t *Grep) Description() string {
	return "Search file contents with a regular expression (Go regexp syntax). " +
		"Returns matching lines as \"path:line:text\". Skips .git directories and " +
		"binary files."
}

// Spec returns the JSON Schema for Grep's input.
func (t *Grep) Spec() Schema {
	return ObjectSchema([]string{"pattern"}, map[string]Property{
		"pattern": {
			Type:        "string",
			Description: "Regular expression to search for (Go regexp syntax).",
		},
		"path": {
			Type:        "string",
			Description: "File or directory to search (default: the tool's working directory).",
		},
		"glob": {
			Type:        "string",
			Description: "Only search files whose path (relative to the search root) matches this glob, e.g. \"**/*.go\".",
		},
		"ignore_case": {
			Type:        "boolean",
			Description: "Match case-insensitively.",
		},
	})
}

// grepInput is the decoded shape of Grep's Run argument.
type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	IgnoreCase bool   `json:"ignore_case"`
}

// Run walks the search path, skipping ".git" directories and binary files
// (a NUL byte anywhere in the file), and returns matching lines. Output is
// capped at grepMatchCap matches and defaultMaxOutputBytes bytes. A bad
// pattern is returned as an IsError [Result]; no matches returns Content
// "no matches" with IsError false.
func (t *Grep) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in grepInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("tool: grep: pattern is required")
	}

	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return errorResult("grep: invalid pattern: %v", err), nil
	}
	if in.Glob != "" {
		if err := validateGlob(in.Glob); err != nil {
			return errorResult("grep: invalid glob: %v", err), nil
		}
	}

	searchRoot := t.root
	if in.Path != "" {
		searchRoot = resolvePath(t.root, in.Path)
	}
	if _, err := os.Stat(searchRoot); err != nil {
		return errorResult("grep: %s: %v", in.Path, err), nil
	}

	var out strings.Builder
	matches := 0
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

		relRoot, err := filepath.Rel(t.root, path)
		if err != nil {
			return nil
		}
		relRootSlash := filepath.ToSlash(relRoot)

		if in.Glob != "" {
			relSearch, err := filepath.Rel(searchRoot, path)
			if err != nil {
				return nil
			}
			ok, gErr := matchGlob(in.Glob, filepath.ToSlash(relSearch))
			if gErr != nil || !ok {
				return nil
			}
		}

		data, err := os.ReadFile(path) // #nosec G304 -- path resolution is intentionally unconfined at this layer (see resolvePath)
		if err != nil {
			return nil // unreadable file: skip
		}
		if bytes.IndexByte(data, 0) != -1 {
			return nil // binary guard: a NUL byte anywhere means skip the file
		}

		lineNo := 0
		for _, line := range strings.Split(string(data), "\n") {
			lineNo++
			if !re.MatchString(line) {
				continue
			}
			fmt.Fprintf(&out, "%s:%d:%s\n", relRootSlash, lineNo, line)
			matches++
			if matches >= grepMatchCap {
				capped = true
				return errGrepCap
			}
		}
		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, errGrepCap) {
		return Result{}, walkErr
	}

	if matches == 0 {
		return Result{Content: "no matches"}, nil
	}

	content := out.String()
	if capped {
		content += fmt.Sprintf("… (truncated at %d matches)\n", grepMatchCap)
	}
	content, byteTruncated := truncateBytes(content, defaultMaxOutputBytes)

	return Result{
		Content:  content,
		Metadata: Metadata{Truncated: capped || byteTruncated, Extra: map[string]any{"matches": matches}},
	}, nil
}
