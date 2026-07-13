package runner_test

import (
	"strings"

	"github.com/jedwards1230/agent-sdk-go/provider"
	"github.com/jedwards1230/agent-sdk-go/session"
)

// msgText concatenates a message's text blocks.
func msgText(m provider.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == provider.BlockText {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// msgReasoning concatenates a message's reasoning blocks.
func msgReasoning(m provider.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if blk.Type == provider.BlockReasoning {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// blocksOfType returns the message's content blocks of a given type.
func blocksOfType(m provider.Message, t provider.BlockType) []provider.ContentBlock {
	var out []provider.ContentBlock
	for _, blk := range m.Content {
		if blk.Type == t {
			out = append(out, blk)
		}
	}
	return out
}

// skipMeta drops a runner.New-created journal's leading [session.EntryMeta]
// entry (its root, carrying the session's cwd), if present, so tests
// asserting on the conversation entries by index don't have to special-case
// it. Fold-based assertions never need this: [session.Journal.Fold] already
// skips EntryMeta entries.
func skipMeta(entries []session.Entry) []session.Entry {
	if len(entries) > 0 && entries[0].Type == session.EntryMeta {
		return entries[1:]
	}
	return entries
}
