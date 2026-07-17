package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestToolCallContentMarshal(t *testing.T) {
	tests := []struct {
		name    string
		content acp.ToolCallContent
		want    string
	}{
		{
			name:    "content block",
			content: acp.ToolCallContentBlock{Content: acp.TextBlock("3 files")},
			want:    `{"type":"content","content":{"type":"text","text":"3 files"}}`,
		},
		{
			name:    "diff with old text",
			content: acp.ToolCallContentDiff{Path: "a.go", OldText: "old", NewText: "new"},
			want:    `{"type":"diff","path":"a.go","oldText":"old","newText":"new"}`,
		},
		{
			name:    "diff without old text omits oldText",
			content: acp.ToolCallContentDiff{Path: "a.go", NewText: "new"},
			want:    `{"type":"diff","path":"a.go","newText":"new"}`,
		},
		{
			name:    "terminal",
			content: acp.ToolCallContentTerminal{TerminalID: "term-1"},
			want:    `{"type":"terminal","terminalId":"term-1"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.content)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}
