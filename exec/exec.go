// Package exec is the SDK's headless one-shot adapter: it drives a drivable
// session to completion with a single prompt and emits the typed event stream
// as JSONL on an io.Writer (os.Stdout by default) — the SDK half of an
// application's `exec` verb. Transport-agnostic (any io.Writer) and
// app-agnostic (consumes only the event contract). Optionally validates the
// run's final text result against a JSON-schema subset (see schema.go); a
// mismatch is reported through the Go return value, never as a new event kind.
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// Session is the minimal drivable-session surface the adapter drives. Both
// *session.Session and *runner.Runner satisfy it. The adapter expects a
// freshly-created (un-prompted) session — the one-shot contract.
type Session interface {
	ID() string
	Events() *event.Subscription
	Prompt(ctx context.Context, text string) error
}

// Options configures a Run. The zero value is valid (emits to os.Stdout, no
// schema validation).
type Options struct {
	// Out is the JSONL sink; nil defaults to os.Stdout.
	Out io.Writer
	// OutputSchema, when non-empty, is a JSON-schema-subset document (see
	// schema.go) the run's final text result is validated against. A mismatch
	// makes Run return a *SchemaError (with the Result still populated).
	OutputSchema []byte
}

// Result summarizes a completed one-shot run.
type Result struct {
	SessionID  string // the driven session's id
	Final      string // content of the last message.finished with kind "text"
	StopReason string // stop_reason from the terminal turn.finished
	Events     int    // number of events emitted to Out
}

// collected is the drainer goroutine's handoff to the calling goroutine.
type collected struct {
	final     string
	stop      string
	count     int
	encodeErr error
}

// Run drives sess with exactly one prompt, streaming every emitted event to
// opts.Out as JSONL (one compact JSON object per line, in seq order) until the
// terminal turn.finished, then (if OutputSchema is set) validates the final
// text result. It returns the run summary; a schema mismatch returns a
// *SchemaError with Result still populated. A provider/prompt error is returned
// after the stream has been drained.
func Run(ctx context.Context, sess Session, prompt string, opts Options) (Result, error) {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}

	var sch *schema
	if len(opts.OutputSchema) > 0 {
		var err error
		sch, err = compileSchema(opts.OutputSchema)
		if err != nil {
			return Result{}, fmt.Errorf("exec: compile output schema: %w", err)
		}
	}

	// Subscribe before prompting so the retained session.created (replayed by
	// FilterAll) is included and no live event is missed.
	sub := sess.Events()
	defer sub.Close()

	// Drain in a separate goroutine: the session publishes must-deliver events
	// synchronously and a full subscriber buffer blocks the publisher, so a
	// concurrent drainer is required to avoid deadlock. The drainer is the sole
	// writer of out and the collected value; the caller reads it only after
	// <-done (happens-after), keeping Run race-clean.
	done := make(chan collected, 1)
	go func() {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false) // match goldenio: don't HTML-escape output
		var c collected
		for ev := range sub.C {
			if err := enc.Encode(ev); err != nil && c.encodeErr == nil {
				c.encodeErr = err
			}
			c.count++
			switch e := ev.(type) {
			case event.MessageFinished:
				if e.MessageKind == event.MessageText {
					c.final = e.Content
				}
			case event.TurnFinished:
				c.stop = e.StopReason
				done <- c // terminal event: exactly one per one-shot run
				return
			}
		}
		done <- c // channel closed without a terminal event
	}()

	// Prompt always emits a terminal turn.finished (even on provider error or
	// cancellation), so the drainer completes promptly — no timeout needed.
	promptErr := sess.Prompt(ctx, prompt)
	c := <-done

	res := Result{SessionID: sess.ID(), Final: c.final, StopReason: c.stop, Events: c.count}
	switch {
	case c.encodeErr != nil:
		return res, fmt.Errorf("exec: encode event: %w", c.encodeErr)
	case promptErr != nil:
		return res, promptErr
	case sch != nil:
		if err := sch.validate([]byte(c.final)); err != nil {
			return res, err
		}
	}
	return res, nil
}
