package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// ToolName is the name a [NewTool]-constructed [tool.Tool] registers under.
const ToolName = "skill"

// NewTool returns an optional [tool.Tool] that projects set's current
// [Set.Index] into its Description — one line per skill, name and
// description — and reads the requested skill's body from disk only in Run.
// This is one instantiation of the invocation surface, not the only one: an
// embedder is equally free to inject the index into a system prompt or
// expose a slash command instead, using [Set.Index] and [Set.Body] directly.
// NewTool is never registered implicitly; a caller that wants it must
// construct one and call [tool.Registry.Register] itself.
func NewTool(set *Set) tool.Tool { return skillTool{set: set} }

type skillTool struct{ set *Set }

func (t skillTool) Name() string { return ToolName }

// Description lists the currently loaded skills — name and (budget-
// truncated) description — so the model can choose one by name. It is
// evaluated fresh on every call (the loop re-projects tool specs once per
// model call), so a Set that changes between turns is reflected without any
// extra wiring.
func (t skillTool) Description() string {
	idx := t.set.Index()
	if len(idx) == 0 {
		return "Loads the instructions for a named skill. No skills are currently available."
	}
	var b strings.Builder
	b.WriteString("Loads the full instructions for a named skill. Available skills:\n")
	for _, m := range idx {
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
	}
	return b.String()
}

func (t skillTool) Spec() tool.Schema {
	names := make([]string, 0, t.set.Len())
	for _, m := range t.set.Index() {
		names = append(names, m.Name)
	}
	return tool.ObjectSchema([]string{"name"}, map[string]tool.Property{
		"name": {
			Type:        "string",
			Description: "The skill to load, from the tool description's list.",
			Enum:        names,
		},
	})
}

func (t skillTool) Run(_ context.Context, input json.RawMessage) (tool.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return tool.Result{}, fmt.Errorf("skill: invalid input: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return tool.Result{}, errors.New("skill: name is required")
	}
	body, err := t.set.Body(args.Name)
	if err != nil {
		// An unknown or unreadable skill is a model-correctable mistake (it
		// picked a name outside the list, or the file changed on disk since
		// the index was built), not a call the loop should treat as
		// malformed — see tool.Tool.Run's (Result, error) split.
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	// FullResult: a skill body is the thing the model explicitly asked to
	// load by name, already capped at Options.MaxBodyBytes — it must not be
	// re-truncated by the loop's spill excerpt behavior.
	return tool.Result{Content: body, FullResult: true}, nil
}
