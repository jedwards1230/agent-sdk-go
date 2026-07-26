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
	if mp.Role != "" {
		t.Errorf("Meta().Role = %q, want empty (unclassified by default)", mp.Role)
	}

	ckpt := session.NewCheckpointEntry("before-refactor")
	if ckpt.Type != session.EntryCheckpoint {
		t.Fatalf("ckpt.Type = %q, want %q", ckpt.Type, session.EntryCheckpoint)
	}
	ckp, err := ckpt.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint(): %v", err)
	}
	if ckp.Label != "before-refactor" {
		t.Errorf("Checkpoint().Label = %q, want %q", ckp.Label, "before-refactor")
	}
}

// TestMetaEntryRole asserts the additive MetaPayload.Role round-trips through
// the entry payload when set via WithMetaRole, and is omitted from the wire
// when unset — so an existing journal's meta entry is byte-identical to one
// written before the field existed.
func TestMetaEntryRole(t *testing.T) {
	cases := []struct {
		name     string
		opts     []session.MetaOpt
		wantRole string
		wantWire bool // "role" expected present in the marshalled payload
	}{
		{name: "unset", wantRole: "", wantWire: false},
		{name: "empty string", opts: []session.MetaOpt{session.WithMetaRole("")}, wantRole: "", wantWire: false},
		{name: "monitor", opts: []session.MetaOpt{session.WithMetaRole("monitor")}, wantRole: "monitor", wantWire: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := session.NewMetaEntry("/home/user/project", tc.opts...)
			mp, err := e.Meta()
			if err != nil {
				t.Fatalf("Meta(): %v", err)
			}
			if mp.Role != tc.wantRole {
				t.Errorf("Meta().Role = %q, want %q", mp.Role, tc.wantRole)
			}
			if mp.Cwd != "/home/user/project" {
				t.Errorf("Meta().Cwd = %q, want the cwd unchanged", mp.Cwd)
			}
			var raw map[string]any
			if err := json.Unmarshal(e.Payload, &raw); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if _, ok := raw["role"]; ok != tc.wantWire {
				t.Errorf(`payload "role" present = %v, want %v: %s`, ok, tc.wantWire, e.Payload)
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
	if _, err := msg.Checkpoint(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Checkpoint() on message entry: err = %v, want ErrEntryType", err)
	}

	comp := session.NewCompactionEntry("s", "")
	if _, err := comp.Message(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Message() on compaction entry: err = %v, want ErrEntryType", err)
	}

	ckpt := session.NewCheckpointEntry("mark")
	if _, err := ckpt.Message(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Message() on checkpoint entry: err = %v, want ErrEntryType", err)
	}
	if _, err := ckpt.Meta(); !errors.Is(err, session.ErrEntryType) {
		t.Errorf("Meta() on checkpoint entry: err = %v, want ErrEntryType", err)
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
