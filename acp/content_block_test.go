package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestContentBlockMarshal(t *testing.T) {
	tests := []struct {
		name  string
		block acp.ContentBlock
		want  string
	}{
		{
			name:  "text",
			block: acp.TextBlock("hello"),
			want:  `{"type":"text","text":"hello"}`,
		},
		{
			name:  "resource_link",
			block: acp.ResourceLink("file:///a.go", "a.go"),
			want:  `{"type":"resource_link","uri":"file:///a.go","name":"a.go"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.block)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, got, tc.want)
		})
	}
}

func TestContentBlockRoundTrip(t *testing.T) {
	tests := []acp.ContentBlock{
		acp.TextBlock("round trip"),
		acp.ResourceLink("file:///b.go", "b.go"),
	}
	for _, want := range tests {
		data, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		got, err := acp.UnmarshalContentBlock(data)
		if err != nil {
			t.Fatalf("UnmarshalContentBlock() error = %v", err)
		}
		if got != want {
			t.Errorf("round trip = %#v, want %#v", got, want)
		}
	}
}

func TestUnmarshalContentBlockUnknownType(t *testing.T) {
	_, err := acp.UnmarshalContentBlock([]byte(`{"type":"image"}`))
	if err == nil {
		t.Fatal("UnmarshalContentBlock() error = nil, want error for unmodeled variant")
	}
}

func TestBlocksText(t *testing.T) {
	blocks := []acp.ContentBlock{
		acp.TextBlock("hello "),
		acp.ResourceLink("file:///a.go", "a.go"),
		acp.TextBlock("world"),
	}
	if got, want := acp.BlocksText(blocks), "hello world"; got != want {
		t.Errorf("BlocksText() = %q, want %q", got, want)
	}
}

func TestBlocksTextEmpty(t *testing.T) {
	if got := acp.BlocksText(nil); got != "" {
		t.Errorf("BlocksText(nil) = %q, want empty", got)
	}
}
