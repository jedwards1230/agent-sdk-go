package benchguard

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The guard's whole value is that it fires. These tests therefore spend most
// of their assertions on the firing cases: a guard proven only to stay quiet
// is indistinguishable from a guard that can never fire at all, which is the
// exact failure mode benchguard exists to prevent in benchmarks.

func TestCheck(t *testing.T) {
	tests := []struct {
		name       string
		floor      Floor
		iterations int64
		counted    int64
		digest     uint64
		wantErr    bool
		wantSubstr string
	}{
		{
			name:       "exactly at the floor is quiet",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 100,
			counted:    100,
		},
		{
			name:       "above the floor is quiet",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 100,
			counted:    101,
		},
		{
			name:       "far above the floor is quiet",
			floor:      Floor{Name: "target", PerIteration: 2},
			iterations: 100,
			counted:    100_000,
		},
		{
			name:       "one invocation short fires",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 100,
			counted:    99,
			wantErr:    true,
			wantSubstr: "want >= 100",
		},
		{
			name:       "a stub fixture reaching nothing fires",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 100,
			counted:    0,
			wantErr:    true,
			wantSubstr: "target callback reached 0 times",
		},
		{
			name:       "a multi-invocation floor fires on a single-invocation fixture",
			floor:      Floor{Name: "target", PerIteration: 3},
			iterations: 10,
			counted:    10,
			wantErr:    true,
			wantSubstr: "want >= 30",
		},
		{
			name:       "a setup-only fixture fires",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 100,
			counted:    1,
			wantErr:    true,
			wantSubstr: "target callback reached 1 times",
		},
		{
			name:       "a zero floor is rejected, never passed vacuously",
			floor:      Floor{Name: "target", PerIteration: 0},
			iterations: 100,
			counted:    100,
			wantErr:    true,
			wantSubstr: "must be positive",
		},
		{
			name:       "a negative floor is rejected",
			floor:      Floor{Name: "target", PerIteration: -1},
			iterations: 100,
			counted:    100,
			wantErr:    true,
			wantSubstr: "must be positive",
		},
		{
			name:       "zero iterations is rejected, never passed vacuously",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 0,
			counted:    0,
			wantErr:    true,
			wantSubstr: "nothing was measured",
		},
		{
			name:       "zero iterations is rejected even with a large count",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: 0,
			counted:    9999,
			wantErr:    true,
			wantSubstr: "nothing was measured",
		},
		{
			name:       "negative iterations is rejected",
			floor:      Floor{Name: "target", PerIteration: 1},
			iterations: -5,
			counted:    100,
			wantErr:    true,
			wantSubstr: "nothing was measured",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.floor, tc.iterations, Observation{Count: tc.counted, Digest: tc.digest})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Check(%+v, %d, %d) = nil, want an error",
						tc.floor, tc.iterations, tc.counted)
				}
				if !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Errorf("Check error = %q, want it to contain %q", err, tc.wantSubstr)
				}
				if !strings.Contains(err.Error(), tc.floor.Name) {
					t.Errorf("Check error = %q, want it to name the target %q", err, tc.floor.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("Check(%+v, %d, %d) = %v, want nil",
					tc.floor, tc.iterations, tc.counted, err)
			}
		})
	}
}

// TestCheckDigest covers the witness half: a count alone can be produced by a
// loop that calls Hit and nothing else, so the digest has to fire when the
// target was reached often enough but handed different data.
func TestCheckDigest(t *testing.T) {
	names := []string{"read", "write", "mcp__github__issue_list"}
	perIter := Digest(names...)
	const iterations = 100

	base := Floor{Name: "resident", PerIteration: int64(len(names)), Digest: perIter}

	t.Run("matching digest is quiet", func(t *testing.T) {
		obs := Observation{Count: iterations * int64(len(names)), Digest: perIter * iterations}
		if err := Check(base, iterations, obs); err != nil {
			t.Fatalf("Check = %v, want nil", err)
		}
	})

	t.Run("right count, wrong tokens fires", func(t *testing.T) {
		// The classic fake: a loop that hits the counter the right number of
		// times without the target ever assembling the argument stream.
		obs := Observation{Count: iterations * int64(len(names)), Digest: Digest("x", "y", "z") * iterations}
		err := Check(base, iterations, obs)
		if err == nil {
			t.Fatal("Check = nil, want a digest mismatch error")
		}
		if !strings.Contains(err.Error(), "argument digest") {
			t.Errorf("Check error = %q, want it to mention the argument digest", err)
		}
	})

	t.Run("right count, no digest at all fires", func(t *testing.T) {
		obs := Observation{Count: iterations * int64(len(names))}
		if err := Check(base, iterations, obs); err == nil {
			t.Fatal("Check = nil, want an error: a plain Hit loop records no digest")
		}
	})

	t.Run("a subset of the tokens fires", func(t *testing.T) {
		// Indexing fewer tools than claimed: count could still be padded, but
		// the digest cannot be.
		obs := Observation{
			Count:  iterations * int64(len(names)),
			Digest: Digest(names[:2]...) * iterations,
		}
		if err := Check(base, iterations, obs); err == nil {
			t.Fatal("Check = nil, want an error on a partial token set")
		}
	})

	t.Run("zero Digest means not asserted", func(t *testing.T) {
		f := Floor{Name: "resident", PerIteration: int64(len(names))}
		obs := Observation{Count: iterations * int64(len(names)), Digest: 12345}
		if err := Check(f, iterations, obs); err != nil {
			t.Fatalf("Check = %v, want nil when Floor.Digest is unset", err)
		}
	})
}

// TestDigestFold pins the properties the witness relies on: order independence
// (the target's iteration order is not part of the contract) and no cancelling
// on repeats (an XOR fold would silently zero a duplicated token).
func TestDigestFold(t *testing.T) {
	if Digest("a", "b", "c") != Digest("c", "a", "b") {
		t.Error("Digest is order dependent; it must not be")
	}
	if Digest("a", "a") == 0 {
		t.Error("Digest cancels a repeated token; duplicates must accumulate")
	}
	if Digest("a", "a") == Digest("a") {
		t.Error("Digest ignores a repeated token")
	}
	if Digest() != 0 {
		t.Error("Digest of nothing must be 0, the not-asserted sentinel")
	}
	if Digest("read") == Digest("reads") {
		t.Error("Digest collides on a one-character difference")
	}

	// Position sensitivity. Everything above still holds if fold() degrades to
	// a plain XOR of the bytes — order-independence, duplicate accumulation,
	// and read/reads (different lengths) all survive that. Only an anagram
	// distinguishes a real hash from an XOR, because XOR is commutative over
	// the bytes WITHIN a token as well as across tokens.
	//
	// This is not academic for this suite: under an XOR fold,
	// Digest("mcp__github__tool_0001") == Digest("mcp__github__tool_0010"),
	// and that is the exact name shape benchNames generates. A digest that
	// cannot tell two indexed tools apart cannot witness which tools the
	// target walked, which is the only thing it is there to do.
	if Digest("ab") == Digest("ba") {
		t.Error("Digest is position insensitive within a token; an anagram must not collide")
	}
	if Digest("mcp__github__tool_0001") == Digest("mcp__github__tool_0010") {
		t.Error("Digest collides on two names benchNames actually generates")
	}
}

// TestCounterHitWith proves the Counter side agrees with the standalone Digest
// helper — if they ever disagreed, every digest assertion would fire forever
// and get deleted as noise.
func TestCounterHitWith(t *testing.T) {
	names := []string{"read", "write", "edit"}

	var c Counter
	for range 3 {
		for _, n := range names {
			c.HitWith(n)
		}
	}

	got := c.Observe()
	if want := int64(9); got.Count != want {
		t.Errorf("Count = %d, want %d", got.Count, want)
	}
	if want := Digest(names...) * 3; got.Digest != want {
		t.Errorf("Digest = %#x, want %#x", got.Digest, want)
	}

	c.Reset()
	if after := c.Observe(); after.Count != 0 || after.Digest != 0 {
		t.Errorf("after Reset = %+v, want a zero Observation", after)
	}
}

// TestHitWithDoesNotAllocate is load-bearing, not hygiene: the guard is hooked
// into a closure that runs inside a measured loop, so if folding allocated it
// would move the very numbers scripts/bench.sh gates on.
func TestHitWithDoesNotAllocate(t *testing.T) {
	var c Counter
	names := benchmarkTokens()

	got := testing.AllocsPerRun(100, func() {
		for _, n := range names {
			c.HitWith(n)
		}
	})
	if got != 0 {
		t.Errorf("HitWith allocated %.1f times per run, want 0", got)
	}
}

func benchmarkTokens() []string {
	return []string{"read", "write", "mcp__github__issue_list", "mcp__postgres__query"}
}

// TestCheckBoundary walks the count across the floor one invocation at a time,
// so the exact transition point is pinned rather than inferred from two
// far-apart samples.
func TestCheckBoundary(t *testing.T) {
	f := Floor{Name: "boundary", PerIteration: 2}
	const iterations = 50
	const want = iterations * 2

	for counted := want - 3; counted <= want+3; counted++ {
		err := Check(f, iterations, Observation{Count: int64(counted)})
		wantErr := counted < want
		if wantErr != (err != nil) {
			t.Errorf("Check(counted=%d) err = %v, wantErr = %v", counted, err, wantErr)
		}
	}
}

// recorder is a reporter that captures what the guard would have reported to
// *testing.B, so the plumbing half is exercised without failing this test.
type recorder struct {
	helpers int
	msgs    []string
}

func (r *recorder) Helper() { r.helpers++ }

func (r *recorder) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func TestAssertPlumbing(t *testing.T) {
	tests := []struct {
		name       string
		floor      Floor
		iterations int64
		counted    int64
		nilCounter bool
		wantMsgs   int
		wantSubstr string
	}{
		{
			name:       "at the floor reports nothing",
			floor:      Floor{Name: "specs", PerIteration: 1},
			iterations: 100,
			counted:    100,
		},
		{
			name:       "below the floor reports once",
			floor:      Floor{Name: "specs", PerIteration: 1},
			iterations: 100,
			counted:    3,
			wantMsgs:   1,
			wantSubstr: "benchguard: floor \"specs\"",
		},
		{
			name:       "a nil Counter reports rather than panicking",
			floor:      Floor{Name: "specs", PerIteration: 1},
			iterations: 100,
			nilCounter: true,
			wantMsgs:   1,
			wantSubstr: "nil Counter",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var c *Counter
			if !tc.nilCounter {
				c = &Counter{}
				for range tc.counted {
					c.Hit()
				}
			}
			var r recorder
			assert(&r, tc.floor, tc.iterations, c)

			if len(r.msgs) != tc.wantMsgs {
				t.Fatalf("assert reported %d messages (%v), want %d", len(r.msgs), r.msgs, tc.wantMsgs)
			}
			if tc.wantMsgs > 0 && !strings.Contains(r.msgs[0], tc.wantSubstr) {
				t.Errorf("assert message = %q, want it to contain %q", r.msgs[0], tc.wantSubstr)
			}
			if r.helpers == 0 {
				t.Error("assert did not mark itself a test helper; failures would point at the guard, not the benchmark")
			}
		})
	}
}

func TestCounter(t *testing.T) {
	var c Counter
	if got := c.Count(); got != 0 {
		t.Fatalf("zero-value Counter.Count() = %d, want 0", got)
	}
	if got := c.Hit(); got != 1 {
		t.Errorf("first Hit() = %d, want 1", got)
	}
	if got := c.Hit(); got != 2 {
		t.Errorf("second Hit() = %d, want 2", got)
	}
	if got := c.Count(); got != 2 {
		t.Errorf("Count() after 2 hits = %d, want 2", got)
	}
	c.Reset()
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after Reset() = %d, want 0", got)
	}
	if got := c.Hit(); got != 1 {
		t.Errorf("Hit() after Reset() = %d, want 1", got)
	}
}

// TestCounterConcurrent pins the documented concurrency safety: a target that
// fans out internally must still be counted exactly, with no lost increments.
// It is the reason Counter holds an atomic rather than a plain int64.
func TestCounterConcurrent(t *testing.T) {
	const goroutines, perGoroutine = 8, 500

	var c Counter
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				c.Hit()
			}
		}()
	}
	wg.Wait()

	if got, want := c.Count(), int64(goroutines*perGoroutine); got != want {
		t.Errorf("Count() = %d, want %d", got, want)
	}
}
