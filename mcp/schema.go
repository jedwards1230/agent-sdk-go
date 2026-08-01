package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jedwards1230/agent-sdk-go/tool"
)

// inertKeywords are annotations and definition blocks that constrain nothing
// on their own. Losing them changes which inputs a server accepts not at all,
// so they are not reported as dropped. `$defs`/`definitions` are here because
// every pydantic-derived schema carries one and only the `$ref` *into* it
// constrains anything — and that `$ref` is already reported at its own path,
// so reporting the block too would be a guaranteed false positive on the
// commonest server family.
//
// `readOnly`/`writeOnly` are a deliberate judgement call rather than a clean
// case: JSON Schema defines both as pure annotations, but an OpenAPI-derived
// server may use `readOnly: true` on a request field to mean "do not send
// this". Treating them as inert accepts that a server using them in the
// OpenAPI sense gets no warning; the alternative warns on every schema that
// uses them in the standard sense, which is the far more common one.
var inertKeywords = map[string]bool{
	"$schema": true, "$id": true, "$comment": true, "title": true,
	"examples": true, "deprecated": true, "readOnly": true, "writeOnly": true,
	"$defs": true, "definitions": true, "$vocabulary": true,
}

// listCap bounds every server-controlled list that reaches the model's
// context: dropped keywords, composition branches, and the paths of nested
// compositions. Without it a tool's description grows with the size of the
// server's schema, on every request, for the life of the session — a 40-
// property schema with four unrepresentable keywords each produced 161 drops
// and a description larger than the schema it annotated. The precise,
// uncapped list still goes to the logger, where it costs nothing and is what
// an operator actually needs.
const listCap = 6

// keywordRuneCap clips one server-supplied token (a keyword name, a schema
// path, a required key) before it reaches the model. Combined with listCap it
// makes the appended description bounded by construction rather than by the
// server's good behavior.
const keywordRuneCap = 40

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
	// parseErr is the decode failure behind a whole-schema drop, kept for the
	// log attr only. The model-facing description stays generic: splicing a
	// raw parser error into a tool description puts server-controlled text in
	// the prompt and tells the model nothing it can act on, while an operator
	// needs to know whether the schema was truncated, invalid UTF-8, or
	// nested past encoding/json's depth limit.
	parseErr error
}

// degraded reports whether the projection lost anything the server will still
// enforce. It gates both the description warning and the log line — a tool
// whose schema projected cleanly must produce neither.
func (p projection) degraded() bool { return len(p.dropped) > 0 }

// droppedKeywords renders the dropped constraints, one entry per occurrence
// with its full path, for the structured log attr. This is the precise list;
// [projection.describe] emits a deduplicated and capped summary instead.
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
// Two rules keep "never widens" true where a naive projection would not:
//   - A composition branch that projects to nothing would marshal as `{}`,
//     the schema that matches everything. See [projector.branches].
//   - The root node's own `description`, `default`, and `items` are discarded
//     without a report, because none of them asserts anything about an
//     argument of a tool-input object. Root `enum` DOES assert, so it is
//     reported like any other drop even though it is discarded the same way.
func projectSchema(raw json.RawMessage) projection {
	if len(raw) == 0 {
		return projection{schema: tool.Schema{Type: "object"}}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return projection{
			schema: tool.Schema{Type: "object"},
			dropped: []droppedConstraint{{
				Keyword: "the server sent a schema this client could not read, so none of it could be applied",
				Whole:   true,
			}},
			parseErr: fmt.Errorf("decode inputSchema: %w", err),
		}
	}

	var p projector
	node := p.node("", root)
	if node.Enum != nil {
		p.drop("", "enum")
	}
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
			// An empty or malformed required list constrains nothing, so
			// unlike enum there is nothing to report when it does not
			// project — the absence of a requirement is the default.
			if members, ok := v.([]any); ok {
				keys := make([]string, 0, len(members))
				for _, m := range members {
					s, ok := m.(string)
					if !ok {
						keys = nil
						break
					}
					keys = append(keys, s)
				}
				if len(keys) > 0 {
					out.Required = keys
				}
			}
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
// keeps its first non-"null" member — narrower than the server, never wider.
// It reports "type union" rather than "type", because "type at properties.s"
// in a list of things the schema "cannot express" reads as if the value were
// left unconstrained, which is the opposite of what happened. A single-member
// array loses nothing and is not reported at all.
func (p *projector) typeOf(path string, v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	members, ok := v.([]any)
	if !ok {
		p.drop(path, "type")
		return ""
	}
	if len(members) == 1 {
		if s, ok := members[0].(string); ok && s != "null" {
			return s
		}
	}
	p.drop(path, "type union")
	for _, m := range members {
		if s, ok := m.(string); ok && s != "null" {
			return s
		}
	}
	return ""
}

// enumOf projects the "enum" keyword. [tool.Property.Enum] is []string, so an
// enum with any non-string member is dropped for that property alone — the
// bug this replaces let one numeric enum discard the entire schema. An empty
// enum is a schema nothing validates against, so silently emitting no enum in
// its place widens; it is reported like any other loss.
func (p *projector) enumOf(path string, v any) []string {
	members, ok := v.([]any)
	if !ok || len(members) == 0 {
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
//
// A branch whose every keyword was unrepresentable projects to the zero
// [tool.Property], which marshals as `{}` — the schema that matches
// EVERYTHING. That is the one place this projection could emit something
// wider than the server rather than narrower, and it is the commonest real
// shape there is: pydantic's Optional[Model] federates as
// `{"anyOf":[{"$ref":"#/$defs/Cfg"},{"type":"null"}]}`, which would become
// `anyOf:[{}, {"type":"null"}]` — vacuously true. For oneOf it is worse:
// `[{},{}]` matches every input twice, so under strict validation nothing
// validates at all.
//
// So a union (oneOf/anyOf) with any such branch is dropped whole: omitting
// the keyword is strictly better than emitting a vacuous one. allOf is an
// intersection, not a union — dropping only the unrepresentable member still
// applies every other member's constraints, which is strictly better than
// dropping them all — so it keeps what projected and reports the loss.
// Either way the keyword is reported, and no composition prose is emitted for
// a keyword that did not survive.
func (p *projector) branches(path, keyword string, v any) []tool.Property {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		// An empty composition list validates nothing; emitting no keyword in
		// its place widens, so it is a reportable loss like any other.
		p.drop(path, keyword)
		return nil
	}
	out := make([]tool.Property, 0, len(raw))
	phrases := make([]string, 0, len(raw))
	lost := false
	for i, b := range raw {
		branch, ok := p.subschema(fmt.Sprintf("%s[%d]", childPath(path, keyword), i), b)
		if !ok || vacuous(branch) {
			lost = true
			continue
		}
		out = append(out, branch)
		phrases = append(phrases, branchPhrase(branch))
	}
	if lost {
		p.drop(path, keyword)
		if keyword != "allOf" {
			return nil
		}
	}
	if len(out) == 0 {
		return nil
	}
	p.composition = append(p.composition, compositionNote{Path: path, Keyword: keyword, Branches: phrases})
	return out
}

// vacuous reports whether a projected branch asserts nothing about its input
// and would therefore marshal as a schema matching everything. Description
// and Default are excluded deliberately: they are annotations, so a branch
// carrying only those is still vacuous.
func vacuous(b tool.Property) bool {
	return b.Type == "" && b.Enum == nil && b.Items == nil &&
		len(b.Properties) == 0 && len(b.Required) == 0 &&
		len(b.OneOf) == 0 && len(b.AnyOf) == 0 && len(b.AllOf) == 0 &&
		len(b.PatternProperties) == 0
}

// branchPhrase describes one composition branch in the fewest words that
// still tell a model what to do: the keys the branch requires, else its type.
func branchPhrase(b tool.Property) string {
	if len(b.Required) > 0 {
		quoted := make([]string, 0, len(b.Required))
		for _, r := range b.Required {
			quoted = append(quoted, strconv.Quote(clip(r)))
		}
		return "requires " + capList(quoted)
	}
	if b.Type != "" {
		return "a " + clip(b.Type)
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
//
// Everything appended is bounded by [listCap] and [keywordRuneCap], so a
// tool's prompt cost cannot be made to grow with the size (or the malice) of
// the server's schema. The prose only has to tell the model that its
// arguments may be rejected and roughly why; the exhaustive per-occurrence
// list with full paths goes to the logger instead.
func (p projection) describe(base string) string {
	if len(p.composition) == 0 && !p.degraded() {
		return base
	}
	notes := p.compositionNotes()
	if p.degraded() {
		notes = append(notes, "Schema note: the server's schema for this tool also constrains input in ways this schema cannot express ("+
			p.droppedSummary()+"). Arguments that satisfy the schema above may still be rejected.")
	}
	appended := strings.Join(notes, "\n")
	if base == "" {
		return appended
	}
	return base + "\n\n" + appended
}

// compositionNotes renders the represented compositions as prose. Root
// composition is spelled out branch by branch — that is the actionable part,
// and there are at most three such keywords. Nested compositions are grouped
// by keyword and named by path, because a wide schema can carry one per
// property (pydantic emits an anyOf for every Optional field) and restating
// each one's branches would reproduce the schema in prose.
func (p projection) compositionNotes() []string {
	var notes []string
	for _, c := range p.composition {
		if c.Path != "" {
			continue
		}
		parts := make([]string, 0, len(c.Branches))
		for i, phrase := range c.Branches {
			parts = append(parts, fmt.Sprintf("(%d) %s", i+1, phrase))
		}
		notes = append(notes, fmt.Sprintf(
			"Schema note: this tool's input must satisfy %s %d alternative shapes (%s): %s.",
			quantifier(c.Keyword), len(c.Branches), c.Keyword, capList(parts)))
	}

	byKeyword := make(map[string][]string)
	var keywords []string
	for _, c := range p.composition {
		if c.Path == "" {
			continue
		}
		if _, seen := byKeyword[c.Keyword]; !seen {
			keywords = append(keywords, c.Keyword)
		}
		byKeyword[c.Keyword] = append(byKeyword[c.Keyword], clip(c.Path))
	}
	slices.Sort(keywords)
	for _, kw := range keywords {
		paths := byKeyword[kw]
		subject, verb := "the value at "+capList(paths), "must satisfy"
		if len(paths) > 1 {
			subject, verb = "the values at "+capList(paths), "must each satisfy"
		}
		notes = append(notes, fmt.Sprintf(
			"Schema note: %s %s %s several alternative shapes (%s).",
			subject, verb, quantifier(kw), kw))
	}
	return notes
}

// droppedSummary collapses the drop list into the bounded form that reaches
// the model: one entry per distinct keyword with an occurrence count, most
// frequent first, capped. The per-occurrence paths are deliberately absent —
// they are unbounded and server-controlled, and an operator reading the log
// gets them in full.
func (p projection) droppedSummary() string {
	counts := make(map[string]int)
	var order []string
	for _, d := range p.dropped {
		if d.Whole {
			return d.Keyword
		}
		k := clip(d.Keyword)
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	slices.SortStableFunc(order, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	items := make([]string, 0, len(order))
	for _, k := range order {
		if counts[k] > 1 {
			items = append(items, fmt.Sprintf("%s (×%d)", k, counts[k]))
			continue
		}
		items = append(items, k)
	}
	return capList(items)
}

// capList joins items for model-facing prose, keeping at most [listCap] of
// them and summarizing the rest as a count.
func capList(items []string) string {
	if len(items) <= listCap {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(items[:listCap], ", "), len(items)-listCap)
}

// clip bounds one server-supplied token at [keywordRuneCap] runes. It counts
// runes, not bytes, so it can never split a multi-byte character into
// invalid UTF-8 on its way into the prompt.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= keywordRuneCap {
		return s
	}
	return string([]rune(s)[:keywordRuneCap]) + "..."
}

// sortedKeys returns m's keys in ascending order, so every walk over a
// decoded schema — and therefore every dropped-constraint report — is
// deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
