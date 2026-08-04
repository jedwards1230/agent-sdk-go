package benchguard_test

import (
	"fmt"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/tool"
)

// BenchmarkRegistrySpecs measures the plain registry adapter —
// loop.FromRegistry(reg).Specs(), loop/toolreg.go:30 — on the same n=8/64/512
// axis as [BenchmarkIndexWrap] and [BenchmarkIndexProject], so index mode's
// per-model-call cost can be compared against preload mode instead of
// assumed. See jedwards1230/agent-sdk-go#127.
//
// This is the "preload" half of that comparison: registryAdapter.Specs
// re-marshals every tool's tool.Schema with encoding/json on every call, with
// nothing cached between calls — unlike toolindex.Index, which snapshots once
// in Wrap and re-projects a slice on every Specs(). Specs() is called once per
// model call (loop/loop.go:276, req.Tools = r.cfg.Tools.Specs()), so this
// per-call cost multiplies by every turn of every session exactly the way
// BenchmarkIndexProject's does.
//
// # Anti-blindness
//
// This benchmark constructs no [toolindex.Index] anywhere in its path — it
// only ever touches loop.FromRegistry and the real *tool.Registry underneath
// it. That makes it the negative half of the paired mutation #127 calls for: a
// panic planted anywhere in the toolindex package (e.g. Index.Specs) is
// reachable from BenchmarkIndexWrap and BenchmarkIndexProject and NOT from
// this one. See the mutation record in the PR description for the observed
// pair (index benchmarks red, this one green, on a tree that still builds).
//
// # Warm-up is mandatory
//
// encoding/json builds a per-type encoder the FIRST time a process marshals
// that type, and registryAdapter.Specs marshals tool.Schema on every call — so
// without an explicit warm-up call before the measured loop, whichever
// benchmark in this package happens to run first (this one, or
// BenchmarkIndexWrap/Project via newBenchBase) pays that one-time cost and the
// other reads artificially cheap by comparison. b.ResetTimer alone does not
// fix this: the cache is a process-global side effect of the FIRST marshal,
// not something scoped to one benchmark's timer window. See newBenchBase's
// identical warmup in index_bench_test.go and docs/TESTING.md.
func BenchmarkRegistrySpecs(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			names := benchNames(n)
			reg := tool.NewRegistry(benchTools(names)...)
			if got := reg.Len(); got != n {
				b.Fatalf("bench registry has %d tools, want %d", got, n)
			}
			adapter := loop.FromRegistry(reg)

			// Warm-up: see the package doc above.
			sinkSpecs = adapter.Specs()

			b.ReportAllocs()
			for b.Loop() {
				sinkSpecs = adapter.Specs()
			}
		})
	}
}
