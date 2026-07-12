package compose_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/agent-sdk-go/compose"
	"github.com/jedwards1230/agent-sdk-go/internal/goldenio"
	"github.com/jedwards1230/agent-sdk-go/session"
)

func goldenClock() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }

// TestLoadStreamsFauxGolden is the M0 proof: compose.Load returns a session
// that streams the faux provider, verified against a golden JSONL file.
func TestLoadStreamsFauxGolden(t *testing.T) {
	ctx := context.Background()
	sess, err := compose.Load(ctx, filepath.Join("testdata", "agent.yaml"),
		session.WithIDGen(func() string { return "0192a1b2-c3d4-7e5f-8a90-000000000001" }),
		session.WithClock(goldenClock),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := goldenio.Collect(ctx, sess, "hello")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	goldenio.Assert(t, filepath.Join("testdata", "session.golden.jsonl"), got)
}

// TestParse covers manifest validation and the unknown-provider error.
func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring; empty means success
	}{
		{
			name: "valid",
			yaml: "name: demo\nprovider:\n  type: faux\n",
		},
		{
			name:    "missing name",
			yaml:    "provider:\n  type: faux\n",
			wantErr: "name is required",
		},
		{
			name:    "missing provider type",
			yaml:    "name: demo\nprovider: {}\n",
			wantErr: "provider.type is required",
		},
		{
			name:    "malformed yaml",
			yaml:    "name: [unterminated\n",
			wantErr: "parse manifest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compose.Parse([]byte(tt.yaml))
			assertErr(t, err, tt.wantErr)
		})
	}
}

// TestBuildUnsupportedProvider asserts an unknown provider type errors and lists
// the supported types.
func TestBuildUnsupportedProvider(t *testing.T) {
	m, err := compose.Parse([]byte("name: demo\nprovider:\n  type: openai\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, err = compose.Build(context.Background(), m)
	assertErr(t, err, "unsupported provider type")
	if err != nil && !strings.Contains(err.Error(), "faux") {
		t.Errorf("error should list supported types, got: %v", err)
	}
}

func assertErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Fatalf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Fatalf("expected error containing %q, got nil", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
