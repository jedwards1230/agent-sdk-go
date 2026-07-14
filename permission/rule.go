// Package permission is the SDK's format-agnostic permission rule engine: a
// typed []Rule + evaluator the guard consults for the deny/allow-before-sandbox
// step. Vendor-format loaders (Claude Code settings.json, native manifest) are
// M4/M5 and deliberately NOT here yet.
package permission

import "github.com/jedwards1230/agent-sdk-go/event"

// Rule is one entry in a ruleset. Verdict is allow|ask|deny. Tool "" or "*"
// matches any tool. Specifier "" or "*" matches any target; a "prefix:*"
// specifier matches by command/target prefix; otherwise it is a path.Match
// glob. Source is an informational provenance label (e.g. "session",
// "project").
type Rule struct {
	Verdict   event.Verdict
	Tool      string
	Specifier string
	Source    string
}

// matches reports whether req satisfies r's tool and specifier match.
func (r Rule) matches(req Request) bool {
	if r.Tool != "" && r.Tool != "*" && r.Tool != req.Tool {
		return false
	}
	return specMatches(r.Specifier, req.Target)
}
