package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// maxToolName is the tool-name length ceiling providers enforce: 64 bytes of
// [A-Za-z0-9_-]. A name over the limit is a provider 400 that kills the
// entire request, not just the offending tool — see docs/DESIGN.md
// "MCP (M7)".
const maxToolName = 64

// truncatedPrefixLen is how much of the sanitized name survives truncation:
// 64 total, minus 1 separator byte, minus 6 hex digits of disambiguating
// hash.
const truncatedPrefixLen = maxToolName - 1 - 6

// qualifiedToolName builds the permission-grammar-facing name for a tool
// projected from server's tool named tool: "mcp__<server>__<tool>" (see
// docs/DESIGN.md's permission rule grammar, `mcp__search__*(*)`). Both
// components are sanitized independently (so a "__" inside either component
// can never be confused with the two structural separators), then the whole
// name is truncated with a content hash if it still exceeds maxToolName.
//
// Truncation is deterministic and collision-resistant, not merely
// best-effort: it hashes the FULL sanitized name (every byte, not just the
// surviving prefix), so two names that agree on their first 57 bytes but
// differ only past the cut still truncate to distinct results.
func qualifiedToolName(server, tool string) string {
	full := "mcp__" + sanitizeComponent(server) + "__" + sanitizeComponent(tool)
	if len(full) <= maxToolName {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := hex.EncodeToString(sum[:])[:6]
	return full[:truncatedPrefixLen] + "_" + suffix
}

// sanitizeComponent replaces every rune outside [A-Za-z0-9_-] with '_'. The
// result is pure ASCII, so a later byte-length truncation of the qualified
// name can never split a multi-byte rune.
func sanitizeComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isNameRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		return true
	default:
		return false
	}
}
