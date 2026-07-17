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
		{
			name:  "image",
			block: acp.ImageBlock("aGk=", "image/png"),
			want:  `{"type":"image","data":"aGk=","mimeType":"image/png"}`,
		},
		{
			name:  "image with uri",
			block: acp.ImageContentBlock{Data: "aGk=", MimeType: "image/png", URI: "file:///a.png"},
			want:  `{"type":"image","data":"aGk=","mimeType":"image/png","uri":"file:///a.png"}`,
		},
		{
			name:  "audio",
			block: acp.AudioBlock("aGk=", "audio/wav"),
			want:  `{"type":"audio","data":"aGk=","mimeType":"audio/wav"}`,
		},
		{
			name:  "text resource",
			block: acp.TextResourceBlock("file:///a.txt", "hello", "text/plain"),
			want:  `{"type":"resource","resource":{"uri":"file:///a.txt","text":"hello","mimeType":"text/plain"}}`,
		},
		{
			name:  "text resource without mime",
			block: acp.TextResourceBlock("file:///a.txt", "hello", ""),
			want:  `{"type":"resource","resource":{"uri":"file:///a.txt","text":"hello"}}`,
		},
		{
			name:  "blob resource",
			block: acp.BlobResourceBlock("file:///a.bin", "aGk=", "application/octet-stream"),
			want:  `{"type":"resource","resource":{"uri":"file:///a.bin","blob":"aGk=","mimeType":"application/octet-stream"}}`,
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
		acp.ImageBlock("aGk=", "image/png"),
		acp.ImageContentBlock{Data: "aGk=", MimeType: "image/png", URI: "file:///b.png"},
		acp.AudioBlock("aGk=", "audio/wav"),
		acp.TextResourceBlock("file:///b.txt", "body", "text/plain"),
		acp.TextResourceBlock("file:///b.txt", "body", ""),
		acp.BlobResourceBlock("file:///b.bin", "aGk=", "application/octet-stream"),
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
	_, err := acp.UnmarshalContentBlock([]byte(`{"type":"totally_unknown"}`))
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
