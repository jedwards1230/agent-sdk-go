// Package spill streams a tool call's raw output to a durable, append-only
// per-call file while retaining only a bounded head+tail excerpt (plus a running
// sha256 and byte count) in memory. It is the "bulk-payload spill" seam
// (docs/DESIGN.md): tool output is bulk ground truth that belongs on disk, not in
// the event stream, so tool.call.finished carries a reference (path, bytes,
// sha256) and a small excerpt instead of an unbounded payload.
//
// The package is a stdlib-only leaf with no dependency on the rest of the SDK.
// A [Writer] is an [io.Writer]: a streaming tool (bash) points its process
// stdout/stderr straight at one, so no code path ever buffers the full output in
// memory. The loop hands a per-call writer to a tool through the context
// ([NewContext]/[FromContext]); a tool that does not stream simply returns its
// (bounded) content, which the loop writes through the same writer.
package spill

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Excerpt window sizes: the head and tail byte budgets retained in memory for
// the preview carried on tool.call.finished. They bound memory regardless of how
// much a tool emits; the full output lives only in the spill file. Tunable.
const (
	headBytes = 2 << 10 // 2 KiB
	tailBytes = 2 << 10 // 2 KiB

	// spillBufSize is the write-buffer size for the append-only file. A larger
	// buffer keeps multi-megabyte streaming to a handful of syscalls.
	spillBufSize = 64 << 10 // 64 KiB
)

// ErrClosed is returned by [Writer.Write] after the writer has been closed.
var ErrClosed = errors.New("spill: write after close")

// Ref is the settled reference to a spilled tool output, returned by
// [Writer.Close]. Path is relative to the session store root (empty for a
// file-less writer); Bytes and SHA256 describe the full on-disk content; Excerpt
// is the bounded head+tail preview.
type Ref struct {
	// Path is the spill file relative to the store root (forward-slashed), or
	// empty when the writer is file-less. It is deliberately root-relative so the
	// serialized event stays portable (no host path leaks). This is NOT the same
	// string the excerpt's elision marker names: the marker uses the ABSOLUTE
	// path so the read tool can resolve it from any working directory (see
	// [Writer]'s excerpt). The divergence is intentional — the structured field
	// is for portability, the marker is for the model to act on.
	Path string
	// Bytes is the total number of bytes written.
	Bytes int64
	// SHA256 is the hex-encoded sha256 of the full content.
	SHA256 string
	// Excerpt is the bounded head+tail preview (see [Writer]). When the content
	// exceeds the head+tail budget the middle is elided with a marker; otherwise
	// it is the whole content.
	Excerpt string
	// Elided reports whether the excerpt omitted any bytes (content exceeded the
	// head+tail budget).
	Elided bool
}

// Writer streams bytes to an append-only spill file (when file-backed) while
// maintaining a running sha256, a byte count, and bounded head/tail buffers for
// the excerpt. It is not safe for concurrent use; callers serialize writes (for
// bash, os/exec guarantees a single goroutine writes when Stdout==Stderr).
type Writer struct {
	relPath string // recorded in Ref.Path (store-root-relative); empty ⇒ file-less
	absPath string // absolute file path; named in the excerpt's elision marker
	f       *os.File
	bw      *bufio.Writer
	hash    hash.Hash
	n       int64
	head    []byte
	tail    *ring
	closed  bool
	ref     Ref
}

// Create opens a per-call spill writer. When dir is empty the writer is
// file-less: it still computes the excerpt, sha256, and byte count in memory but
// writes no file and reports an empty [Ref.Path] — used when no session store is
// configured, so a tool still streams into a bounded sink and never buffers its
// full output. Otherwise Create makes dir (mode 0o700, lazily) and opens an
// append-only file dir/<callID>.log; relDir (dir relative to the store root) is
// recorded so the returned [Ref.Path] is a portable, root-relative path.
func Create(dir, relDir, callID string) (*Writer, error) {
	w := &Writer{hash: sha256.New(), tail: newRing(tailBytes)}
	if dir == "" {
		return w, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("spill: create dir %s: %w", dir, err)
	}
	name := safeName(callID) + ".log"
	abs := filepath.Join(dir, name)
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("spill: open %s: %w", abs, err)
	}
	w.f = f
	w.bw = bufio.NewWriterSize(f, spillBufSize)
	w.relPath = path.Join(filepath.ToSlash(relDir), name)
	w.absPath = abs
	return w, nil
}

// Write appends p to the spill file (when file-backed) and folds it into the
// running hash, byte count, and head/tail buffers. It counts and hashes only the
// bytes actually accepted into the file, so a partial write leaves the reference
// consistent with what is durably on disk.
func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, ErrClosed
	}
	if w.bw == nil {
		w.record(p)
		return len(p), nil
	}
	n, err := w.bw.Write(p)
	if n > 0 {
		w.record(p[:n])
	}
	return n, err
}

// record folds accepted bytes into the hash, count, and head/tail buffers.
func (w *Writer) record(p []byte) {
	w.hash.Write(p) // hash.Hash.Write never errors
	w.n += int64(len(p))
	if len(w.head) < headBytes {
		take := headBytes - len(w.head)
		if take > len(p) {
			take = len(p)
		}
		w.head = append(w.head, p[:take]...)
	}
	w.tail.Write(p)
}

// Close flushes and fsyncs the spill file (never truncating), then returns the
// settled [Ref]. It is idempotent: a second call returns the same reference.
func (w *Writer) Close() (Ref, error) {
	if w.closed {
		return w.ref, nil
	}
	w.closed = true

	var errs []error
	if w.bw != nil {
		if err := w.bw.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	if w.f != nil {
		if err := w.f.Sync(); err != nil {
			errs = append(errs, err)
		}
		if err := w.f.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	excerpt, elided := w.excerpt()
	w.ref = Ref{
		Path:    w.relPath,
		Bytes:   w.n,
		SHA256:  hex.EncodeToString(w.hash.Sum(nil)),
		Excerpt: excerpt,
		Elided:  elided,
	}
	return w.ref, errors.Join(errs...)
}

// excerpt builds the bounded preview from the head and tail buffers: the whole
// content when it fits the head+tail budget, otherwise head + an elision marker
// noting the omitted byte count + tail.
func (w *Writer) excerpt() (string, bool) {
	tail := w.tail.Bytes()
	if w.n <= int64(headBytes+tailBytes) {
		if w.n <= int64(headBytes) {
			return string(w.head), false // head holds all n bytes
		}
		// head holds the first headBytes; bytes [headBytes, n) are the last
		// (n-headBytes) bytes, i.e. the suffix of the tail buffer.
		need := int(w.n) - headBytes
		return string(w.head) + string(tail[len(tail)-need:]), false
	}
	omitted := w.n - int64(headBytes) - int64(tailBytes)
	var marker string
	if w.absPath != "" {
		// Name the ABSOLUTE spill path so the model can read the full output back
		// with the read tool from ANY working directory — the read tool resolves
		// a relative path against its cwd, which need not match the store root the
		// (portable, root-relative) Ref.Path is measured from. The structured
		// event field stays relative; only this human/model-facing marker is
		// absolute. See Ref.Path.
		marker = fmt.Sprintf("\n… [%d bytes elided — full output at %s] …\n", omitted, w.absPath)
	} else {
		marker = fmt.Sprintf("\n… [%d bytes elided] …\n", omitted)
	}
	return string(w.head) + marker + string(tail), true
}

// safeName reduces a tool call id to a safe single-path-component filename,
// replacing path separators and parent refs so a hostile id cannot escape dir.
func safeName(callID string) string {
	if callID == "" {
		return "call"
	}
	r := strings.NewReplacer("/", "_", `\`, "_", "..", "_", "\x00", "_")
	return r.Replace(callID)
}

// --- context seam ---

type ctxKey struct{}

// NewContext returns a copy of ctx carrying w as the per-call output sink. A
// streaming tool retrieves it with [FromContext] and points its process output
// at it; other tools ignore it.
func NewContext(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, ctxKey{}, w)
}

// FromContext returns the output sink carried by ctx, if any.
func FromContext(ctx context.Context) (io.Writer, bool) {
	w, ok := ctx.Value(ctxKey{}).(io.Writer)
	return w, ok
}

// --- bounded tail ring ---

// ring retains the last cap bytes written to it in O(cap) memory.
type ring struct {
	buf  []byte
	w    int
	full bool
}

func newRing(capacity int) *ring { return &ring{buf: make([]byte, capacity)} }

// Write records p, keeping only the last cap bytes seen across all writes.
func (r *ring) Write(p []byte) {
	if len(r.buf) == 0 {
		return
	}
	if len(p) >= len(r.buf) {
		copy(r.buf, p[len(p)-len(r.buf):])
		r.w, r.full = 0, true
		return
	}
	n := copy(r.buf[r.w:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.full = true
	}
	r.w += len(p)
	if r.w >= len(r.buf) {
		r.w -= len(r.buf)
		r.full = true
	}
}

// Bytes returns the retained bytes in write order (length ≤ cap).
func (r *ring) Bytes() []byte {
	if !r.full {
		out := make([]byte, r.w)
		copy(out, r.buf[:r.w])
		return out
	}
	out := make([]byte, len(r.buf))
	n := copy(out, r.buf[r.w:])
	copy(out[n:], r.buf[:r.w])
	return out
}
