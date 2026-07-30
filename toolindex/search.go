package toolindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// SearchToolName is the stable name tool_search registers under and the model
// calls it by. It is exported so an embedder can recognize the tool (e.g. to
// exclude it from a permission prompt) without a magic string.
const SearchToolName = "tool_search"

// searchDescription is the fixed, model-facing description of tool_search: it
// states the promotion contract in plain language, since that contract is
// what makes the index-first design legible to the model rather than a dead
// end.
const searchDescription = "Search the tool index by name, summary, or source keyword. " +
	"Matched tools are promoted: their full schemas become callable starting the next turn. " +
	"Call this before guessing an unresolved tool name."

// searchTool is the tool.Tool implementation SearchTool returns. Its schema
// is fixed and minimal by design — {query, limit?} — and its result text
// never carries a matched tool's schema, only its indexed summary: putting
// schemas in the result body would double-bill the exact tokens this whole
// package exists to save.
type searchTool struct {
	ix *Index
}

func (t *searchTool) Name() string        { return SearchToolName }
func (t *searchTool) Description() string { return searchDescription }

func (t *searchTool) Spec() tool.Schema {
	return tool.ObjectSchema([]string{"query"}, map[string]tool.Property{
		"query": {
			Type:        "string",
			Description: "text matched case-insensitively against each indexed tool's name, summary, and source",
		},
		"limit": {
			Type:        "integer",
			Description: "max results to return; defaults to the index's configured MaxResults",
		},
	})
}

// searchInput is tool_search's JSON input, matching its fixed schema.
type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Run searches the index, batch-promotes every match (so N discoveries cost
// one Specs()-tail rewrite instead of N — see [Index.Promote]), and returns
// the matched entries plus one line stating those tools' full schemas are
// available from the next turn. It never returns a match's schema.
func (t *searchTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	var in searchInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return tool.Result{}, fmt.Errorf("tool_search: invalid input: %w", err)
		}
	}
	results := t.ix.Search(in.Query, in.Limit)
	if len(results) == 0 {
		return tool.Result{Content: fmt.Sprintf("no tools matched %q", in.Query)}, nil
	}

	names := make([]string, len(results))
	lines := make([]string, 0, len(results)+1)
	for i, e := range results {
		names[i] = e.Name
		lines = append(lines, fmt.Sprintf("%s — %s (%s)", e.Name, e.Summary, e.Source))
	}
	t.ix.Promote(names...)
	lines = append(lines, fmt.Sprintf("Full schemas for %s are available starting next turn.", strings.Join(names, ", ")))
	return tool.Result{Content: strings.Join(lines, "\n")}, nil
}

// SearchTool returns the tool_search tool.Tool. Register it into the base
// tool.Registry BEFORE calling [Index.Wrap] — Index's construction is
// deliberately two-phase so the search tool is present in the very snapshot
// Wrap takes, and is therefore always resolvable via Get and forced resident
// by Wrap.
func (ix *Index) SearchTool() tool.Tool {
	return &searchTool{ix: ix}
}
