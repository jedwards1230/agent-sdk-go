package provider

import "io"

// SliceStream returns a [StreamHandle] that yields the given events in order,
// then io.EOF. It is the canonical fake stream for loop unit tests and any code
// that needs to replay a fixed event sequence. Close is a no-op.
func SliceStream(events ...StreamEvent) StreamHandle {
	return &sliceStream{events: events}
}

type sliceStream struct {
	events []StreamEvent
	i      int
}

// Next returns the next event, or io.EOF when the slice is exhausted.
func (s *sliceStream) Next() (StreamEvent, error) {
	if s.i >= len(s.events) {
		return StreamEvent{}, io.EOF
	}
	e := s.events[s.i]
	s.i++
	return e, nil
}

// Close is a no-op; a slice stream holds no resources.
func (s *sliceStream) Close() error { return nil }
