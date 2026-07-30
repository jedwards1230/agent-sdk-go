// Package toolindex is an optional decorator over [loop.ToolRegistry] that
// projects a large federated tool surface as a small resident index plus a
// tool_search escape hatch, instead of every tool's full schema riding every
// model call. Federating N MCP servers must not mean N resident schemas.
//
// The mechanism lives entirely at the loop's single re-evaluated projection
// point — req.Tools = r.cfg.Tools.Specs() in (*runner).callModel — because
// loop.ToolRegistry is a two-method consumer-side interface (Get + Specs).
// This package never imports or modifies tool.Tool or tool.Registry: an
// Index wraps whatever loop.ToolRegistry the embedder already built (usually
// loop.FromRegistry(*tool.Registry)) and satisfies the same interface, so the
// loop needs no change to consume it. See docs/DESIGN.md for the normative
// contract this implements.
package toolindex

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

// Default tuning values. Each backs a zero-valued Options field — never a
// hardcoded literal at the call site — so a caller can override exactly the
// knob it cares about and get documented defaults for the rest.
const (
	// DefaultSummaryBytes bounds Entry.Summary: one legible line, roughly 40
	// tokens per indexed tool.
	DefaultSummaryBytes = 160
	// DefaultMaxResults bounds a Search call that specifies no limit of its
	// own: a result set that fits a screen and a context budget.
	DefaultMaxResults = 10
	// DefaultInlineMax is the entry count at or below which Hint inlines the
	// whole index instead of a per-Source roster: at or below it, inlining
	// costs less context than a tool_search round trip; above it (a flat
	// index over hundreds of federated tools runs to several thousand
	// tokens), the roster tier keeps Hint small and auditable.
	DefaultInlineMax = 25
)

// Entry is one line of the tool index: enough for the model to decide
// whether to call tool_search or Promote further, never the full schema —
// that is exactly what the index exists to keep out of every turn's context
// until it is earned.
type Entry struct {
	// Name is the tool name exactly as the model must call it.
	Name string
	// Summary is a one-line, whitespace-collapsed, byte-bounded cut of the
	// tool's Description — legible at a glance, not a schema substitute.
	Summary string
	// Source groups entries for Hint's roster tier: "mcp:<server>" for a
	// federated MCP tool by default, "local" otherwise (Options.Source
	// overrides the derivation).
	Source string
}

// Options configures an [Index]. The zero value is usable: it yields the
// conservative default of nothing pinned resident (beyond the search tool
// itself, which the mechanism requires) and the package's Default* tuning.
type Options struct {
	// Resident decides which base tool names are advertised in full, every
	// turn, with no tool_search round trip — typically the handful of
	// builtins a session cannot function without. nil means nothing is
	// pinned resident this way; the tool_search tool is still always
	// resident regardless (see [Index.Wrap]), or the index could never be
	// discovered in the first place.
	Resident func(name string) bool
	// Source overrides the default "mcp__<server>__*" → "mcp:<server>" /
	// else "local" grouping used by Entry.Source and Hint's roster tier.
	// nil uses the default derivation.
	Source func(name string) string
	// SummaryBytes bounds each Entry.Summary. 0 uses DefaultSummaryBytes.
	SummaryBytes int
	// MaxResults bounds a Search call that does not specify its own limit.
	// 0 uses DefaultMaxResults.
	MaxResults int
	// InlineMax is the entry count at or below which Hint inlines the whole
	// index instead of a per-Source roster. 0 uses DefaultInlineMax.
	InlineMax int
}

// Index decorates a base [loop.ToolRegistry], replacing the "advertise every
// tool's full schema every call" projection with a small resident set plus
// promoted discoveries. It satisfies loop.ToolRegistry itself, so an embedder
// assigns it straight to loop.Config.Tools — the loop needs no changes to
// consume it.
//
// Construction is deliberately two-phase: New builds the Index; [Index.Wrap]
// finishes it by snapshotting the base registry's current tool set. Call
// [Index.SearchTool] and register its result into the base tool.Registry
// BEFORE calling Wrap, so the search tool itself is present in the very
// snapshot Wrap takes — otherwise Get("tool_search") would fail because the
// base would not know it yet.
//
// The zero value is not usable; construct with [New]. Safe for concurrent
// use once Wrap has returned: reads (Specs, Get, Resident, Entries, Search)
// and writes (Get's auto-promotion, Promote, Rehydrate) are RWMutex-guarded,
// matching tool.Registry.
type Index struct {
	resident     func(name string) bool
	source       func(name string) string
	summaryBytes int
	maxResults   int
	inlineMax    int

	mu      sync.RWMutex
	base    loop.ToolRegistry
	wrapped bool

	// entries and specs are snapshotted once, in Wrap, from base.Specs() —
	// the single source Entry derivation is defined against. They never
	// change afterward: only which names are visible (residentOrder fixed at
	// Wrap, promoted growing monotonically) changes.
	entries []Entry
	specs   map[string]provider.ToolSpec

	residentSet   map[string]bool
	residentOrder []string // sorted subset of entries' names, fixed at Wrap

	promoted    []string // promotion order, append-only
	promotedSet map[string]bool
}

// New constructs an Index from opts, resolving zero-valued tuning fields to
// their documented defaults. The returned Index is not yet a usable
// loop.ToolRegistry — call [Index.Wrap] to finish construction.
func New(opts Options) *Index {
	summaryBytes := opts.SummaryBytes
	if summaryBytes <= 0 {
		summaryBytes = DefaultSummaryBytes
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	inlineMax := opts.InlineMax
	if inlineMax <= 0 {
		inlineMax = DefaultInlineMax
	}
	return &Index{
		resident:     opts.Resident,
		source:       opts.Source,
		summaryBytes: summaryBytes,
		maxResults:   maxResults,
		inlineMax:    inlineMax,
	}
}

// Wrap finishes construction: it snapshots base's current tool set (name,
// description, schema) into the index's entries, fixes the always-resident
// name set from Options.Resident (plus the search tool, forced resident so
// the discovery mechanism can never go dark — a search-only registry that
// never advertises its own search tool could never be discovered), and
// returns ix as a loop.ToolRegistry.
//
// It panics if called a second time on the same Index, matching
// tool.NewRegistry's construction-time-error contract: a second Wrap would
// silently rebase the whole index against a new base and invalidate every
// promotion already recorded, which is a programming error to catch at
// construction, not a runtime condition to handle.
func (ix *Index) Wrap(base loop.ToolRegistry) loop.ToolRegistry {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.wrapped {
		panic("toolindex: Wrap called twice on the same Index")
	}
	ix.wrapped = true
	ix.base = base
	ix.buildIndex(base.Specs())
	ix.buildResident()
	return ix
}

// buildIndex snapshots specs into ix.entries (sorted by Name) and
// ix.specs (name → full spec). Called once, from Wrap, while ix.mu is held.
func (ix *Index) buildIndex(specs []provider.ToolSpec) {
	entries := make([]Entry, 0, len(specs))
	specMap := make(map[string]provider.ToolSpec, len(specs))
	for _, s := range specs {
		entries = append(entries, Entry{
			Name:    s.Name,
			Summary: summarize(s.Description, ix.summaryBytes),
			Source:  ix.deriveSource(s.Name),
		})
		specMap[s.Name] = s
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	ix.entries = entries
	ix.specs = specMap
}

// buildResident computes the fixed always-visible name set from
// Options.Resident plus the forced-resident search tool. Called once, from
// Wrap, while ix.mu is held.
func (ix *Index) buildResident() {
	set := make(map[string]bool)
	for _, e := range ix.entries {
		if ix.resident != nil && ix.resident(e.Name) {
			set[e.Name] = true
		}
	}
	// The tool_search tool is the only way to discover anything else in the
	// index; it must be visible from the first model call regardless of
	// Options.Resident, or the mechanism this package exists to provide is
	// unreachable.
	if _, ok := ix.specs[SearchToolName]; ok {
		set[SearchToolName] = true
	}
	order := make([]string, 0, len(set))
	for name := range set {
		order = append(order, name)
	}
	sort.Strings(order)
	ix.residentSet = set
	ix.residentOrder = order
}

// Get delegates to the base registry for any name it knows, auto-promoting a
// resident/promoted-set miss so a model that already guessed a tool name
// correctly (training data, a prior turn, a human-provided name) is served
// rather than punished for skipping tool_search. It ran with arguments the
// provider never schema-validated (the model never saw the schema before
// this call) — acceptable, because the tool's own Run validates and any
// configured guard still gates it. An unknown name is base's unknown name
// too, so the loop's existing "unknown tool" error result is unchanged.
func (ix *Index) Get(name string) (loop.Tool, bool) {
	ix.mu.RLock()
	base := ix.base
	ix.mu.RUnlock()
	t, ok := base.Get(name)
	if !ok {
		return nil, false
	}
	ix.mu.Lock()
	ix.promoteLocked(name)
	ix.mu.Unlock()
	return t, true
}

// Specs returns full ToolSpecs for the resident set (Options.Resident plus
// the search tool, sorted by name) followed by the promoted set (in
// promotion order) — the union the model has earned visibility into, and no
// more. The index itself never rides in Specs(): tool_search is a tool like
// any other in the resident set, but the entries/summaries backing Hint and
// Search are never projected as specs.
//
// The resident segment's order and content never change after Wrap, so
// indices [0..len(resident)-1] stay byte-identical across every call: a
// provider's longest-cached-prefix match against consecutive requests only
// has to reprice the promoted tail when a new tool is promoted, not the
// whole tool list.
func (ix *Index) Specs() []provider.ToolSpec {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]provider.ToolSpec, 0, len(ix.residentOrder)+len(ix.promoted))
	for _, name := range ix.residentOrder {
		out = append(out, ix.specs[name])
	}
	for _, name := range ix.promoted {
		out = append(out, ix.specs[name])
	}
	return out
}

// Entries returns every indexed tool's Entry, sorted by Name — the full
// index Search and Hint's inline tier draw from. The returned slice is a
// copy; mutating it does not affect the Index.
func (ix *Index) Entries() []Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]Entry, len(ix.entries))
	copy(out, ix.entries)
	return out
}

// Search returns entries whose Name, Summary, or Source case-insensitively
// contains query, in Entries order, capped at limit (<= 0 uses
// Options.MaxResults). An empty query matches every entry, letting a caller
// browse the whole index a page at a time.
func (ix *Index) Search(query string, limit int) []Entry {
	ix.mu.RLock()
	entries := ix.entries
	ix.mu.RUnlock()
	if limit <= 0 {
		limit = ix.maxResults
	}
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]Entry, 0, limit)
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		if q == "" || strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Summary), q) ||
			strings.Contains(strings.ToLower(e.Source), q) {
			out = append(out, e)
		}
	}
	return out
}

// Promote adds names to the promoted set, in the order given, skipping any
// name already resident or already promoted (idempotent — a repeat name
// costs nothing) and any name outside the indexed snapshot (nothing to
// advertise a schema for). It returns only the names newly promoted by this
// call: the batching contract tool_search relies on, so one search promoting
// N results costs one Specs()-tail rewrite rather than N.
func (ix *Index) Promote(names ...string) []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.promoteLocked(names...)
}

// promoteLocked is Promote's body; the caller must hold ix.mu for writing.
func (ix *Index) promoteLocked(names ...string) []string {
	var newly []string
	for _, name := range names {
		if ix.residentSet[name] || ix.promotedSet[name] {
			continue
		}
		if _, ok := ix.specs[name]; !ok {
			continue
		}
		if ix.promotedSet == nil {
			ix.promotedSet = make(map[string]bool)
		}
		ix.promotedSet[name] = true
		ix.promoted = append(ix.promoted, name)
		newly = append(newly, name)
	}
	return newly
}

// Rehydrate re-marks previously known tool names as promoted without going
// through Get or tool_search — the session-resume path: a client restoring a
// run from disk already knows (e.g. from the journal) which tool names a
// prior turn had promoted, and wants Specs() to include them again from the
// very first model call, rather than re-paying a tool_search round trip or
// waiting for the model to re-guess an unresolved name via Get. It shares
// Promote's monotonic, order-preserving, already-visible-is-a-no-op
// semantics; it returns nothing because the caller already knows what it
// asked for.
func (ix *Index) Rehydrate(names ...string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.promoteLocked(names...)
}

// Resident returns the full currently-visible tool name set — the fixed
// resident names followed by the promoted names, in the same order Specs
// emits them — for a client to display "what's loaded right now". It takes
// only the read lock, so it is safe to call from a display goroutine
// concurrently with the loop goroutine driving Get/Promote.
func (ix *Index) Resident() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]string, 0, len(ix.residentOrder)+len(ix.promoted))
	out = append(out, ix.residentOrder...)
	out = append(out, ix.promoted...)
	return out
}

// Hint renders a model-facing description of the current tool surface, at
// one of two tiers gated on Options.InlineMax: at or below it, the whole
// name — summary index, one line per tool; above it, a per-Source roster
// (count plus a few sample short names) plus the instruction to call
// tool_search. This matters at scale: a flat index over ~400 federated tools
// runs to roughly 8k tokens — cheaper than 400 schemas, but not "small and
// auditable" — so the roster tier keeps Hint itself bounded regardless of
// how large the federated surface grows.
//
// Hint returns a string; it never injects itself anywhere. The embedder
// composes it into the system prompt, replaces it, or drops it entirely —
// that is how "nothing enters the model's context the embedder can't see and
// override" is upheld for this package.
func (ix *Index) Hint() string {
	ix.mu.RLock()
	entries := ix.entries
	inlineMax := ix.inlineMax
	ix.mu.RUnlock()
	if len(entries) == 0 {
		return ""
	}
	if len(entries) <= inlineMax {
		return inlineHint(entries)
	}
	return rosterHint(entries)
}

// inlineHint renders every entry as one "name — summary" line, used at or
// below Options.InlineMax.
func inlineHint(entries []Entry) string {
	var b strings.Builder
	b.WriteString("Tool index (")
	b.WriteString(strconv.Itoa(len(entries)))
	b.WriteString(" tools):\n")
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Name)
		b.WriteString(" — ")
		b.WriteString(e.Summary)
	}
	return b.String()
}

// rosterHint renders one line per Source (count plus up to three sample
// short names) followed by the tool_search instruction, used above
// Options.InlineMax so Hint itself stays small regardless of federated
// surface size.
func rosterHint(entries []Entry) string {
	order := make([]string, 0)
	bySource := make(map[string][]Entry)
	for _, e := range entries {
		if _, ok := bySource[e.Source]; !ok {
			order = append(order, e.Source)
		}
		bySource[e.Source] = append(bySource[e.Source], e)
	}
	sort.Strings(order)

	var b strings.Builder
	b.WriteString("Tool index (")
	b.WriteString(strconv.Itoa(len(entries)))
	b.WriteString(" tools across ")
	b.WriteString(strconv.Itoa(len(order)))
	b.WriteString(" sources). Call tool_search{query} to find and load a tool's full schema:\n")
	for i, src := range order {
		if i > 0 {
			b.WriteByte('\n')
		}
		group := bySource[src]
		b.WriteString(src)
		b.WriteString(" — ")
		b.WriteString(strconv.Itoa(len(group)))
		b.WriteString(" tools (")
		n := len(group)
		if n > 3 {
			n = 3
		}
		for j := 0; j < n; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(shortName(group[j].Name))
		}
		b.WriteString(")")
	}
	return b.String()
}

// shortName strips a "prefix__" grouping (e.g. "mcp__wiki__search" →
// "search") for the roster tier's sample names; a name with no such
// separator is returned unchanged.
func shortName(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		return name[i+2:]
	}
	return name
}

// deriveSource resolves an entry's Source via Options.Source when set, else
// the default "mcp__<server>__*" → "mcp:<server>" / else "local" grouping.
func (ix *Index) deriveSource(name string) string {
	if ix.source != nil {
		return ix.source(name)
	}
	return defaultSource(name)
}

// defaultSource implements the default Entry.Source derivation: an MCP tool
// name of the form "mcp__<server>__<tool>" groups as "mcp:<server>";
// everything else groups as "local".
func defaultSource(name string) string {
	parts := strings.SplitN(name, "__", 3)
	if len(parts) == 3 && parts[0] == "mcp" && parts[1] != "" {
		return "mcp:" + parts[1]
	}
	return "local"
}

// summarize derives Entry.Summary from a tool's full Description: cut at the
// first blank line, whitespace-collapsed, then cut at a word boundary within
// maxBytes with a trailing ellipsis if that cut actually shortened it.
func summarize(desc string, maxBytes int) string {
	if i := strings.Index(desc, "\n\n"); i >= 0 {
		desc = desc[:i]
	}
	collapsed := strings.Join(strings.Fields(desc), " ")
	if len(collapsed) <= maxBytes {
		return collapsed
	}
	cut := collapsed[:maxBytes]
	// Never split a multi-byte rune: trim back to a valid UTF-8 boundary
	// before looking for a word boundary.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ") + "..."
}
