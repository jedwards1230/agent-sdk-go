package permission

import (
	"path"
	"strings"
	"sync"

	"github.com/jedwards1230/agent-sdk-go/event"
)

// Request is the tool invocation the engine evaluates. Target is the single
// string the Specifier matches against (the command line for bash, the path
// for file tools, …); the CALLER decides what Target is — the engine stays
// format-agnostic. Args is optional decoded input for future matchers.
type Request struct {
	Tool   string
	Target string
	Args   map[string]any
}

// tiers is the precedence order Evaluate checks: deny beats ask beats allow.
var tiers = [...]event.Verdict{event.VerdictDeny, event.VerdictAsk, event.VerdictAllow}

// Engine evaluates a Request against an ordered ruleset with deny > ask >
// allow precedence; an unmatched request is "ask" (fail-safe). Safe for
// concurrent use.
type Engine struct {
	mu    sync.RWMutex
	rules []Rule
}

// New returns an Engine seeded with rules.
func New(rules ...Rule) *Engine {
	return &Engine{rules: append([]Rule(nil), rules...)}
}

// Evaluate returns the winning verdict, the matched rule, and whether any rule
// matched. Checks all deny rules first, then ask, then allow; unmatched ⇒
// (VerdictAsk, Rule{}, false).
func (e *Engine) Evaluate(req Request) (event.Verdict, Rule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, tier := range tiers {
		for _, r := range e.rules {
			if r.Verdict == tier && r.matches(req) {
				return r.Verdict, r, true
			}
		}
	}
	return event.VerdictAsk, Rule{}, false
}

// Grant appends a runtime (session-scoped) rule — thin remember-grant
// persistence for M3. TTL / anti-escalation / dangerous-downgrade are M4/M5.
func (e *Engine) Grant(r Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, r)
}

// specMatches implements the specifier grammar: ""/"*" matches any target; a
// "prefix:*" specifier matches by prefix (exact prefix string, or the target
// itself); otherwise it is a path.Match glob against the target.
func specMatches(spec, target string) bool {
	if spec == "" || spec == "*" {
		return true
	}
	if strings.HasSuffix(spec, ":*") {
		prefix := strings.TrimSuffix(spec, ":*")
		return target == prefix || strings.HasPrefix(target, prefix)
	}
	ok, err := path.Match(spec, target)
	return err == nil && ok
}
