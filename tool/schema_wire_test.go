package tool_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/skill"
	"github.com/jedwards1230/agent-sdk-go/tool"
	"github.com/jedwards1230/agent-sdk-go/toolindex"
)

// TestBuiltinSpecWireBytes pins the exact JSON every SDK-owned tool's Spec
// marshals to. loop/toolreg.go marshals Spec() once into a json.RawMessage
// and both provider adapters copy those bytes into the request verbatim, so
// this file IS the prompt the model sees — a stray field or a lost omitempty
// changes every request against every provider.
//
// The strings were captured before [tool.Schema]/[tool.Property] gained their
// composition fields. Their job is to keep that addition additive: if adding
// an optional field to either type ever changes a builtin's wire bytes, this
// test fails rather than the change shipping silently. Update a string here
// only when the tool's own schema deliberately changed.
//
// It lives in tool_test (an external test package) so it can reach the two
// SDK tools defined outside this package — toolindex's tool_search and
// skill's skill — without an import cycle.
func TestBuiltinSpecWireBytes(t *testing.T) {
	emptySkills, _ := skill.Load(nil, skill.Options{})

	tests := []struct {
		tl   tool.Tool
		want string
	}{
		{
			tl:   tool.NewRead("/tmp"),
			want: `{"type":"object","properties":{"limit":{"type":"integer","description":"Maximum number of lines to return (default 2000)."},"offset":{"type":"integer","description":"1-based line number to start reading at (default 1)."},"path":{"type":"string","description":"File path to read (absolute, or relative to the tool's working directory)."}},"required":["path"]}`,
		},
		{
			tl:   tool.NewGlob("/tmp"),
			want: `{"type":"object","properties":{"path":{"type":"string","description":"Directory to search from (default: the tool's working directory)."},"pattern":{"type":"string","description":"\"**\"-aware glob pattern to match, relative to path, e.g. \"**/*.go\"."}},"required":["pattern"]}`,
		},
		{
			tl:   tool.NewGrep("/tmp"),
			want: `{"type":"object","properties":{"glob":{"type":"string","description":"Only search files whose path (relative to the search root) matches this glob, e.g. \"**/*.go\"."},"ignore_case":{"type":"boolean","description":"Match case-insensitively."},"path":{"type":"string","description":"File or directory to search (default: the tool's working directory)."},"pattern":{"type":"string","description":"Regular expression to search for (Go regexp syntax)."}},"required":["pattern"]}`,
		},
		{
			tl:   tool.NewLS("/tmp"),
			want: `{"type":"object","properties":{"path":{"type":"string","description":"Directory to list (default: the tool's working directory)."}}}`,
		},
		{
			tl:   tool.NewBash("/tmp"),
			want: `{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute."},"timeout_ms":{"type":"integer","description":"Maximum time to allow the command to run, in milliseconds (default 120000, max 600000)."}},"required":["command"]}`,
		},
		{
			tl:   tool.NewEdit("/tmp"),
			want: `{"type":"object","properties":{"new_string":{"type":"string","description":"The text to replace old_string with."},"old_string":{"type":"string","description":"The exact text to replace. Must be unique in the file unless replace_all is set."},"path":{"type":"string","description":"File path to edit (absolute, or relative to the tool's working directory)."},"replace_all":{"type":"boolean","description":"Replace every occurrence of old_string instead of requiring a unique match."}},"required":["path","old_string","new_string"]}`,
		},
		{
			tl:   tool.NewWrite("/tmp"),
			want: `{"type":"object","properties":{"content":{"type":"string","description":"The full content to write to the file."},"path":{"type":"string","description":"File path to write (absolute, or relative to the tool's working directory)."}},"required":["path","content"]}`,
		},
		{
			tl:   tool.NewUpdatePlan(),
			want: `{"type":"object","properties":{"entries":{"type":"array","description":"The full current plan, in order. Each entry needs content, priority, and status.","items":{"type":"object","properties":{"content":{"type":"string","description":"Human-readable description of the task."},"priority":{"type":"string","description":"Task priority.","enum":["high","medium","low"]},"status":{"type":"string","description":"Task status.","enum":["pending","in_progress","completed"]}}}}},"required":["entries"]}`,
		},
		{
			tl:   toolindex.New(toolindex.Options{}).SearchTool(),
			want: `{"type":"object","properties":{"limit":{"type":"integer","description":"max results to return; defaults to the index's configured MaxResults"},"query":{"type":"string","description":"text matched case-insensitively against each indexed tool's name, summary, and source"}},"required":["query"]}`,
		},
		{
			tl:   skill.NewTool(emptySkills),
			want: `{"type":"object","properties":{"name":{"type":"string","description":"The skill to load, from the tool description's list."}},"required":["name"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.tl.Name(), func(t *testing.T) {
			got, err := json.Marshal(tt.tl.Spec())
			if err != nil {
				t.Fatalf("marshal Spec(): %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Spec() JSON =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}
