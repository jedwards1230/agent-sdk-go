package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// defaultMaxOutputBytes caps captured tool output (bash stdout+stderr, grep
// matches) before it is handed back to the model.
const defaultMaxOutputBytes = 30_000

// resolvePath resolves p against root: an absolute p is returned cleaned;
// otherwise p is joined onto root. resolvePath does not enforce workspace
// confinement — keeping a path inside root, if required, is a permission-layer
// concern (M3), not this function's job.
func resolvePath(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(root, p)
}

// decodeInput unmarshals raw into v, wrapping any error. A nil or empty raw
// is treated as "{}" so tools whose input is entirely optional accept no
// arguments.
func decodeInput(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("tool: invalid input: %w", err)
	}
	return nil
}

// truncateBytes caps s at max bytes, cutting on a UTF-8 rune boundary and
// appending a "truncated N bytes" marker when it does. A non-positive max is
// treated as "no cap" and s is returned unchanged.
func truncateBytes(s string, max int) (out string, truncated bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	dropped := len(s) - cut
	return s[:cut] + fmt.Sprintf("\n… [truncated %d bytes]", dropped), true
}

// matchGlob reports whether name matches pattern, treating both as "/"
// separated path segments. A "**" segment matches zero or more path
// segments; every other segment is matched against its counterpart with
// [filepath.Match]. It returns filepath.Match's error for a malformed
// pattern segment.
func matchGlob(pattern, name string) (bool, error) {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments is the recursive segment matcher behind [matchGlob].
func matchSegments(p, n []string) (bool, error) {
	if len(p) == 0 {
		return len(n) == 0, nil
	}
	if p[0] == "**" {
		ok, err := matchSegments(p[1:], n)
		if err != nil || ok {
			return ok, err
		}
		if len(n) == 0 {
			return false, nil
		}
		return matchSegments(p, n[1:])
	}
	if len(n) == 0 {
		return false, nil
	}
	matched, err := filepath.Match(p[0], n[0])
	if err != nil {
		return false, err
	}
	if !matched {
		return false, nil
	}
	return matchSegments(p[1:], n[1:])
}

// validateGlob reports a malformed glob pattern (e.g. an unterminated
// character class) without needing a name to match against. Each "/"-separated
// segment other than "**" is checked with [filepath.Match]; the first bad
// segment's error is returned.
func validateGlob(pattern string) error {
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := filepath.Match(seg, ""); err != nil {
			return err
		}
	}
	return nil
}

// ctxErr returns ctx.Err(), which is nil for a live context. Builtins call it
// at entry, and grep/glob call it inside their directory-walk loop, so a
// cancelled ctx aborts promptly rather than after the whole tree is walked.
func ctxErr(ctx context.Context) error {
	return ctx.Err()
}
