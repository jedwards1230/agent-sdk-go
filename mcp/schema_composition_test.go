package mcp_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/mcp"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// projectOne drives one hermetic tools/list against a fake server advertising
// a single tool with the given description and raw inputSchema, and returns
// the projected tool. rawSchema is spliced into the response verbatim so a
// test can state the exact JSON Schema a real server sends — including value
// types (a numeric enum, a union type) that no Go schema struct can hold.
// Pass "null" for a tool with no inputSchema at all.
func projectOne(t *testing.T, desc, rawSchema string, opts ...mcp.Option) tool.Tool {
	t.Helper()
	c, server := newTestClient(t, opts...)

	serverErrs := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		if err := handshake(r, server); err != nil {
			serverErrs <- err
			return
		}
		req, err := readLine(r)
		if err != nil {
			serverErrs <- err
			return
		}
		body := `{"tools":[{"name":"probe","description":` + strconv.Quote(desc) +
			`,"inputSchema":` + rawSchema + `}]}`
		serverErrs <- writeLine(server, wireMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(body)})
	}()

	ctx := ctxWithTimeout(t)
	if _, err := c.Initialize(ctx, mcp.ClientInfo{Name: "test"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := mcp.Project(ctx, c, "srv")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	select {
	case err := <-serverErrs:
		if err != nil {
			t.Fatalf("fake server: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for fake server")
	}
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	return tools[0]
}

// specJSON marshals a projected tool's Spec exactly as loop/toolreg.go does
// before handing it to a provider adapter — the bytes that reach the wire.
func specJSON(t *testing.T, tl tool.Tool) string {
	t.Helper()
	b, err := json.Marshal(tl.Spec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return string(b)
}

// TestProjectSchemaWireOutput pins the exact JSON an MCP tool's projected
// schema marshals to. The pre-existing cases were captured from the tree
// BEFORE composition support was added: they are the regression guard that
// the new optional fields stayed additive and changed no existing tool's
// prompt bytes. The composition cases pin the new representable set,
// including the shapes a live ContextForge gateway actually federates.
func TestProjectSchemaWireOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			// Pre-existing: mcp/schema_test.go TestSchemaConversionNested.
			name: "nested objects arrays enum default",
			raw:  `{"type":"object","properties":{"status":{"type":"string","description":"new status","enum":["open","closed"],"default":"open"},"tags":{"type":"array","items":{"type":"string"}},"owner":{"type":"object","properties":{"name":{"type":"string"}}}},"required":["status"]}`,
			want: `{"type":"object","properties":{"owner":{"type":"object","properties":{"name":{"type":"string"}}},"status":{"type":"string","description":"new status","enum":["open","closed"],"default":"open"},"tags":{"type":"array","items":{"type":"string"}}},"required":["status"]}`,
		},
		{
			// Pre-existing: no inputSchema at all.
			name: "absent schema",
			raw:  `null`,
			want: `{"type":"object"}`,
		},
		{
			// Pre-existing: mcp/list_test.go.
			name: "bare object",
			raw:  `{"type":"object"}`,
			want: `{"type":"object"}`,
		},
		{
			// Pre-existing: a schema that is not an object degrades, it does
			// not fail the projection.
			name: "unparseable schema",
			raw:  `"not-a-schema"`,
			want: `{"type":"object"}`,
		},
		{
			// tv-shell open_settings/navigate/key/open_overlay: branches that
			// differ only by which key they require.
			name: "oneOf branches differing by required",
			raw:  `{"type":"object","properties":{"target":{"type":"string"},"index":{"type":"integer"}},"oneOf":[{"required":["target"]},{"required":["index"]}]}`,
			want: `{"type":"object","properties":{"index":{"type":"integer"},"target":{"type":"string"}},"oneOf":[{"required":["target"]},{"required":["index"]}]}`,
		},
		{
			// Home Assistant ha_manage_addon: an anyOf whose branches are a
			// concrete type and a null.
			name: "anyOf nullable union branch",
			raw:  `{"type":"object","properties":{"slug":{"anyOf":[{"type":"string"},{"type":"null"}],"description":"add-on slug"}},"required":["slug"]}`,
			want: `{"type":"object","properties":{"slug":{"description":"add-on slug","anyOf":[{"type":"string"},{"type":"null"}]}},"required":["slug"]}`,
		},
		{
			// n8n get_workflow_best_practices: composition on a nested
			// property, not at the root.
			name: "anyOf on nested property",
			raw:  `{"type":"object","properties":{"filter":{"type":"object","properties":{"scope":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}}}`,
			want: `{"type":"object","properties":{"filter":{"type":"object","properties":{"scope":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}}}`,
		},
		{
			name: "allOf",
			raw:  `{"type":"object","allOf":[{"required":["a"]},{"required":["b"]}],"properties":{"a":{"type":"string"},"b":{"type":"string"}}}`,
			want: `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"allOf":[{"required":["a"]},{"required":["b"]}]}`,
		},
		{
			name: "patternProperties",
			raw:  `{"type":"object","patternProperties":{"^x-":{"type":"string"}}}`,
			want: `{"type":"object","patternProperties":{"^x-":{"type":"string"}}}`,
		},
		{
			// additionalProperties has no representation; the rest survives.
			name: "additionalProperties false",
			raw:  `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"]}`,
			want: `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
		},
		{
			// The collapse bug: a numeric enum used to discard the WHOLE
			// schema. Now it costs exactly that property's enum.
			name: "numeric enum keeps siblings",
			raw:  `{"type":"object","properties":{"n":{"type":"integer","enum":[1,2]},"s":{"type":"string","description":"keep me"}},"required":["s"]}`,
			want: `{"type":"object","properties":{"n":{"type":"integer"},"s":{"type":"string","description":"keep me"}},"required":["s"]}`,
		},
		{
			// Same trap via a union type; it also used to collapse the schema.
			name: "union type keeps siblings",
			raw:  `{"type":"object","properties":{"s":{"type":["string","null"],"description":"keep me"}},"required":["s"]}`,
			want: `{"type":"object","properties":{"s":{"type":"string","description":"keep me"}},"required":["s"]}`,
		},
		{
			name: "nested object required",
			raw:  `{"type":"object","properties":{"owner":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`,
			want: `{"type":"object","properties":{"owner":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := specJSON(t, projectOne(t, "desc", tt.raw)); got != tt.want {
				t.Errorf("spec JSON =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestProjectDescriptionUnchangedWithoutComposition is the non-negotiable
// one: a schema that projects cleanly must leave the server's description
// alone byte for byte. No marker, no trailing newline, nothing — this is the
// overwhelming majority of federated tools and their prompt cost must not
// move at all.
func TestProjectDescriptionUnchangedWithoutComposition(t *testing.T) {
	const desc = "update a record"
	tl := projectOne(t, desc,
		`{"type":"object","properties":{"status":{"type":"string","enum":["open","closed"]}},"required":["status"]}`)
	if got := tl.Description(); got != desc {
		t.Errorf("Description() = %q, want %q exactly", got, desc)
	}
}

// TestProjectDescriptionNamesComposition covers the restatement: a model
// follows prose more reliably than a nested oneOf, so the description must
// name the keyword and the keys each alternative requires.
func TestProjectDescriptionNamesComposition(t *testing.T) {
	const desc = "act on a UI element"
	tl := projectOne(t, desc,
		`{"type":"object","properties":{"target":{"type":"string"},"index":{"type":"integer"}},"oneOf":[{"required":["target"]},{"required":["index"]}]}`)
	got := tl.Description()
	if !strings.HasPrefix(got, desc) {
		t.Fatalf("Description() = %q, want it to start with %q", got, desc)
	}
	for _, want := range []string{"oneOf", "exactly one of", `requires "target"`, `requires "index"`} {
		if !strings.Contains(got, want) {
			t.Errorf("Description() = %q, want it to contain %q", got, want)
		}
	}
}

// TestProjectDescriptionNamesNestedComposition covers the shorter clause used
// when the composition sits on a property rather than the root: it names
// where, without restating every branch.
func TestProjectDescriptionNamesNestedComposition(t *testing.T) {
	tl := projectOne(t, "d",
		`{"type":"object","properties":{"slug":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`)
	got := tl.Description()
	for _, want := range []string{"anyOf", "at least one of", "properties.slug"} {
		if !strings.Contains(got, want) {
			t.Errorf("Description() = %q, want it to contain %q", got, want)
		}
	}
}

// TestProjectDescriptionWarnsAboutDropped covers the visible-degradation
// half: a constraint the projection cannot express must reach the model as
// prose naming the keyword, and must say the server may still reject the
// call. Paths deliberately do NOT appear here — they are unbounded and
// server-controlled, so they ride the log instead (see
// TestProjectLogsDroppedConstraintsOncePerTool).
func TestProjectDescriptionWarnsAboutDropped(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "keywords named",
			raw:  `{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string","pattern":"^[a-z]+$"}}}`,
			want: []string{"additionalProperties", "pattern", "may still be rejected"},
		},
		{
			// The degenerate cases are reported too, not just per-keyword
			// drops: a value type tool.Property is too narrow for costs a
			// constraint, so it has to be said out loud.
			name: "non-string enum",
			raw:  `{"type":"object","properties":{"n":{"type":"integer","enum":[1,2]}}}`,
			want: []string{"enum"},
		},
		{
			// A narrowed union must not read as "the type is unconstrained",
			// which is what a bare "type" in this list would imply.
			name: "union type says union",
			raw:  `{"type":"object","properties":{"s":{"type":["string","null"]}}}`,
			want: []string{"type union"},
		},
		{
			// Root enum is an assertion, not an annotation. tool.Schema has
			// no field for it, so discarding it silently would be the one
			// hole in the no-silent-drops invariant.
			name: "root enum",
			raw:  `{"type":"object","enum":["a","b"]}`,
			want: []string{"enum", "may still be rejected"},
		},
		{
			// An empty enum or composition list is a schema nothing validates
			// against; emitting nothing in its place widens.
			name: "empty enum",
			raw:  `{"type":"object","properties":{"a":{"enum":[]}}}`,
			want: []string{"enum"},
		},
		{
			name: "empty composition list",
			raw:  `{"type":"object","anyOf":[]}`,
			want: []string{"anyOf"},
		},
		{
			name: "schema that could not be read",
			raw:  `"not-a-schema"`,
			want: []string{"could not read", "may still be rejected"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectOne(t, "d", tt.raw).Description()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("Description() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestProjectVacuousCompositionBranchIsDropped covers the one place this
// projection could emit a schema WIDER than the server's. A branch whose
// every keyword was unrepresentable projects to {} — the schema matching
// everything — so a union carrying one is vacuously true and must be omitted
// entirely. This is the commonest federated shape there is: pydantic's
// Optional[Model] via FastMCP.
func TestProjectVacuousCompositionBranchIsDropped(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantSpec string
		// wantNoNote asserts the composition prose is suppressed too: a
		// keyword that did not survive must not be restated as "(1) an
		// alternative shape; (2) an alternative shape".
		wantNoNote bool
	}{
		{
			name:       "anyOf with unrepresentable branch (pydantic Optional)",
			raw:        `{"type":"object","properties":{"cfg":{"anyOf":[{"$ref":"#/$defs/Cfg"},{"type":"null"}]}}}`,
			wantSpec:   `{"type":"object","properties":{"cfg":{}}}`,
			wantNoNote: true,
		},
		{
			name:       "oneOf with every branch unrepresentable",
			raw:        `{"type":"object","properties":{"cfg":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}}}`,
			wantSpec:   `{"type":"object","properties":{"cfg":{}}}`,
			wantNoNote: true,
		},
		{
			// allOf is an intersection, not a union: dropping only the
			// unrepresentable member still applies the others. Dropping the
			// whole keyword here would LOSE the required:["a"] constraint.
			name:     "allOf keeps its representable members",
			raw:      `{"type":"object","allOf":[{"$ref":"#/$defs/A"},{"required":["a"]}],"properties":{"a":{"type":"string"}}}`,
			wantSpec: `{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"required":["a"]}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := projectOne(t, "d", tt.raw)
			if got := specJSON(t, tl); got != tt.wantSpec {
				t.Errorf("spec JSON =\n%s\nwant\n%s", got, tt.wantSpec)
			}
			desc := tl.Description()
			if !strings.Contains(desc, "may still be rejected") {
				t.Errorf("Description() = %q, want the dropped-constraint warning", desc)
			}
			if tt.wantNoNote && strings.Contains(desc, "alternative shapes") {
				t.Errorf("Description() = %q, want no composition prose for a keyword that was dropped", desc)
			}
		})
	}
}

// TestProjectDescriptionIsBoundedOnWideSchema is the prompt-cost gate. The
// appended prose must not grow with the server's schema: a projected tool's
// description rides EVERY request for the life of the session, so an
// undeduplicated per-occurrence list makes prompt cost server-controlled. The
// measured pre-fix case was a 40-property schema whose 4,072-byte schema
// produced a 5,328-byte description from 162 drops.
func TestProjectDescriptionIsBoundedOnWideSchema(t *testing.T) {
	const base = "Do a thing."
	var props []string
	for i := range 40 {
		props = append(props, fmt.Sprintf(
			`"field%02d":{"type":"string","format":"email","minLength":1,"maxLength":64,"pattern":"^.+$","anyOf":[{"type":"string"},{"type":"null"}]}`, i))
	}
	raw := `{"type":"object","additionalProperties":false,"properties":{` + strings.Join(props, ",") + `}}`

	desc := projectOne(t, base, raw).Description()
	appended := len(desc) - len(base)
	const budget = 1500
	if appended > budget {
		t.Errorf("appended description = %d bytes (raw schema %d bytes), want <= %d\n%s",
			appended, len(raw), budget, desc)
	}
	// Bounded but still useful: it must name the frequent offenders with
	// counts, and still tell the model why.
	for _, want := range []string{"format", "pattern", "×40", "may still be rejected"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Description() = %q, want it to contain %q", desc, want)
		}
	}
	// A long tail must be summarized, not enumerated.
	if !strings.Contains(desc, "more") {
		t.Errorf("Description() = %q, want a capped list summarizing the remainder", desc)
	}
	t.Logf("raw schema %d bytes -> appended description %d bytes", len(raw), appended)
}

// TestProjectDroppedKeywordDetectionIsDenyByDefault covers the detector's
// direction: it is not an allowlist of known constraint keywords, so a
// keyword nobody has heard of is still surfaced rather than quietly widening
// the schema. Inert annotations stay quiet.
func TestProjectDroppedKeywordDetectionIsDenyByDefault(t *testing.T) {
	t.Run("unknown keyword reported", func(t *testing.T) {
		tl := projectOne(t, "d", `{"type":"object","x-madeUpKeyword":{"a":1}}`)
		if !strings.Contains(tl.Description(), "x-madeUpKeyword") {
			t.Errorf("Description() = %q, want it to report x-madeUpKeyword", tl.Description())
		}
	})
	t.Run("inert annotations stay quiet", func(t *testing.T) {
		const desc = "d"
		tl := projectOne(t, desc,
			`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"x","title":"T","examples":[{}],"deprecated":false,"readOnly":false,"writeOnly":false,"$comment":"c","type":"object"}`)
		if got := tl.Description(); got != desc {
			t.Errorf("Description() = %q, want %q exactly", got, desc)
		}
	})
	t.Run("definition blocks stay quiet", func(t *testing.T) {
		// $defs constrains nothing by itself — only a $ref into it does, and
		// that is reported at its own path. Every pydantic schema carries
		// one, so reporting the block would be a guaranteed false positive
		// on the commonest server family.
		const desc = "d"
		tl := projectOne(t, desc,
			`{"type":"object","$defs":{"Cfg":{"type":"object"}},"definitions":{"Old":{"type":"object"}},"$vocabulary":{"x":true}}`)
		if got := tl.Description(); got != desc {
			t.Errorf("Description() = %q, want %q exactly", got, desc)
		}
	})
	t.Run("single-member type array loses nothing", func(t *testing.T) {
		// ["string"] carries exactly as much as "string" does, so reporting
		// it would be noise.
		const desc = "d"
		tl := projectOne(t, desc, `{"type":"object","properties":{"s":{"type":["string"]}}}`)
		if got := tl.Description(); got != desc {
			t.Errorf("Description() = %q, want %q exactly", got, desc)
		}
		if got := specJSON(t, tl); got != `{"type":"object","properties":{"s":{"type":"string"}}}` {
			t.Errorf("spec JSON = %s", got)
		}
	})
}

// TestProjectLogsDroppedConstraintsOncePerTool covers the operator-facing
// half. A tool that lost constraints logs exactly one Warn carrying the
// PRECISE per-occurrence list with full paths — the detail the description
// deliberately drops, because in a log it costs nothing and it is what an
// operator needs to find the keyword in the server's own schema. A tool that
// projected cleanly logs nothing, so the warnings stay a short actionable
// list instead of a wall.
func TestProjectLogsDroppedConstraintsOncePerTool(t *testing.T) {
	t.Run("degraded tool warns once with full paths", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		projectOne(t, "d",
			`{"type":"object","additionalProperties":false,"properties":{"name":{"type":"string","pattern":"^[a-z]+$"}}}`,
			mcp.WithLogger(log))

		out := buf.String()
		if n := strings.Count(out, "level=WARN"); n != 1 {
			t.Fatalf("warn count = %d, want 1; log:\n%s", n, out)
		}
		for _, want := range []string{
			"server=srv", "tool=probe",
			"additionalProperties at the root", "pattern at properties.name",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("log = %q, want it to contain %q", out, want)
			}
		}
	})
	t.Run("unreadable schema logs the decode error", func(t *testing.T) {
		// The model-facing description stays generic; an operator needs to
		// know whether the schema was truncated, invalid UTF-8, or nested too
		// deeply, and that only makes sense in a log.
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		tl := projectOne(t, "d", `"not-a-schema"`, mcp.WithLogger(log))

		if out := buf.String(); !strings.Contains(out, "decode inputSchema") {
			t.Errorf("log = %q, want it to carry the decode error", out)
		}
		if desc := tl.Description(); strings.Contains(desc, "decode inputSchema") {
			t.Errorf("Description() = %q, want no raw parser error spliced into the prompt", desc)
		}
	})
	t.Run("clean tool is silent", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
		projectOne(t, "d", `{"type":"object","properties":{"a":{"type":"string"}}}`, mcp.WithLogger(log))
		if out := buf.String(); out != "" {
			t.Errorf("log = %q, want empty for a cleanly projected schema", out)
		}
	})
}
