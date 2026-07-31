package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// inertKeywords are annotations that constrain nothing. Losing them changes
// which inputs a server accepts not at all, so they are not reported as
// dropped either.
var inertKeywords = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "title": true,
	"examples": true, "deprecated": true, "readOnly": true, "writeOnly": true,
}

// droppedConstraint is one JSON Schema keyword the projection could not
// represent, plus where in the schema it appeared. Path is "" for the root
// and otherwise a dotted trail through the raw schema
// ("properties.name.items"), so an operator reading a log line can find the
// keyword in the server's own schema without translating our field names
// back to theirs. Whole marks the degenerate case where nothing at all could
// be read and Keyword is a whole sentence rather than a keyword name.
type droppedConstraint struct {
	Keyword string
	Path    string
	Whole   bool
}

func (d droppedConstraint) String() string {
	switch {
	case d.Whole:
		return d.Keyword
	case d.Path == "":
		return d.Keyword + " at the root"
	default:
		return d.Keyword + " at " + d.Path
	}
}

// compositionNote records one oneOf/anyOf/allOf the projection did represent,
// so [projection.describe] can restate it as prose. Models follow a sentence
// more reliably than a nested composition keyword, and the restatement costs
// a line only on the schemas that actually use composition.
type compositionNote struct {
	Path     string   // "" for the root, else a dotted trail
	Keyword  string   // "oneOf", "anyOf", or "allOf"
	Branches []string // one human phrase per branch, in schema order
}

// projection is the full result of projecting one server-supplied JSON
// Schema: what we could represent, what we deliberately restate in prose, and
// what we had to drop.
type projection struct {
	schema      tool.Schema
	composition []compositionNote
	dropped     []droppedConstraint
}

// degraded reports whether the projection lost anything the server will still
// enforce. It gates both the description warning and the log line — a tool
// whose schema projected cleanly must produce neither.
func (p projection) degraded() bool { return len(p.dropped) > 0 }

// droppedKeywords renders the dropped constraints for a structured log attr.
func (p projection) droppedKeywords() []string {
	out := make([]string, 0, len(p.dropped))
	for _, d := range p.dropped {
		out = append(out, d.String())
	}
	return out
}

// projectSchema converts an MCP tool's raw JSON Schema inputSchema into a
// [tool.Schema] and reports what that conversion cost.
//
// It is a best-effort projection, not a JSON Schema implementation, and it is
// deliberately total: every input produces a usable schema. The raw schema is
// decoded once into map[string]any rather than into narrow Go structs, so no
// value type a server legally uses can fail the decode and collapse the whole
// schema — a `{"type":"integer","enum":[1,2]}` property costs that one
// property's enum, never the sibling properties, descriptions, and required
// list. A missing schema degrades to an empty object schema (a tool that
// takes no meaningful input still needs SOME valid [tool.Schema]); a schema
// that is not a JSON object at all degrades the same way and is reported.
//
// Constructs [tool.Schema] cannot express are dropped rather than rejected,
// but never silently: every such keyword is collected with its path so
// [Project] can both warn the model in the tool's description and log it for
// an operator. The detector is deny-by-default — anything that is neither
// representable nor inert metadata is reported — so a JSON Schema keyword
// invented after this code was written is still surfaced instead of quietly
// widening the schema the model sees.
//
// The root node's own `description`, `default`, and `items` are ignored:
// [tool.Schema] has no field for them and, on a tool input object, none of
// them constrains an argument (the tool's own description carries the prose).
func projectSchema(raw json.RawMessage) projection {
	if len(raw) == 0 {
		return projection{schema: tool.Schema{Type: "object"}}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return projection{
			schema: tool.Schema{Type: "object"},
			dropped: []droppedConstraint{{
				Keyword: "the server sent a schema that is not a JSON object, so none of it could be read",
				Whole:   true,
			}},
		}
	}

	var p projector
	node := p.node("", root)
	s := tool.Schema{
		Type:              node.Type,
		Properties:        node.Properties,
		Required:          node.Required,
		OneOf:             node.OneOf,
		AnyOf:             node.AnyOf,
		AllOf:             node.AllOf,
		PatternProperties: node.PatternProperties,
	}
	if s.Type == "" {
		s.Type = "object"
	}
	return projection{schema: s, composition: p.composition, dropped: p.dropped}
}

// projector walks a decoded JSON Schema, accumulating the composition notes
// and dropped constraints found anywhere in the tree. It is a value type used
// once per projection; nothing shares one.
type projector struct {
	composition []compositionNote
	dropped     []droppedConstraint
}

func (p *projector) drop(path, keyword string) {
	p.dropped = append(p.dropped, droppedConstraint{Keyword: keyword, Path: path})
}

// childPath extends a schema path by one segment. The root's own path is "".
func childPath(parent, seg string) string {
	if parent == "" {
		return seg
	}
	return parent + "." + seg
}

// node projects one JSON Schema node. The switch's explicit cases ARE the
// representable keyword set — there is no second allowlist to drift from it,
// so every other non-inert keyword falls through to the default and is
// reported. Keys are visited in sorted order so the dropped-constraint report
// is deterministic regardless of map iteration.
func (p *projector) node(path string, raw map[string]any) tool.Property {
	var out tool.Property
	for _, key := range sortedKeys(raw) {
		v := raw[key]
		switch key {
		case "type":
			out.Type = p.typeOf(path, v)
		case "description":
			if s, ok := v.(string); ok {
				out.Description = s
			}
		case "default":
			out.Default = v
		case "enum":
			out.Enum = p.enumOf(path, v)
		case "required":
			out.Required = p.stringsOf(path, "required", v)
		case "items":
			if item, ok := p.subschema(childPath(path, "items"), v); ok {
				out.Items = &item
			}
		case "properties":
			out.Properties = p.subschemaMap(path, key, v)
		case "patternProperties":
			out.PatternProperties = p.subschemaMap(path, key, v)
		case "oneOf", "anyOf", "allOf":
			branches := p.branches(path, key, v)
			switch key {
			case "oneOf":
				out.OneOf = branches
			case "anyOf":
				out.AnyOf = branches
			case "allOf":
				out.AllOf = branches
			}
		default:
			if !inertKeywords[key] {
				p.drop(path, key)
			}
		}
	}
	return out
}

// typeOf projects the "type" keyword. JSON Schema allows a union
// (["string","null"]); [tool.Property.Type] is a single string, so a union
// keeps its first non-"null" member — narrower than the server, never wider —
// and is reported as dropped.
func (p *projector) typeOf(path string, v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	members, ok := v.([]any)
	if !ok {
		p.drop(path, "type")
		return ""
	}
	p.drop(path, "type")
	for _, m := range members {
		if s, ok := m.(string); ok && s != "null" {
			return s
		}
	}
	return ""
}

// enumOf projects the "enum" keyword. [tool.Property.Enum] is []string, so an
// enum with any non-string member is dropped for that property alone — the
// bug this replaces let one numeric enum discard the entire schema.
func (p *projector) enumOf(path string, v any) []string {
	members, ok := v.([]any)
	if !ok {
		p.drop(path, "enum")
		return nil
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		s, ok := m.(string)
		if !ok {
			p.drop(path, "enum")
			return nil
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stringsOf projects a keyword whose value must be an array of strings.
func (p *projector) stringsOf(path, keyword string, v any) []string {
	members, ok := v.([]any)
	if !ok {
		p.drop(path, keyword)
		return nil
	}
	out := make([]string, 0, len(members))
	for _, m := range members {
		s, ok := m.(string)
		if !ok {
			p.drop(path, keyword)
			return nil
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// subschema projects a nested schema node, reporting the JSON Schema boolean
// form (`true`/`false` in place of an object) as a drop — it is legal but has
// no [tool.Property] representation.
func (p *projector) subschema(path string, v any) (tool.Property, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		p.drop(path, "schema")
		return tool.Property{}, false
	}
	return p.node(path, m), true
}

// subschemaMap projects a keyword at parent whose value is an object of
// subschemas ("properties", "patternProperties").
func (p *projector) subschemaMap(parent, keyword string, v any) map[string]tool.Property {
	raw, ok := v.(map[string]any)
	if !ok {
		p.drop(parent, keyword)
		return nil
	}
	path := childPath(parent, keyword)
	out := make(map[string]tool.Property, len(raw))
	for _, name := range sortedKeys(raw) {
		if sub, ok := p.subschema(childPath(path, name), raw[name]); ok {
			out[name] = sub
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// branches projects a oneOf/anyOf/allOf list and records a [compositionNote]
// describing it, so the projection can restate the composition as prose the
// model will actually follow.
func (p *projector) branches(path, keyword string, v any) []tool.Property {
	raw, ok := v.([]any)
	if !ok {
		p.drop(path, keyword)
		return nil
	}
	out := make([]tool.Property, 0, len(raw))
	phrases := make([]string, 0, len(raw))
	for i, b := range raw {
		branch, ok := p.subschema(fmt.Sprintf("%s[%d]", childPath(path, keyword), i), b)
		if !ok {
			continue
		}
		out = append(out, branch)
		phrases = append(phrases, branchPhrase(branch))
	}
	if len(out) == 0 {
		return nil
	}
	p.composition = append(p.composition, compositionNote{Path: path, Keyword: keyword, Branches: phrases})
	return out
}

// branchPhrase describes one composition branch in the fewest words that
// still tell a model what to do: the keys the branch requires, else its type,
// else nothing distinguishing.
func branchPhrase(b tool.Property) string {
	if len(b.Required) > 0 {
		quoted := make([]string, 0, len(b.Required))
		for _, r := range b.Required {
			quoted = append(quoted, strconv.Quote(r))
		}
		return "requires " + strings.Join(quoted, ", ")
	}
	if b.Type != "" {
		return "a " + b.Type
	}
	return "an alternative shape"
}

// quantifier renders a composition keyword as the phrase a model reads.
func quantifier(keyword string) string {
	switch keyword {
	case "oneOf":
		return "exactly one of"
	case "anyOf":
		return "at least one of"
	default:
		return "all of"
	}
}

// describe returns the model-facing description for a projected tool: base
// (the server's own description) plus, only when the projection has something
// to say, appended "Schema note:" lines.
//
// A schema with no composition and nothing dropped returns base byte-for-byte
// — no marker, no trailing newline. That is the overwhelming majority of
// federated tools, and their prompt cost must not change at all.
func (p projection) describe(base string) string {
	if len(p.composition) == 0 && !p.degraded() {
		return base
	}
	var notes []string
	for _, c := range p.composition {
		subject := "this tool's input"
		if c.Path != "" {
			subject = "the value at " + c.Path
		}
		note := fmt.Sprintf("Schema note: %s must satisfy %s %d alternative shapes (%s)",
			subject, quantifier(c.Keyword), len(c.Branches), c.Keyword)
		if c.Path == "" {
			parts := make([]string, 0, len(c.Branches))
			for i, phrase := range c.Branches {
				parts = append(parts, fmt.Sprintf("(%d) %s", i+1, phrase))
			}
			note += ": " + strings.Join(parts, "; ")
		}
		notes = append(notes, note+".")
	}
	if p.degraded() {
		notes = append(notes, fmt.Sprintf(
			"Schema note: the server's schema for this tool also constrains input in ways this schema cannot express (%s). Arguments that satisfy the schema above may still be rejected.",
			strings.Join(p.droppedKeywords(), "; ")))
	}
	appended := strings.Join(notes, "\n")
	if base == "" {
		return appended
	}
	return base + "\n\n" + appended
}

// sortedKeys returns m's keys in ascending order, so every walk over a
// decoded schema — and therefore every dropped-constraint report — is
// deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
