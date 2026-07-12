package compose_test

import (
	"context"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/compose"
	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/loop"
	"github.com/jedwards1230/agent-sdk-go/provider"
)

func TestParseAuth(t *testing.T) {
	tests := []struct {
		in       string
		wantKind compose.AuthKind
		wantRef  string
		wantErr  bool
	}{
		{in: "env:ANTHROPIC_API_KEY", wantKind: compose.AuthEnv, wantRef: "ANTHROPIC_API_KEY"},
		{in: "oauth:anthropic", wantKind: compose.AuthOAuth, wantRef: "anthropic"},
		{in: "env:", wantErr: true},
		{in: "bogus", wantErr: true},
		{in: "sms:x", wantErr: true},
	}
	for _, tt := range tests {
		got, err := compose.ParseAuth(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseAuth(%q) = %+v, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAuth(%q): %v", tt.in, err)
			continue
		}
		if got.Kind != tt.wantKind || got.Ref != tt.wantRef {
			t.Errorf("ParseAuth(%q) = %+v", tt.in, got)
		}
	}
}

func TestModelIDOverride(t *testing.T) {
	m := &compose.Manifest{Model: "top", Provider: compose.ProviderConfig{Type: "faux", Model: "override"}}
	if m.ModelID() != "override" {
		t.Errorf("ModelID = %q, want override", m.ModelID())
	}
	m.Provider.Model = ""
	if m.ModelID() != "top" {
		t.Errorf("ModelID = %q, want top", m.ModelID())
	}
}

func TestCredentialSourceEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-abc")
	// A real model id resolves the provider id to "anthropic" via the registry.
	m := &compose.Manifest{
		Name:     "a",
		Model:    "claude-sonnet-5",
		Provider: compose.ProviderConfig{Type: "anthropic", Auth: "env:ANTHROPIC_API_KEY"},
	}
	src, err := compose.CredentialSource(m)
	if err != nil {
		t.Fatalf("CredentialSource: %v", err)
	}
	cred, err := src.Credential(context.Background(), "anthropic")
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Kind != provider.CredAPIKey || cred.Token != "sk-abc" {
		t.Errorf("cred = %+v", cred)
	}
}

func TestCredentialSourceOAuthDeferred(t *testing.T) {
	m := &compose.Manifest{Name: "a", Provider: compose.ProviderConfig{Type: "anthropic", Auth: "oauth:anthropic"}}
	if _, err := compose.CredentialSource(m); err == nil {
		t.Error("oauth auth should defer to an auth.Store and error here")
	}
}

func TestParseRejectsBadAuth(t *testing.T) {
	if _, err := compose.Parse([]byte("name: a\nprovider:\n  type: faux\n  auth: nope\n")); err == nil {
		t.Error("Parse should reject a malformed auth value")
	}
}

func TestBuildHonorsModelOverride(t *testing.T) {
	m := &compose.Manifest{Name: "a", Model: "top", Provider: compose.ProviderConfig{Type: "faux", Model: "override"}}
	sess, err := compose.Build(context.Background(), m)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer sess.Close()
	if sess.Model() != "override" {
		t.Errorf("session model = %q, want the provider.model override", sess.Model())
	}
}

func TestLoopConfigDrivesFaux(t *testing.T) {
	b := event.NewBroker()
	defer b.Close()
	sub := b.Subscribe(event.FilterAll, 256)

	m := &compose.Manifest{Name: "demo", Model: "faux-1", Provider: compose.ProviderConfig{Type: "faux"}}
	cfg, err := compose.LoopConfig(context.Background(), m, compose.LoopDeps{Broker: b, SessionID: "s1"})
	if err != nil {
		t.Fatalf("LoopConfig: %v", err)
	}
	res, err := loop.Run(context.Background(), cfg, []provider.Message{provider.UserText("hi")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != provider.StopEndTurn {
		t.Errorf("stop = %q, want end_turn", res.StopReason)
	}

	var sawTurnFinished bool
	for {
		select {
		case e, ok := <-sub.C:
			if !ok {
				goto done
			}
			if e.Kind() == event.KindTurnFinished {
				sawTurnFinished = true
			}
		default:
			goto done
		}
	}
done:
	if !sawTurnFinished {
		t.Error("expected a turn.finished event through the broker")
	}
}

// TestLoopConfigResolvesRealProvider asserts a non-faux manifest resolves
// through providers.Build to the matching adapter, using the manifest's
// env-based credential source (no CredentialSource override, no network).
func TestLoopConfigResolvesRealProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	b := event.NewBroker()
	defer b.Close()

	m := &compose.Manifest{
		Name:     "demo",
		Model:    "claude-sonnet-5",
		Provider: compose.ProviderConfig{Type: "anthropic", Auth: "env:ANTHROPIC_API_KEY"},
	}
	cfg, err := compose.LoopConfig(context.Background(), m, compose.LoopDeps{Broker: b, SessionID: "s1"})
	if err != nil {
		t.Fatalf("LoopConfig: %v", err)
	}
	if got := cfg.Provider.Info().Provider; got != "anthropic" {
		t.Errorf("Provider.Info().Provider = %q, want anthropic", got)
	}
}
