// Package benchguard makes a benchmark prove it reached the code it claims to
// measure. A benchmark that stops exercising its target does not fail — it
// gets faster and allocates less, and an allocation gate reads that as an
// improvement. The guard closes that hole permanently: the fixture routes its
// real work through a [Counter], and [Assert] fails the benchmark when the
// recorded invocation count falls below a documented per-iteration floor.
//
// It lives under internal/ on purpose. This is a test-authoring aid with no
// place on the SDK's public API surface: embedders never call it, it adds no
// exported package to maintain, no construction-time option, and no behavior
// change for anyone who ignores it. It is still importable from every
// benchmark in this module, which is the reuse that matters.
//
// It is deliberately narrow. It counts invocations and compares against a
// floor; it does not time anything, does not sample allocations, does not
// wrap testing.B, and knows nothing about any particular package under test.
// The seed benchmark that ships alongside it (see index_bench_test.go) is one
// user, not the shape of the API — the fixture that does the counting is
// always the benchmark's own, because only the benchmark knows what "real
// work" means for its target.
//
// The policy is split out as a pure function, [Check], separate from the
// *testing.B plumbing in [Assert], so the policy itself is directly unit
// tested. A guard that can never fire is itself blind, so [Check] rejects a
// zero or negative floor rather than passing vacuously.
package benchguard

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// Counter records how many times a benchmark fixture routed real work through
// it. The zero value is ready to use, and it is safe for concurrent use — not
// because guarded benchmarks should be parallel (they must not be; see
// docs/TESTING.md), but so a fixture whose target itself fans out internally
// still counts correctly.
type Counter struct {
	n atomic.Int64
}

// Hit records one invocation and returns the new total. A fixture calls it on
// every path that reaches the measured target, and on no other path — a Hit
// on a path that does not reach the target is exactly the blindness the guard
// exists to prevent.
func (c *Counter) Hit() int64 { return c.n.Add(1) }

// Count reports the invocations recorded so far.
func (c *Counter) Count() int64 { return c.n.Load() }

// Reset returns the counter to zero. Use it between the setup phase and the
// measured loop when setup also reaches the target, so the floor is stated
// purely per measured iteration.
func (c *Counter) Reset() { c.n.Store(0) }

// Floor is the contract a guarded benchmark asserts: the target named by Name
// must be reached at least PerIteration times per measured iteration.
//
// PerIteration is a floor, not an equality, on purpose: a target that is
// reached more often than the floor is not a blindness failure, and pinning an
// exact count would turn every legitimate refactor of the fixture into a red
// benchmark. The floor must be positive — a floor of zero can never fire.
type Floor struct {
	// Name identifies the target in failure output, e.g.
	// "loop.ToolRegistry.Specs via toolindex.Index.Wrap".
	Name string
	// PerIteration is the minimum invocations each measured iteration must
	// contribute. Document at the call site why it is that number.
	PerIteration int64
}

// Check is the whole policy, as a pure function: it reports whether counted
// invocations over iterations measured iterations satisfy f.
//
// It is an error, not a pass, when:
//   - f.PerIteration is not positive (a floor that can never fire is blind),
//   - iterations is not positive (nothing was measured, so nothing was proven),
//   - counted is below iterations * f.PerIteration.
//
// The returned error names the target, the expected floor, and the observed
// count, because the whole point of firing is telling the reader which fixture
// stopped reaching which target.
func Check(f Floor, iterations, counted int64) error {
	if f.PerIteration <= 0 {
		return fmt.Errorf("floor %q: PerIteration = %d, must be positive (a zero floor can never fire)",
			f.Name, f.PerIteration)
	}
	if iterations <= 0 {
		return fmt.Errorf("floor %q: measured %d iterations, must be positive (nothing was measured)",
			f.Name, iterations)
	}
	want := iterations * f.PerIteration
	if counted < want {
		return fmt.Errorf("floor %q: fixture reached the target %d times over %d iterations, want >= %d (%d/iteration); the fixture is no longer exercising the code this benchmark claims to measure",
			f.Name, counted, iterations, want, f.PerIteration)
	}
	return nil
}

// Assert applies [Check] to c's count and fails b on violation. Call it after
// the measured loop, with the iteration count the loop actually ran:
//
//	var seen benchguard.Counter
//	base := newFixture(&seen)
//	var iters int64
//	b.ReportAllocs()
//	for b.Loop() {
//		sink = doWork(base)
//		iters++
//	}
//	benchguard.Assert(b, benchguard.Floor{Name: "…", PerIteration: 1}, iters, &seen)
//
// iters is counted by the benchmark rather than read from b.N so the assertion
// does not depend on how the loop was driven (b.Loop, an explicit b.N loop, or
// a sub-benchmark), and so a loop body that never ran is caught rather than
// silently satisfied.
//
// It uses b.Errorf, not b.Fatalf: the measured loop is already over, the
// reported allocation numbers are still emitted, and a failing benchmark
// marks the run red either way — while Fatal's runtime.Goexit from a
// benchmark goroutine is a needlessly blunt way to end a run whose numbers
// are worth reading alongside the failure.
func Assert(b *testing.B, f Floor, iterations int64, c *Counter) {
	b.Helper()
	assert(b, f, iterations, c)
}

// reporter is the subset of *testing.B that [Assert] uses. Declaring it lets
// the plumbing be unit tested against a recording fake: testing.TB cannot be
// implemented outside the testing package, and a guard whose reporting path is
// never exercised is one more thing that could silently never fire.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// assert is Assert's body, against the narrow reporter seam.
func assert(r reporter, f Floor, iterations int64, c *Counter) {
	r.Helper()
	if c == nil {
		r.Errorf("benchguard: floor %q: nil Counter", f.Name)
		return
	}
	if err := Check(f, iterations, c.Count()); err != nil {
		r.Errorf("benchguard: %v", err)
	}
}
