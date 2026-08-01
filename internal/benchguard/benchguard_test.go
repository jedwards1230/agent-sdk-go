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
			wantSubstr: "reached the target 0 times",
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
			wantSubstr: "reached the target 1 times",
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
			err := Check(tc.floor, tc.iterations, tc.counted)
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

// TestCheckBoundary walks the count across the floor one invocation at a time,
// so the exact transition point is pinned rather than inferred from two
// far-apart samples.
func TestCheckBoundary(t *testing.T) {
	f := Floor{Name: "boundary", PerIteration: 2}
	const iterations = 50
	const want = iterations * 2

	for counted := want - 3; counted <= want+3; counted++ {
		err := Check(f, iterations, int64(counted))
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
