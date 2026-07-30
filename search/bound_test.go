package search_test

import (
	"strings"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/search"
)

func TestClampMaxResults(t *testing.T) {
	tests := []struct {
		name         string
		cfgDefault   int
		optsOverride int
		want         int
	}{
		{name: "both zero uses package default", cfgDefault: 0, optsOverride: 0, want: search.DefaultMaxResults},
		{name: "cfg default only", cfgDefault: 5, optsOverride: 0, want: 5},
		{name: "opts override wins over cfg", cfgDefault: 5, optsOverride: 3, want: 3},
		{name: "opts override wins even over a larger cfg", cfgDefault: 20, optsOverride: 3, want: 3},
		{name: "cfg default clamped to ceiling", cfgDefault: 999, optsOverride: 0, want: search.MaxResultsCeiling},
		{name: "opts override clamped to ceiling", cfgDefault: 5, optsOverride: 999, want: search.MaxResultsCeiling},
		{name: "negative cfg default falls back to package default", cfgDefault: -1, optsOverride: 0, want: search.DefaultMaxResults},
		{name: "negative opts override is ignored, cfg default used", cfgDefault: 7, optsOverride: -1, want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := search.ClampMaxResults(tt.cfgDefault, tt.optsOverride); got != tt.want {
				t.Errorf("ClampMaxResults(%d, %d) = %d, want %d", tt.cfgDefault, tt.optsOverride, got, tt.want)
			}
		})
	}
}

func TestTruncateSnippet(t *testing.T) {
	short := "a short description"
	if got := search.TruncateSnippet(short); got != short {
		t.Errorf("TruncateSnippet(short) = %q, want unchanged", got)
	}

	long := strings.Repeat("x", search.DefaultSnippetLimit+50)
	got := search.TruncateSnippet(long)
	gotRunes := []rune(got)
	// DefaultSnippetLimit runes of content plus the appended ellipsis.
	if len(gotRunes) != search.DefaultSnippetLimit+1 {
		t.Fatalf("TruncateSnippet(long) len = %d, want %d", len(gotRunes), search.DefaultSnippetLimit+1)
	}
	if gotRunes[len(gotRunes)-1] != '…' {
		t.Errorf("TruncateSnippet(long) does not end with an ellipsis marker: %q", got)
	}

	exact := strings.Repeat("y", search.DefaultSnippetLimit)
	if got := search.TruncateSnippet(exact); got != exact {
		t.Errorf("TruncateSnippet(exact-length) = %q, want unchanged (no truncation at the boundary)", got)
	}
}
