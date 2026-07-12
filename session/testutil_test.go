package session_test

import (
	"fmt"
	"sync/atomic"
	"time"
)

// newCounterIDGen returns a deterministic, concurrency-safe id generator
// producing "<prefix>-000001", "<prefix>-000002", ... Tests use it in place
// of the default UUIDv7 generator so assertions can pin exact ids.
func newCounterIDGen(prefix string) func() string {
	var n atomic.Int64
	return func() string {
		return fmt.Sprintf("%s-%06d", prefix, n.Add(1))
	}
}

// newStepClock returns a clock that advances by step on every call, starting
// at start. Useful where entries must have distinct, monotonic timestamps.
func newStepClock(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		t := cur
		cur = cur.Add(step)
		return t
	}
}
