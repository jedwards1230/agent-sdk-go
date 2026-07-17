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

// ImageContentBlock is inline image content: base64 data plus its media type,
// with an optional source URI. Build one with [ImageBlock].
type ImageContentBlock struct {
	// Data is the base64-encoded image bytes.
	Data string
	// MimeType is the image's media type (e.g. "image/png").
	MimeType string
	// URI is the image's optional source location.
	URI string
}

// Type returns "image".
func (ImageContentBlock) Type() string { return "image" }

// MarshalJSON encodes {"type":"image","data":...,"mimeType":...,"uri"?:...}.
func (b ImageContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
		URI      string `json:"uri,omitempty"`
	}{b.Type(), b.Data, b.MimeType, b.URI})
}

// ImageBlock builds an image [ContentBlock] from base64 data and its media type.
func ImageBlock(data, mimeType string) ContentBlock {
	return ImageContentBlock{Data: data, MimeType: mimeType}
}

// AudioContentBlock is inline audio content: base64 data plus its media type.
// Build one with [AudioBlock].
type AudioContentBlock struct {
	// Data is the base64-encoded audio bytes.
	Data string
	// MimeType is the audio's media type (e.g. "audio/wav").
	MimeType string
}

// Type returns "audio".
func (AudioContentBlock) Type() string { return "audio" }

// MarshalJSON encodes {"type":"audio","data":...,"mimeType":...}.
func (b AudioContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}{b.Type(), b.Data, b.MimeType})
}

// AudioBlock builds an audio [ContentBlock] from base64 data and its media type.
func AudioBlock(data, mimeType string) ContentBlock {
	return AudioContentBlock{Data: data, MimeType: mimeType}
}

// EmbeddedResource is the inner resource of a [ResourceContentBlock]: a resource
// identified by URI carrying EITHER text or base64 blob content (exactly one),
// with an optional media type.
type EmbeddedResource struct {
	// URI is the resource's location.
	URI string
	// Text is the resource's text content. Set for a text resource; empty for a
	// blob resource.
	Text string
	// Blob is the resource's base64-encoded binary content. Set for a blob
	// resource; empty for a text resource.
	Blob string
	// MimeType is the resource's optional media type.
	MimeType string
}

// MarshalJSON encodes a text resource {"uri":...,"text":...,"mimeType"?:...} or,
// when Blob is set, a blob resource {"uri":...,"blob":...,"mimeType"?:...}. Text
// wins if both are somehow set; a resource with neither encodes as an empty-text
// resource.
func (r EmbeddedResource) MarshalJSON() ([]byte, error) {
	if r.Blob != "" && r.Text == "" {
		return json.Marshal(struct {
			URI      string `json:"uri"`
			Blob     string `json:"blob"`
			MimeType string `json:"mimeType,omitempty"`
		}{r.URI, r.Blob, r.MimeType})
	}
	return json.Marshal(struct {
		URI      string `json:"uri"`
		Text     string `json:"text"`
		MimeType string `json:"mimeType,omitempty"`
	}{r.URI, r.Text, r.MimeType})
}

// ResourceContentBlock embeds a resource's content inline (as opposed to
// [ResourceLinkContentBlock], which references it by URI). Build one with
// [TextResourceBlock] or [BlobResourceBlock].
type ResourceContentBlock struct {
	// Resource is the embedded resource.
	Resource EmbeddedResource
}

// Type returns "resource".
func (ResourceContentBlock) Type() string { return "resource" }

// MarshalJSON encodes {"type":"resource","resource":{...}}.
func (b ResourceContentBlock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type     string           `json:"type"`
		Resource EmbeddedResource `json:"resource"`
	}{b.Type(), b.Resource})
}

// TextResourceBlock builds an embedded-resource [ContentBlock] carrying text.
func TextResourceBlock(uri, text, mimeType string) ContentBlock {
	return ResourceContentBlock{Resource: EmbeddedResource{URI: uri, Text: text, MimeType: mimeType}}
}

// BlobResourceBlock builds an embedded-resource [ContentBlock] carrying a
// base64-encoded binary blob.
func BlobResourceBlock(uri, blob, mimeType string) ContentBlock {
	return ResourceContentBlock{Resource: EmbeddedResource{URI: uri, Blob: blob, MimeType: mimeType}}
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
	case "image":
		var v struct {
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode image block: %w", err)
		}
		return ImageContentBlock{Data: v.Data, MimeType: v.MimeType, URI: v.URI}, nil
	case "audio":
		var v struct {
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode audio block: %w", err)
		}
		return AudioContentBlock{Data: v.Data, MimeType: v.MimeType}, nil
	case "resource":
		var v struct {
			Resource struct {
				URI      string `json:"uri"`
				Text     string `json:"text"`
				Blob     string `json:"blob"`
				MimeType string `json:"mimeType"`
			} `json:"resource"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode resource block: %w", err)
		}
		return ResourceContentBlock{Resource: EmbeddedResource{
			URI:      v.Resource.URI,
			Text:     v.Resource.Text,
			Blob:     v.Resource.Blob,
			MimeType: v.Resource.MimeType,
		}}, nil
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
