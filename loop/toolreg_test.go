package loop_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// stubTool is a minimal tool.Tool for exercising the registry adapter.
type stubTool struct {
	name string
	ran  bool
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "a stub tool" }
func (s *stubTool) Spec() tool.Schema {
	return tool.ObjectSchema([]string{"x"}, map[string]tool.Property{"x": {Type: "string"}})
}
func (s *stubTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	s.ran = true
	return tool.Result{Content: "stub-ok"}, nil
}

// TestFromRegistry drives the loop through a tool round against a real
// tool.Registry adapted with loop.FromRegistry, confirming Specs() and Get()
// bridge correctly and the tool executes.
func TestFromRegistry(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()

	st := &stubTool{name: "stub"}
	reg := tool.NewRegistry(st)
	adapted := loop.FromRegistry(reg)

	// Specs() carries the tool's name, description, and marshaled JSON schema.
	specs := adapted.Specs()
	if len(specs) != 1 || specs[0].Name != "stub" || specs[0].Description != "a stub tool" {
		t.Fatalf("specs = %+v", specs)
	}
	if len(specs[0].InputSchema) == 0 {
		t.Error("expected a marshaled input schema")
	}

	cfg := loop.Config{Stream: scripted(
		toolTurn("t1", "stub", `{"x":"hi"}`),
		textTurn("done", provider.StopEndTurn),
	), Model: "faux", Broker: b, SessionID: sid, Tools: adapted}

	if _, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("go")}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !st.ran {
		t.Error("adapted tool did not run")
	}
}

// editTool returns a Result carrying a Metadata.FileChange, like the builtin
// edit/write tools.
type editTool struct{ change tool.FileChange }

func (editTool) Name() string        { return "edit" }
func (editTool) Description() string { return "an editing tool" }
func (editTool) Spec() tool.Schema {
	return tool.ObjectSchema(nil, map[string]tool.Property{"path": {Type: "string"}})
}
func (e editTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "edited", Metadata: tool.Metadata{FileChange: &e.change}}, nil
}

// TestFromRegistryMapsFileChange confirms the adapter lifts a tool's
// Metadata.FileChange onto the consumer-side ToolResult.Edits.
func TestFromRegistryMapsFileChange(t *testing.T) {
	change := tool.FileChange{Path: "foo.go", OldText: "old", NewText: "new"}
	reg := tool.NewRegistry(editTool{change: change})
	adapted := loop.FromRegistry(reg)

	tl, ok := adapted.Get("edit")
	if !ok {
		t.Fatal("Get(edit) not found")
	}
	res, err := tl.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []event.FileEdit{{Path: "foo.go", OldText: "old", NewText: "new"}}
	if !reflect.DeepEqual(res.Edits, want) {
		t.Errorf("Edits = %+v, want %+v", res.Edits, want)
	}
}
