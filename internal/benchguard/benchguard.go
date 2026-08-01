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

// Counter records how many times a callback the measured target invokes was
// reached, and a digest of the arguments it was handed. The zero value is ready
// to use, and it is safe for concurrent use — not because guarded benchmarks
// should be parallel (they must not be; see docs/TESTING.md), but so a target
// that fans out internally still records correctly.
//
// # Where to hook it, and why it matters
//
// Hook a Counter into a callback the TARGET owns the calling of — a
// construction-time option closure, a decorator the target must route through
// to do its own work — not into a method the benchmark body can call itself.
// The distinction is the whole guard:
//
//	// Weak: the benchmark holds fixture and can call fixture.Specs() directly,
//	// so the count survives deleting the target from the loop entirely.
//	sink = fixture.Specs()
//
//	// Strong: only toolindex.Index.Wrap ever calls Options.Resident, once per
//	// indexed entry, with names that could only have come from base.Specs().
//	toolindex.New(toolindex.Options{Resident: func(n string) bool { c.HitWith(n); … }})
//
// See [Check] for the limits of what this can prove.
type Counter struct {
	n      atomic.Int64
	digest atomic.Uint64
}

// Hit records one invocation and returns the new total.
func (c *Counter) Hit() int64 { return c.n.Add(1) }

// HitWith records one invocation and folds token into the digest, so the
// assertion can pin WHAT the target passed in and not merely how often it
// called. Folding is addition of per-token FNV-1a hashes: commutative, so it
// does not depend on the target's iteration order, and accumulating, so a
// repeated token is not silently cancelled the way an XOR would cancel it.
//
// It allocates nothing, so hooking it into a callback inside a measured loop
// does not perturb the allocation numbers the gate reads.
func (c *Counter) HitWith(token string) int64 {
	c.digest.Add(fold(token))
	return c.n.Add(1)
}

// Count reports the invocations recorded so far.
func (c *Counter) Count() int64 { return c.n.Load() }

// Observe snapshots the counter as a value [Check] can be applied to.
func (c *Counter) Observe() Observation {
	return Observation{Count: c.n.Load(), Digest: c.digest.Load()}
}

// Reset returns the counter to zero, digest included. Use it between the setup
// phase and the measured loop when setup also reaches the target, so the floor
// is stated purely per measured iteration.
func (c *Counter) Reset() {
	c.n.Store(0)
	c.digest.Store(0)
}

// Observation is what a [Counter] recorded: the plain invocation count and the
// folded digest of every token passed to [Counter.HitWith].
type Observation struct {
	Count  int64
	Digest uint64
}

// Digest folds tokens the same way [Counter.HitWith] does, so a benchmark can
// compute the value it expects the target to have been handed:
//
//	want := benchguard.Digest(namesTheIndexMustSee...)
func Digest(tokens ...string) uint64 {
	var sum uint64
	for _, t := range tokens {
		sum += fold(t)
	}
	return sum
}

// FNV-1a 64-bit. Written out rather than using hash/fnv because that returns an
// interface whose Write escapes, which would allocate inside a measured loop.
const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

func fold(token string) uint64 {
	h := fnvOffset64
	for i := 0; i < len(token); i++ {
		h ^= uint64(token[i])
		h *= fnvPrime64
	}
	return h
}

// Floor is the contract a guarded benchmark asserts: the target named by Name
// must be reached at least PerIteration times per measured iteration, and —
// when Digest is set — must have been handed exactly the expected tokens.
//
// PerIteration is a floor, not an equality, on purpose: a target that is
// reached more often than the floor is not a blindness failure, and pinning an
// exact count would turn every legitimate refactor of the fixture into a red
// benchmark. The floor must be positive — a floor of zero can never fire.
type Floor struct {
	// Name identifies the target in failure output, e.g.
	// "toolindex.Options.Resident via Index.Wrap".
	Name string
	// PerIteration is the minimum invocations each measured iteration must
	// contribute. Document at the call site why it is that number.
	PerIteration int64
	// Digest, when non-zero, is the [Digest] of the tokens the target must
	// have passed to [Counter.HitWith] over ONE iteration. It upgrades the
	// assertion from "something called this N times" to "the target walked
	// exactly this data" — a count alone can be produced by a loop that calls
	// Hit and nothing else, where a digest match additionally requires
	// reproducing the target's whole argument stream.
	//
	// Zero means "not asserted", so a Counter used with plain Hit still works.
	Digest uint64
}

// Check is the whole policy, as a pure function: it reports whether obs, over
// iterations measured iterations, satisfies f.
//
// It is an error, not a pass, when:
//   - f.PerIteration is not positive (a floor that can never fire is blind),
//   - iterations is not positive (nothing was measured, so nothing was proven),
//   - obs.Count is below iterations * f.PerIteration,
//   - f.Digest is set and obs.Digest is not iterations * f.Digest.
//
// # What this proves, and what it cannot
//
// The guard is a permanent detector of DRIFT — a fixture or a measured loop
// that quietly stops doing the work it claims — not a sandbox against a
// benchmark author who sets out to write a lie. Nothing in Go can make a
// counter unreachable from the file that owns it, and any claim otherwise is
// the same overconfidence this package exists to correct.
//
// What hooking a target-owned callback buys is that both the count and the
// argument digest are produced INSIDE the target, from data only the target
// could have assembled. Faking it stops being a one-line slip during a
// refactor and becomes a deliberate reconstruction of the target's argument
// stream — visible in review as exactly that. The numeric gate in
// scripts/bench.sh is the second, independent line: it fails on an outsized
// drop as well as a rise, so a benchmark that goes blind and gets cheaper is
// caught by the numbers even where a guard could be bypassed.
func Check(f Floor, iterations int64, obs Observation) error {
	if f.PerIteration <= 0 {
		return fmt.Errorf("floor %q: PerIteration = %d, must be positive (a zero floor can never fire)",
			f.Name, f.PerIteration)
	}
	if iterations <= 0 {
		return fmt.Errorf("floor %q: measured %d iterations, must be positive (nothing was measured)",
			f.Name, iterations)
	}
	want := iterations * f.PerIteration
	if obs.Count < want {
		return fmt.Errorf("floor %q: target callback reached %d times over %d iterations, want >= %d (%d/iteration); the measured loop is no longer exercising the code this benchmark claims to measure",
			f.Name, obs.Count, iterations, want, f.PerIteration)
	}
	if f.Digest != 0 {
		// Every iteration must walk the same token set, so the expected total
		// scales with the iteration count.
		wantDigest := f.Digest * uint64(iterations)
		if obs.Digest != wantDigest {
			return fmt.Errorf("floor %q: argument digest is %#x over %d iterations, want %#x; the target was reached often enough but was handed different data than this benchmark claims to feed it",
				f.Name, obs.Digest, iterations, wantDigest)
		}
	}
	return nil
}

// Assert applies [Check] to c's observation and fails b on violation. Call it
// after the measured loop, with the iteration count the loop actually ran:
//
//	var seen benchguard.Counter
//	target := newTarget(&seen) // seen is hooked into a TARGET-owned callback
//	var iters int64
//	b.ReportAllocs()
//	for b.Loop() {
//		sink = doWork(target)
//		iters++
//	}
//	benchguard.Assert(b, benchguard.Floor{
//		Name: "…", PerIteration: 1, Digest: benchguard.Digest(wantTokens...),
//	}, iters, &seen)
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
	if err := Check(f, iterations, c.Observe()); err != nil {
		r.Errorf("benchguard: %v", err)
	}
}
