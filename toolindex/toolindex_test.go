package toolindex_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/tool"
	"github.com/jedwards1230/agent-sdk-go/toolindex"
)

// stubTool is a minimal tool.Tool used to build a base registry for these
// tests. name is also used to derive Description so summarization has real
// text to chew on.
type stubTool struct {
	name string
	desc string
}

func (s stubTool) Name() string        { return s.name }
func (s stubTool) Description() string { return s.desc }
func (s stubTool) Spec() tool.Schema {
	return tool.ObjectSchema([]string{"x"}, map[string]tool.Property{"x": {Type: "string"}})
}
func (s stubTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: s.name + "-ok"}, nil
}

// newBase builds a base loop.ToolRegistry (a real tool.Registry adapted via
// loop.FromRegistry) from names, and describes each as "desc for <name>".
func newBase(t *testing.T, names ...string) *tool.Registry {
	t.Helper()
	tools := make([]tool.Tool, len(names))
	for i, n := range names {
		tools[i] = stubTool{name: n, desc: "desc for " + n}
	}
	return tool.NewRegistry(tools...)
}

// wrapped is a small harness bundling the Index, its base registry, and a
// Wrap already performed with the search tool pre-registered — the two-phase
// construction every real caller must follow.
func wrapped(t *testing.T, opts toolindex.Options, names ...string) (*toolindex.Index, loop.ToolRegistry) {
	t.Helper()
	ix := toolindex.New(opts)
	reg := newBase(t, names...)
	if err := reg.Register(ix.SearchTool()); err != nil {
		t.Fatalf("register search tool: %v", err)
	}
	base := ix.Wrap(loop.FromRegistry(reg))
	return ix, base
}

func TestWrapPanicsOnSecondCall(t *testing.T) {
	ix := toolindex.New(toolindex.Options{})
	reg := newBase(t, "a")
	if err := reg.Register(ix.SearchTool()); err != nil {
		t.Fatal(err)
	}
	base := loop.FromRegistry(reg)
	ix.Wrap(base)

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on second Wrap call")
		}
	}()
	ix.Wrap(base)
}

func TestSpecsOrdering(t *testing.T) {
	resident := map[string]bool{"alpha": true, "beta": true}
	ix, base := wrapped(t, toolindex.Options{
		Resident: func(name string) bool { return resident[name] },
	}, "alpha", "beta", "gamma", "delta")

	specs := base.Specs()
	// Resident set is {alpha, beta, tool_search}, sorted by name.
	wantResidentNames := []string{"alpha", "beta", toolindex.SearchToolName}
	if len(specs) != len(wantResidentNames) {
		t.Fatalf("Specs() len = %d, want %d (%+v)", len(specs), len(wantResidentNames), specs)
	}
	for i, name := range wantResidentNames {
		if specs[i].Name != name {
			t.Errorf("Specs()[%d].Name = %q, want %q", i, specs[i].Name, name)
		}
	}

	// Promote gamma and delta (in that order); they must append AFTER the
	// resident segment, in promotion order, never merged into sorted order.
	newly := ix.Promote("delta", "gamma")
	if len(newly) != 2 || newly[0] != "delta" || newly[1] != "gamma" {
		t.Fatalf("Promote() = %v", newly)
	}

	after := base.Specs()
	wantAll := []string{"alpha", "beta", toolindex.SearchToolName, "delta", "gamma"}
	if len(after) != len(wantAll) {
		t.Fatalf("Specs() after promotion len = %d, want %d (%+v)", len(after), len(wantAll), after)
	}
	for i, name := range wantAll {
		if after[i].Name != name {
			t.Errorf("Specs()[%d].Name = %q, want %q", i, after[i].Name, name)
		}
	}
}

// TestSpecsResidentPrefixStableAcrossPromotion asserts the cache-safety
// property directly: the resident segment's bytes (marshaled JSON, index by
// index) are identical before and after a promotion, so a provider's
// longest-cached-prefix match against consecutive requests only has to
// reprice the promoted tail.
func TestSpecsResidentPrefixStableAcrossPromotion(t *testing.T) {
	resident := map[string]bool{"alpha": true, "beta": true}
	ix, base := wrapped(t, toolindex.Options{
		Resident: func(name string) bool { return resident[name] },
	}, "alpha", "beta", "gamma", "delta", "epsilon")

	before := base.Specs()
	residentLen := len(before) // {alpha, beta, tool_search}
	beforeBytes := make([][]byte, residentLen)
	for i := 0; i < residentLen; i++ {
		b, err := json.Marshal(before[i])
		if err != nil {
			t.Fatal(err)
		}
		beforeBytes[i] = b
	}

	ix.Promote("gamma")
	ix.Promote("delta", "epsilon")

	after := base.Specs()
	if len(after) < residentLen {
		t.Fatalf("Specs() shrank after promotion: %d < %d", len(after), residentLen)
	}
	for i := 0; i < residentLen; i++ {
		b, err := json.Marshal(after[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != string(beforeBytes[i]) {
			t.Errorf("resident spec at index %d changed:\nbefore=%s\nafter=%s", i, beforeBytes[i], b)
		}
	}
}

func TestGetAutoPromotes(t *testing.T) {
	ix, base := wrapped(t, toolindex.Options{}, "alpha", "beta")

	// Nothing is resident (no Options.Resident) besides the search tool, so
	// alpha starts out not advertised in Specs().
	specs := base.Specs()
	for _, s := range specs {
		if s.Name == "alpha" {
			t.Fatal("alpha unexpectedly resident before Get")
		}
	}

	tl, ok := base.Get("alpha")
	if !ok || tl == nil {
		t.Fatalf("Get(alpha) = (%v, %v)", tl, ok)
	}
	res := ix.Resident()
	found := false
	for _, n := range res {
		if n == "alpha" {
			found = true
		}
	}
	if !found {
		t.Errorf("Resident() = %v, want alpha auto-promoted", res)
	}

	specsAfter := base.Specs()
	found = false
	for _, s := range specsAfter {
		if s.Name == "alpha" {
			found = true
		}
	}
	if !found {
		t.Error("Specs() after Get(alpha) does not include alpha")
	}
}

func TestGetUnknownReturnsFalse(t *testing.T) {
	_, base := wrapped(t, toolindex.Options{}, "alpha")
	tl, ok := base.Get("does-not-exist")
	if ok || tl != nil {
		t.Fatalf("Get(unknown) = (%v, %v), want (nil, false)", tl, ok)
	}
}

func TestBatchedPromotion(t *testing.T) {
	ix, _ := wrapped(t, toolindex.Options{}, "alpha", "beta", "gamma")

	newly := ix.Promote("alpha", "beta", "alpha")
	if len(newly) != 2 || newly[0] != "alpha" || newly[1] != "beta" {
		t.Fatalf("Promote() = %v, want [alpha beta] (dup collapsed)", newly)
	}

	// A second call promoting the same names plus one new one only reports
	// the genuinely new one.
	newly2 := ix.Promote("alpha", "beta", "gamma")
	if len(newly2) != 1 || newly2[0] != "gamma" {
		t.Fatalf("Promote() repeat = %v, want [gamma]", newly2)
	}
}

func TestSearchToolBatchPromotes(t *testing.T) {
	ix, base := wrapped(t, toolindex.Options{}, "wiki-search", "wiki-edit", "unrelated")

	search := ix.SearchTool()
	out, err := search.Run(context.Background(), json.RawMessage(`{"query":"wiki"}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.IsError {
		t.Fatalf("unexpected error result: %s", out.Content)
	}
	if !strings.Contains(out.Content, "wiki-search") || !strings.Contains(out.Content, "wiki-edit") {
		t.Fatalf("result missing matched entries: %s", out.Content)
	}
	if strings.Contains(out.Content, "unrelated") {
		t.Fatalf("result unexpectedly matched unrelated entry: %s", out.Content)
	}
	// The result body must never carry a matched tool's schema.
	if strings.Contains(out.Content, "input_schema") || strings.Contains(out.Content, `"type":"object"`) {
		t.Fatalf("result body leaked a schema: %s", out.Content)
	}

	specs := base.Specs()
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
	}
	if !names["wiki-search"] || !names["wiki-edit"] {
		t.Errorf("Specs() after tool_search = %+v, want wiki-search and wiki-edit promoted", specs)
	}
	if names["unrelated"] {
		t.Error("unrelated tool was promoted by an unrelated search")
	}
}

func TestHintInlineTier(t *testing.T) {
	ix, _ := wrapped(t, toolindex.Options{InlineMax: 5}, "alpha", "beta", "gamma")
	hint := ix.Hint()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(hint, name) {
			t.Errorf("inline Hint() missing %q: %s", name, hint)
		}
	}
	if strings.Contains(hint, "Call tool_search") {
		t.Errorf("inline tier should not carry the roster tier's call-to-action: %s", hint)
	}
}

func TestHintRosterTier(t *testing.T) {
	names := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		names = append(names, fmt.Sprintf("mcp__wiki__t%d", i))
	}
	ix, _ := wrapped(t, toolindex.Options{InlineMax: 3}, names...)
	hint := ix.Hint()
	if !strings.Contains(hint, "mcp:wiki") {
		t.Errorf("roster Hint() missing source grouping: %s", hint)
	}
	if !strings.Contains(hint, "tool_search") {
		t.Errorf("roster Hint() missing tool_search instruction: %s", hint)
	}
	// The roster tier must not spell out every single tool name (that would
	// be the inline tier in disguise).
	if strings.Count(hint, "mcp__wiki__t") > 4 {
		t.Errorf("roster Hint() looks like it inlined every entry: %s", hint)
	}
}

// TestHintInlineMaxBoundary asserts the tier switches exactly at InlineMax:
// at the boundary entry count it inlines; one entry past it, it rosters. The
// index always carries one extra entry beyond the stub names supplied here —
// tool_search itself, registered into the base like any other tool — so the
// stub counts are offset by one to land exactly on/over the boundary.
func TestHintInlineMaxBoundary(t *testing.T) {
	const inlineMax = 4

	atBoundary := make([]string, inlineMax-1) // +1 for tool_search = inlineMax entries
	for i := range atBoundary {
		atBoundary[i] = fmt.Sprintf("tool%d", i)
	}
	ix, _ := wrapped(t, toolindex.Options{InlineMax: inlineMax}, atBoundary...)
	if got := len(ix.Entries()); got != inlineMax {
		t.Fatalf("test setup: entry count = %d, want %d", got, inlineMax)
	}
	hint := ix.Hint()
	if strings.Contains(hint, "Call tool_search") {
		t.Errorf("at InlineMax boundary, expected inline tier, got roster: %s", hint)
	}

	overBoundary := make([]string, inlineMax) // +1 for tool_search = inlineMax+1 entries
	for i := range overBoundary {
		overBoundary[i] = fmt.Sprintf("tool%d", i)
	}
	ix2, _ := wrapped(t, toolindex.Options{InlineMax: inlineMax}, overBoundary...)
	if got := len(ix2.Entries()); got != inlineMax+1 {
		t.Fatalf("test setup: entry count = %d, want %d", got, inlineMax+1)
	}
	hint2 := ix2.Hint()
	if !strings.Contains(hint2, "Call tool_search") {
		t.Errorf("one past InlineMax boundary, expected roster tier, got inline: %s", hint2)
	}
}

func TestSummaryTruncatesAtWordBoundary(t *testing.T) {
	ix := toolindex.New(toolindex.Options{SummaryBytes: 20})
	reg := tool.NewRegistry(
		stubTool{name: "longdesc", desc: "this description is deliberately much longer than twenty bytes so it must be cut"},
	)
	if err := reg.Register(ix.SearchTool()); err != nil {
		t.Fatal(err)
	}
	ix.Wrap(loop.FromRegistry(reg))

	var entry *toolindex.Entry
	for _, e := range ix.Entries() {
		if e.Name == "longdesc" {
			e := e
			entry = &e
		}
	}
	if entry == nil {
		t.Fatal("longdesc entry not found")
	}
	if !strings.HasSuffix(entry.Summary, "...") {
		t.Errorf("Summary = %q, want ellipsis suffix", entry.Summary)
	}
	if strings.HasSuffix(strings.TrimSuffix(entry.Summary, "..."), " ") {
		t.Errorf("Summary = %q, trailing space before ellipsis", entry.Summary)
	}
	if len(entry.Summary) > 20+len("...") {
		t.Errorf("Summary = %q, exceeds SummaryBytes bound", entry.Summary)
	}
	// Must not have split a word: every summary word (minus the ellipsis)
	// must be a prefix of the original description's word sequence.
	words := strings.Fields(strings.TrimSuffix(entry.Summary, "..."))
	orig := strings.Fields("this description is deliberately much longer than twenty bytes so it must be cut")
	for i, w := range words {
		if i >= len(orig) || w != orig[i] {
			t.Errorf("Summary word %d = %q, want a clean prefix of the original words", i, w)
		}
	}
}

func TestSummaryStopsAtFirstBlankLine(t *testing.T) {
	ix := toolindex.New(toolindex.Options{})
	reg := tool.NewRegistry(
		stubTool{name: "paragraphs", desc: "first paragraph only.\n\nsecond paragraph must not appear"},
	)
	if err := reg.Register(ix.SearchTool()); err != nil {
		t.Fatal(err)
	}
	ix.Wrap(loop.FromRegistry(reg))

	for _, e := range ix.Entries() {
		if e.Name == "paragraphs" {
			if strings.Contains(e.Summary, "second paragraph") {
				t.Errorf("Summary leaked past the first blank line: %q", e.Summary)
			}
			if e.Summary != "first paragraph only." {
				t.Errorf("Summary = %q, want %q", e.Summary, "first paragraph only.")
			}
		}
	}
}

func TestSourceDerivationDefault(t *testing.T) {
	ix, _ := wrapped(t, toolindex.Options{}, "mcp__wiki__search", "mcp__grafana__query", "local-bash")
	got := map[string]string{}
	for _, e := range ix.Entries() {
		got[e.Name] = e.Source
	}
	if got["mcp__wiki__search"] != "mcp:wiki" {
		t.Errorf("Source(mcp__wiki__search) = %q, want mcp:wiki", got["mcp__wiki__search"])
	}
	if got["mcp__grafana__query"] != "mcp:grafana" {
		t.Errorf("Source(mcp__grafana__query) = %q, want mcp:grafana", got["mcp__grafana__query"])
	}
	if got["local-bash"] != "local" {
		t.Errorf("Source(local-bash) = %q, want local", got["local-bash"])
	}
}

func TestSourceDerivationOverride(t *testing.T) {
	ix, _ := wrapped(t, toolindex.Options{
		Source: func(name string) string { return "custom:" + name },
	}, "alpha")
	for _, e := range ix.Entries() {
		if e.Name == "alpha" && e.Source != "custom:alpha" {
			t.Errorf("Source override not applied: %q", e.Source)
		}
	}
}

func TestRehydrate(t *testing.T) {
	ix, base := wrapped(t, toolindex.Options{}, "alpha", "beta")

	ix.Rehydrate("alpha")
	specs := base.Specs()
	found := false
	for _, s := range specs {
		if s.Name == "alpha" {
			found = true
		}
	}
	if !found {
		t.Error("Rehydrate did not make alpha visible in Specs()")
	}

	// Idempotent / order-preserving: rehydrating an already-visible name and
	// a new one only appends the new one.
	ix.Rehydrate("alpha", "beta")
	res := ix.Resident()
	betaIdx, alphaIdx := -1, -1
	for i, n := range res {
		if n == "alpha" {
			alphaIdx = i
		}
		if n == "beta" {
			betaIdx = i
		}
	}
	if alphaIdx == -1 || betaIdx == -1 || betaIdx < alphaIdx {
		t.Errorf("Resident() = %v, want alpha before beta, both present", res)
	}
}

// TestMonotonicity asserts residency only ever grows within a run: nothing
// promoted is ever removed from Resident()/Specs() by a later call.
func TestMonotonicity(t *testing.T) {
	ix, base := wrapped(t, toolindex.Options{}, "alpha", "beta", "gamma")

	ix.Promote("alpha")
	first := ix.Resident()

	ix.Promote("beta")
	second := ix.Resident()

	if len(second) < len(first) {
		t.Fatalf("Resident() shrank: %v -> %v", first, second)
	}
	seen := map[string]bool{}
	for _, n := range second {
		seen[n] = true
	}
	for _, n := range first {
		if !seen[n] {
			t.Errorf("name %q demoted between calls", n)
		}
	}

	specs := base.Specs()
	specNames := map[string]bool{}
	for _, s := range specs {
		specNames[s.Name] = true
	}
	for _, n := range second {
		if !specNames[n] {
			t.Errorf("Resident() name %q missing from Specs()", n)
		}
	}
}

// TestConcurrentResidentReadsAgainstPromotion drives Resident() reads on one
// goroutine while Promote() (via Get, mimicking loop-goroutine auto-promotion)
// runs on another, under -race.
func TestConcurrentResidentReadsAgainstPromotion(t *testing.T) {
	names := make([]string, 50)
	for i := range names {
		names[i] = fmt.Sprintf("tool%d", i)
	}
	ix, base := wrapped(t, toolindex.Options{}, names...)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = ix.Resident()
				_ = base.Specs()
			}
		}
	}()

	for _, n := range names {
		if _, ok := base.Get(n); !ok {
			t.Errorf("Get(%s) = false", n)
		}
	}
	close(stop)
	wg.Wait()

	res := ix.Resident()
	if len(res) != len(names)+1 { // +1 for tool_search
		t.Errorf("Resident() len = %d, want %d", len(res), len(names)+1)
	}
}
