package session_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// TestEntryConstructorsAndAccessorsRoundTrip asserts each typed constructor
// produces an entry whose typed accessor recovers the payload, and that
// Model/Usage options land where expected.
func TestEntryConstructorsAndAccessorsRoundTrip(t *testing.T) {
	usage := provider.Usage{InputTokens: 12, OutputTokens: 34}

	want := provider.Message{
		Role: provider.RoleAssistant,
		Content: []provider.ContentBlock{
			provider.ReasoningBlock("thinking..."),
			provider.TextBlock("hello there"),
		},
	}
	msg := session.NewMessageEntry(want,
		session.WithEntryModel("model-a"),
		session.WithEntryUsage(usage),
	)
	if msg.Type != session.EntryMessage {
		t.Fatalf("msg.Type = %q, want %q", msg.Type, session.EntryMessage)
	}
	if msg.Model != "model-a" {
		t.Errorf("msg.Model = %q, want model-a", msg.Model)
	}
	if msg.Usage == nil || !msg.Usage.Equal(usage) {
		t.Errorf("msg.Usage = %+v, want %+v", msg.Usage, usage)
	}
	got, err := msg.Message()
	if err != nil {
		t.Fatalf("Message(): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Message() = %+v, want %+v", got, want)
	}

	blocks := []provider.ContentBlock{provider.ToolResultBlock("c1", "ok", false)}
	tr := session.NewToolRoundEntry(blocks, session.WithEntryModel("model-b"))
	trp, err := tr.ToolRound()
	if err != nil {
		t.Fatalf("ToolRound(): %v", err)
	}
	if !reflect.DeepEqual(trp.Blocks, blocks) {
		t.Errorf("ToolRound() = %+v, want %+v", trp.Blocks, blocks)
	}
	if tr.Model != "model-b" {
		t.Errorf("tr.Model = %q, want model-b", tr.Model)
	}

	comp := session.NewCompactionEntry("summary text", "entry-9")
	cp, err := comp.Compaction()
	if err != nil {
		t.Fatalf("Compaction(): %v", err)
	}
	if cp.Summary != "summary text" || cp.ReplacesThrough != "entry-9" {
		t.Errorf("Compaction() = %+v, unexpected", cp)
	}

	meta := session.NewMetaEntry("/home/user/project")
	if meta.Type != session.EntryMeta {
		t.Fatalf("meta.Type = %q, want %q", meta.Type, session.EntryMeta)
	}
	mp, err := meta.Meta()
	if err != nil {
		t.Fatalf("Meta(): %v", err)
	}
	if mp.Cwd != "/home/user/project" {
		t.Errorf("Meta().Cwd = %q, want %q", mp.Cwd, "/home/user/project")
	}
}

// TestMetaEntryParent asserts the additive lineage fields round-trip through a
// meta entry when [session.WithMetaParent] sets them, and that a meta entry
// built without the option is wire-identical to one built before the fields
// existed — no parent_id/depth keys at all — so an existing journal reads back
// unchanged.
func TestMetaEntryParent(t *testing.T) {
	tests := []struct {
		name      string
		opts      []session.MetaOpt
		wantPar   string
		wantDepth int
		wantKeys  bool // parent_id/depth present in the marshaled payload
	}{
		{
			name:     "root session omits both fields",
			wantKeys: false,
		},
		{
			name:      "child records parent and depth",
			opts:      []session.MetaOpt{session.WithMetaParent("parent-session", 2)},
			wantPar:   "parent-session",
			wantDepth: 2,
			wantKeys:  true,
		},
		{
			name: "depth 0 with a parent still names the parent",
			// Depth 0 is omitempty, so a depth-0 child's wire form carries
			// parent_id only — and still decodes back to depth 0.
			opts:     []session.MetaOpt{session.WithMetaParent("parent-session", 0)},
			wantPar:  "parent-session",
			wantKeys: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := session.NewMetaEntry("/work/proj", tc.opts...)
			mp, err := e.Meta()
			if err != nil {
				t.Fatalf("Meta(): %v", err)
			}
			if mp.Cwd != "/work/proj" {
				t.Errorf("Cwd = %q, want /work/proj", mp.Cwd)
			}
			if mp.ParentID != tc.wantPar {
				t.Errorf("ParentID = %q, want %q", mp.ParentID, tc.wantPar)
			}
			if mp.Depth != tc.wantDepth {
				t.Errorf("Depth = %d, want %d", mp.Depth, tc.wantDepth)
			}

			var raw map[string]any
			if err := json.Unmarshal(e.Payload, &raw); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			_, hasParent := raw["parent_id"]
			if hasParent != tc.wantKeys {
				t.Errorf("parent_id present = %t, want %t: %s", hasParent, tc.wantKeys, e.Payload)
			}
			if !tc.wantKeys {
				if _, ok := raw["depth"]; ok {
					t.Errorf("depth present on a root session's metadata: %s", e.Payload)
				}
			}
		})
	}
}

// TestEntryAccessorWrongTypeErrors asserts calling a typed accessor on a
// mismatched entry type returns a wrapped ErrEntryType.
func TestEntryAccessorWrongTypeErrors(t *testing.T) {
	msg := session.NewMessageEntry(provider.UserText("hi"))

	if _, err := msg.ToolRound(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("ToolRound() on message entry: err = %v, want ErrEntryType", err)
	}
	if _, err := msg.Compaction(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Compaction() on message entry: err = %v, want ErrEntryType", err)
	}
	if _, err := msg.Fork(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Fork() on message entry: err = %v, want ErrEntryType", err)
	}
	if _, err := msg.Meta(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Meta() on message entry: err = %v, want ErrEntryType", err)
	}

	comp := session.NewCompactionEntry("s", "")
	if _, err := comp.Message(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Message() on compaction entry: err = %v, want ErrEntryType", err)
	}
}

// TestEntryJSONShape asserts Entry marshals with the documented field names
// and omits empty optional fields.
func TestEntryJSONShape(t *testing.T) {
	e := session.NewMessageEntry(provider.UserText("hi"))
	e.ID = "id-1"

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, absent := range []string{"parent", "model", "usage"} {
		if _, ok := m[absent]; ok {
			t.Errorf("field %q present in JSON, want omitted: %s", absent, b)
		}
	}
	if m["id"] != "id-1" {
		t.Errorf(`m["id"] = %v, want "id-1"`, m["id"])
	}
	if m["type"] != string(session.EntryMessage) {
		t.Errorf(`m["type"] = %v, want %q`, m["type"], session.EntryMessage)
	}
}
