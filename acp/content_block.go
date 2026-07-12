package acp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContentBlock is ACP's tagged content union: a closed set of concrete types
// discriminated on the wire by their "type" field. Only the types in this
// package implement it, so callers can exhaustively type-switch.
type ContentBlock interface {
	// Type returns the block's "type" discriminator (e.g. "text").
	Type() string

	json.Marshaler
}

// TextContentBlock is plain text content. Build one with [TextBlock].
type TextContentBlock struct {
	// Text is the block's text.
	Text string
}

// Type returns "text".
func (TextContentBlock) Type() string { return "text" }

// MarshalJSON encodes {"type":"text","text":...}.
func (b TextContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{b.Type(), b.Text})
}

// TextBlock builds a text [ContentBlock].
func TextBlock(s string) ContentBlock { return TextContentBlock{Text: s} }

// ResourceLinkContentBlock references an external resource by URI, as used
// for prompt attachments. Build one with [ResourceLink].
type ResourceLinkContentBlock struct {
	// URI is the resource's location.
	URI string
	// Name is a display name for the resource.
	Name string
}

// Type returns "resource_link".
func (ResourceLinkContentBlock) Type() string { return "resource_link" }

// MarshalJSON encodes {"type":"resource_link","uri":...,"name":...}.
func (b ResourceLinkContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		URI  string `json:"uri"`
		Name string `json:"name"`
	}{b.Type(), b.URI, b.Name})
}

// ResourceLink builds a resource_link [ContentBlock].
func ResourceLink(uri, name string) ContentBlock {
	return ResourceLinkContentBlock{URI: uri, Name: name}
}

// BlocksText concatenates the text of every text-variant block in blocks,
// ignoring other variants (e.g. resource_link).
func BlocksText(blocks []ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t, ok := b.(TextContentBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// UnmarshalContentBlock decodes a single ACP content block from its "type"
// discriminator. It returns an error for a variant this package does not
// model.
func UnmarshalContentBlock(data []byte) (ContentBlock, error) {
	var disc struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return nil, fmt.Errorf("acp: decode content block: %w", err)
	}
	switch disc.Type {
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode text block: %w", err)
		}
		return TextContentBlock{Text: v.Text}, nil
	case "resource_link":
		var v struct {
			URI  string `json:"uri"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode resource_link block: %w", err)
		}
		return ResourceLinkContentBlock{URI: v.URI, Name: v.Name}, nil
	default:
		return nil, fmt.Errorf("acp: unknown content block type %q", disc.Type)
	}
}

// unmarshalContentBlocks decodes a JSON array of content blocks.
func unmarshalContentBlocks(raw []json.RawMessage) ([]ContentBlock, error) {
	blocks := make([]ContentBlock, 0, len(raw))
	for _, r := range raw {
		b, err := UnmarshalContentBlock(r)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}
