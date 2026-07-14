package loop

import (
	"context"
	"encoding/json"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// FromRegistry adapts a [tool.Registry] to the loop's [ToolRegistry]: it builds
// a [provider.ToolSpec] from each tool's name, description, and JSON-schema
// [tool.Schema], and maps a [tool.Result] to a [ToolResult]. It is the bridge
// between the builtin tool package and the loop's consumer-side interface.
func FromRegistry(r *tool.Registry) ToolRegistry {
	return registryAdapter{r: r}
}

type registryAdapter struct{ r *tool.Registry }

func (a registryAdapter) Get(name string) (Tool, bool) {
	t, ok := a.r.Get(name)
	if !ok {
		return nil, false
	}
	return toolAdapter{t: t}, true
}

func (a registryAdapter) Specs() []provider.ToolSpec {
	tools := a.r.List()
	specs := make([]provider.ToolSpec, 0, len(tools))
	for _, t := range tools {
		var schema json.RawMessage
		if b, err := json.Marshal(t.Spec()); err == nil {
			schema = b
		}
		specs = append(specs, provider.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: schema,
		})
	}
	return specs
}

type toolAdapter struct{ t tool.Tool }

func (a toolAdapter) Run(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	res, err := a.t.Run(ctx, input)
	if err != nil {
		return ToolResult{}, err
	}
	// tool.Result.Metadata.Diagnostics is an M3 slot the builtins never populate;
	// it is not surfaced here at M1.
	return ToolResult{Content: res.Content, IsError: res.IsError, FullResult: res.FullResult}, nil
}
