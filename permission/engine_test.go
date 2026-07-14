package permission_test

import (
	"testing"

	"github.com/jedwards1230/agent-sdk-go/event"
	"github.com/jedwards1230/agent-sdk-go/permission"
)

func TestEvaluateDenyBeatsAllow(t *testing.T) {
	e := permission.New(
		permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "*"},
		permission.Rule{Verdict: event.VerdictDeny, Tool: "Bash", Specifier: "rm -rf:*"},
	)
	v, rule, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "rm -rf /"})
	if !matched || v != event.VerdictDeny {
		t.Fatalf("verdict = %v, matched = %v, want deny/true", v, matched)
	}
	if rule.Specifier != "rm -rf:*" {
		t.Errorf("matched rule = %+v, want the deny rule", rule)
	}
}

func TestEvaluatePrecedenceOrder(t *testing.T) {
	// A call matching allow, ask, AND deny rules must resolve to deny; one
	// matching allow and ask (no deny) must resolve to ask.
	e := permission.New(
		permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "curl:*"},
		permission.Rule{Verdict: event.VerdictAsk, Tool: "Bash", Specifier: "curl:*"},
		permission.Rule{Verdict: event.VerdictDeny, Tool: "Bash", Specifier: "curl:*"},
	)
	v, _, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "curl example.com"})
	if !matched || v != event.VerdictDeny {
		t.Fatalf("verdict = %v, matched = %v, want deny/true", v, matched)
	}

	e2 := permission.New(
		permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "wget:*"},
		permission.Rule{Verdict: event.VerdictAsk, Tool: "Bash", Specifier: "wget:*"},
	)
	v2, _, matched2 := e2.Evaluate(permission.Request{Tool: "Bash", Target: "wget example.com"})
	if !matched2 || v2 != event.VerdictAsk {
		t.Fatalf("verdict = %v, matched = %v, want ask/true", v2, matched2)
	}
}

func TestEvaluateUnmatchedIsAsk(t *testing.T) {
	e := permission.New(permission.Rule{Verdict: event.VerdictAllow, Tool: "Read", Specifier: "*"})
	v, rule, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "ls"})
	if matched || v != event.VerdictAsk {
		t.Errorf("verdict = %v, matched = %v, want ask/false", v, matched)
	}
	if rule != (permission.Rule{}) {
		t.Errorf("unmatched rule = %+v, want zero value", rule)
	}
}

func TestEvaluateToolWildcard(t *testing.T) {
	for _, tool := range []string{"", "*"} {
		e := permission.New(permission.Rule{Verdict: event.VerdictAllow, Tool: tool, Specifier: "*"})
		v, _, matched := e.Evaluate(permission.Request{Tool: "AnyTool", Target: "x"})
		if !matched || v != event.VerdictAllow {
			t.Errorf("tool=%q: verdict = %v, matched = %v, want allow/true", tool, v, matched)
		}
	}
}

func TestEvaluateSpecifierPrefix(t *testing.T) {
	e := permission.New(permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "git status:*"})
	cases := []struct {
		target string
		want   bool
	}{
		{"git status", true},
		{"git status -s", true},
		{"ls -la", false},
	}
	for _, tc := range cases {
		v, _, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: tc.target})
		got := matched && v == event.VerdictAllow
		if got != tc.want {
			t.Errorf("target=%q: matched=%v verdict=%v, want match=%v", tc.target, matched, v, tc.want)
		}
	}
}

func TestEvaluateSpecifierGlob(t *testing.T) {
	e := permission.New(permission.Rule{Verdict: event.VerdictDeny, Tool: "Read", Specifier: "*.env"})

	v, _, matched := e.Evaluate(permission.Request{Tool: "Read", Target: "config.env"})
	if !matched || v != event.VerdictDeny {
		t.Errorf("verdict = %v, matched = %v, want deny/true", v, matched)
	}

	v2, _, matched2 := e.Evaluate(permission.Request{Tool: "Read", Target: "config.yaml"})
	if matched2 || v2 != event.VerdictAsk {
		t.Errorf("non-matching glob should fall through to unmatched ask: verdict=%v matched=%v", v2, matched2)
	}
}

func TestGrantAddsRuntimeAllow(t *testing.T) {
	e := permission.New()

	before, _, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "git status"})
	if matched || before != event.VerdictAsk {
		t.Fatalf("expected unmatched ask before grant, got verdict=%v matched=%v", before, matched)
	}

	e.Grant(permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "git status:*", Source: "session"})

	after, rule, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "git status"})
	if !matched || after != event.VerdictAllow {
		t.Fatalf("expected allow after grant, got verdict=%v matched=%v", after, matched)
	}
	if rule.Source != "session" {
		t.Errorf("rule = %+v, want Source=session", rule)
	}
}

func TestGrantNeverOverridesDeny(t *testing.T) {
	e := permission.New(permission.Rule{Verdict: event.VerdictDeny, Tool: "Bash", Specifier: "rm -rf:*"})
	e.Grant(permission.Rule{Verdict: event.VerdictAllow, Tool: "Bash", Specifier: "rm -rf:*", Source: "session"})

	v, _, matched := e.Evaluate(permission.Request{Tool: "Bash", Target: "rm -rf /"})
	if !matched || v != event.VerdictDeny {
		t.Errorf("verdict = %v, matched = %v, want deny/true (deny beats a later grant)", v, matched)
	}
}
