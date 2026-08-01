package benchguard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/internal/benchguard"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/tool"
	"github.com/jedwards1230/agent-sdk-go/toolindex"
)

// The seed benchmarks for the allocation gate. They measure [toolindex.Index],
// the decorator that stands between a federated tool surface and every model
// call, because that is where a slice-copy regression would be both easy to
// introduce and expensive: Index.Specs() builds a fresh []provider.ToolSpec on
// every model call in every turn of every session, and Index.Wrap snapshots the
// entire base tool set into entries + a spec map + two sorts.
//
// # Why these two shapes, and why they are not structurally blind
//
// A benchmark is structurally blind when nothing in its measured loop can
// prove it still reaches its target — the fixture regresses to a stub, the
// numbers fall, and an allocation gate applauds. Both benchmarks here route
// the real work through a [benchguard.Counter] on a fixture method the target
// must call, and assert a per-iteration floor after the loop:
//
//   - Wrap: toolindex.Index.Wrap calls base.Specs() exactly once
//     (toolindex/toolindex.go, `ix.buildIndex(base.Specs())`) — verified by
//     reading it, and it is the only base call Wrap makes. Floor: 1/iteration.
//   - Project: toolindex.Index.Get delegates to base.Get for every name, on
//     every call, before consulting its own promotion state
//     (toolindex/toolindex.go, `t, ok := base.Get(name)`). Floor: 1/iteration.
//
// Index.Specs() alone has NO such seam: it reads only the snapshot taken at
// Wrap and calls nothing on the base registry. A Specs()-only benchmark could
// therefore never prove it reached anything, which is why the projection
// benchmark pairs Get with Specs rather than measuring Specs by itself.
//
// # Why serial, never RunParallel
//
// Every benchmark here is serial. b.RunParallel does not produce stable
// allocs/op run to run — scheduling shifts how much of a fixture's work lands
// inside b.N — and a baselined parallel benchmark is a gate that fails
// unrelated PRs. See docs/TESTING.md.

// benchSizes spans a hand-wired local tool set through a heavily federated one
// (several MCP servers' worth). Read the alloc/byte claims off the larger rows:
// at n=8 size-class rounding dominates.
var benchSizes = []int{8, 64, 512}

// benchmark sinks, package level so the compiler cannot elide the calls under
// test. The benchmarks are serial, so a shared sink is not a data race.
var (
	sinkSpecs []provider.ToolSpec
	sinkTool  loop.Tool
	sinkOK    bool
)

// benchTool is a minimal tool.Tool: enough real Name/Description/Spec for
// loop.FromRegistry to marshal a genuine schema and for toolindex to
// summarize a genuine description. Nothing here is canned — the description is
// long enough that summarize() actually has to cut it, which is the work the
// benchmark is there to measure.
type benchTool struct {
	name string
	desc string
}

func (t benchTool) Name() string        { return t.name }
func (t benchTool) Description() string { return t.desc }

func (t benchTool) Spec() tool.Schema {
	return tool.ObjectSchema([]string{"path"}, map[string]tool.Property{
		"path":  {Type: "string", Description: "Absolute path to operate on."},
		"limit": {Type: "integer", Description: "Maximum results to return."},
	})
}

func (t benchTool) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: t.name + "-ok"}, nil
}

// countingRegistry is a loop.ToolRegistry decorator that routes Get and Specs
// through counters before delegating to a real base registry. It is the
// permanent guard seam: it is the ONLY thing between toolindex and the real
// tool.Registry, so a fixture change that stops reaching the base cannot avoid
// dropping the count.
//
// It deliberately does not fabricate specs. Delegating to a real
// loop.FromRegistry(*tool.Registry) means the benchmark measures toolindex
// against the shape of registry an embedder actually wires, JSON-marshaled
// schemas and all — a fake returning a canned slice would measure nothing.
type countingRegistry struct {
	base  loop.ToolRegistry
	specs *benchguard.Counter
	gets  *benchguard.Counter
}

func (r countingRegistry) Get(name string) (loop.Tool, bool) {
	r.gets.Hit()
	return r.base.Get(name)
}

func (r countingRegistry) Specs() []provider.ToolSpec {
	r.specs.Hit()
	return r.base.Specs()
}

// benchNames returns n deterministic tool names: a few local builtins followed
// by mcp__<server>__<tool> names spread over four servers, so Entry.Source
// derivation does real string work instead of hitting one constant. The name
// at index 0 is always toolindex.SearchToolName.
func benchNames(n int) []string {
	locals := []string{toolindex.SearchToolName, "read", "write", "edit", "bash", "glob", "grep", "todo"}
	servers := []string{"github", "postgres", "sentry", "filesystem"}

	names := make([]string, 0, n)
	for i := range n {
		if i < len(locals) {
			names = append(names, locals[i])
			continue
		}
		j := i - len(locals)
		names = append(names, fmt.Sprintf("mcp__%s__tool_%04d", servers[j%len(servers)], j))
	}
	return names
}

// newBenchBase builds the counting fixture over a real tool.Registry of n
// tools.
//
// One name is toolindex.SearchToolName, carried by a benchTool rather than by
// ix.SearchTool(): the real search tool is bound to one specific Index, Wrap
// panics if called twice on an Index, and the Wrap benchmark therefore has to
// construct a fresh Index every iteration — so the real search tool could not
// be pre-registered into a shared base. What Wrap consumes from the base is
// only the ToolSpec, and buildResident keys off the NAME
// (`if _, ok := ix.specs[SearchToolName]; ok`), so a tool registered under
// that name exercises the identical forced-resident branch. Skipping the name
// entirely would silently skip that branch.
func newBenchBase(tb testing.TB, n int) (countingRegistry, *benchguard.Counter, *benchguard.Counter) {
	tb.Helper()

	names := benchNames(n)
	tools := make([]tool.Tool, len(names))
	for i, name := range names {
		tools[i] = benchTool{
			name: name,
			desc: "Operate on " + name + ": a deliberately long description, longer than the default summary bound, so summarization has to cut it rather than pass it through.",
		}
	}
	reg := tool.NewRegistry(tools...)
	if got := reg.Len(); got != n {
		tb.Fatalf("bench registry has %d tools, want %d", got, n)
	}

	specs, gets := &benchguard.Counter{}, &benchguard.Counter{}
	fixture := countingRegistry{base: loop.FromRegistry(reg), specs: specs, gets: gets}

	// Warm the process-global caches this fixture touches before anything is
	// measured. loop.FromRegistry marshals every tool.Schema with
	// encoding/json, which builds and caches a per-type encoder the FIRST time
	// a process marshals that type — measured, that one-time cost lands
	// entirely in the first -count sample of whichever benchmark happens to run
	// first (observed: 139 allocs/op cold vs 137 warm at n=8, a 1.5% skew).
	// Warming here makes every sample a steady-state sample, which is what the
	// gate's tolerance is sized against.
	//
	// Reset afterwards: the guard's floor is stated per MEASURED iteration, so
	// setup invocations must not be allowed to pay for it.
	sinkSpecs = fixture.Specs()
	if _, ok := fixture.Get(names[0]); !ok {
		tb.Fatalf("warmup Get(%q) missed; the fixture is not wired to the registry", names[0])
	}
	specs.Reset()
	gets.Reset()

	return fixture, specs, gets
}

// residentCount is how many names benchResident pins: the "handful of builtins
// a session cannot function without" shape Options.Resident documents. It is
// strictly below the smallest entry in benchSizes so that every size still has
// a non-resident tool left to promote — at n == residentCount the projection
// benchmark would have nothing to discover and would silently measure the
// resident-only path at every size.
const residentCount = 4

// benchResident pins the first residentCount names resident.
func benchResident(names []string) func(string) bool {
	set := make(map[string]bool, residentCount)
	for _, name := range names[:min(residentCount, len(names))] {
		set[name] = true
	}
	return func(name string) bool { return set[name] }
}

// BenchmarkIndexWrap measures index construction: New + Wrap + one Specs
// projection. Wrap is where the whole base tool set is snapshotted — entries
// slice, spec map, two sorts — so this is the allocation profile that scales
// with the number of federated tools.
//
// Floor 1/iteration on base.Specs(): Wrap calls it exactly once, and it is
// unreachable without actually entering Wrap. A fixture that stopped building
// a real index would drop the count to zero and fail here rather than post a
// suspiciously good number.
func BenchmarkIndexWrap(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			base, specs, _ := newBenchBase(b, n)
			resident := benchResident(benchNames(n))

			var iters int64
			b.ReportAllocs()
			for b.Loop() {
				ix := toolindex.New(toolindex.Options{Resident: resident})
				sinkSpecs = ix.Wrap(base).Specs()
				iters++
			}

			benchguard.Assert(b, benchguard.Floor{
				Name:         "loop.ToolRegistry.Specs via toolindex.Index.Wrap",
				PerIteration: 1,
			}, iters, specs)
		})
	}
}

// BenchmarkIndexProject measures the per-model-call hot path on an index that
// is already built: resolve a tool by name, then project the visible spec set.
// This is what runs on every iteration of every turn — req.Tools =
// cfg.Tools.Specs() in the loop's callModel — so a per-call slice copy landing
// here costs on every model call, not once per session.
//
// The promoted name is promoted during setup, before the measured loop, so the
// index's state is steady: promotion is monotonic and idempotent, and letting
// the first measured iteration be the one that grows the promoted set would
// make allocs/op depend on -benchtime.
//
// Floor 1/iteration on base.Get(): Index.Get delegates to the base for every
// lookup before consulting its own state, so a fixture that stopped resolving
// through the real registry drops the count. Specs() has no such seam by
// itself, which is exactly why it is measured paired with Get and not alone.
func BenchmarkIndexProject(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			base, _, gets := newBenchBase(b, n)
			names := benchNames(n)
			ix := toolindex.New(toolindex.Options{Resident: benchResident(names)})
			wrapped := ix.Wrap(base)

			// The last name is the furthest from the resident set: a
			// federated MCP tool the model discovered and promoted.
			target := names[len(names)-1]
			if got := ix.Promote(target); len(got) != 1 {
				b.Fatalf("Promote(%q) = %v, want it newly promoted", target, got)
			}
			gets.Reset() // setup must not count toward the measured floor

			var iters int64
			b.ReportAllocs()
			for b.Loop() {
				sinkTool, sinkOK = wrapped.Get(target)
				sinkSpecs = wrapped.Specs()
				iters++
			}

			if !sinkOK {
				b.Fatalf("Get(%q) reported the tool missing; the benchmark measured a miss path", target)
			}
			benchguard.Assert(b, benchguard.Floor{
				Name:         "loop.ToolRegistry.Get via toolindex.Index.Get",
				PerIteration: 1,
			}, iters, gets)
		})
	}
}
