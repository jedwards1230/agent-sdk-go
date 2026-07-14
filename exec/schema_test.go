package exec

import (
	"errors"
	"strings"
	"testing"
)

// TestCompileSchema covers schema-document parsing: a malformed doc and an
// unrecognized type are compile errors; supported types compile.
func TestCompileSchema(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		wantErr string // substring; empty means success
	}{
		{name: "empty object", doc: `{}`},
		{name: "typed object", doc: `{"type":"object","properties":{"a":{"type":"string"}}}`},
		{name: "nested items", doc: `{"type":"array","items":{"type":"integer"}}`},
		{name: "unknown keyword ignored", doc: `{"type":"string","format":"email"}`},
		{name: "malformed json", doc: `{"type":`, wantErr: "parse schema"},
		{name: "unknown type", doc: `{"type":"widget"}`, wantErr: "unsupported schema type"},
		{name: "unknown nested type", doc: `{"type":"object","properties":{"a":{"type":"blob"}}}`, wantErr: "unsupported schema type"},
		{name: "unknown items type", doc: `{"type":"array","items":{"type":"blob"}}`, wantErr: "unsupported schema type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compileSchema([]byte(tt.doc))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

// TestValidate exercises each supported keyword's pass and fail path.
func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		data     string
		wantOK   bool
		wantPath string
	}{
		// type: string
		{name: "string ok", schema: `{"type":"string"}`, data: `"hi"`, wantOK: true},
		{name: "string mismatch", schema: `{"type":"string"}`, data: `42`, wantPath: ""},

		// type: number vs integer
		{name: "number accepts fractional", schema: `{"type":"number"}`, data: `1.5`, wantOK: true},
		{name: "integer ok", schema: `{"type":"integer"}`, data: `7`, wantOK: true},
		{name: "integer rejects fractional", schema: `{"type":"integer"}`, data: `1.5`, wantPath: ""},

		// type: boolean / null
		{name: "boolean ok", schema: `{"type":"boolean"}`, data: `true`, wantOK: true},
		{name: "null ok", schema: `{"type":"null"}`, data: `null`, wantOK: true},
		{name: "null mismatch", schema: `{"type":"null"}`, data: `0`, wantPath: ""},

		// required
		{name: "required present", schema: `{"type":"object","required":["a"]}`, data: `{"a":1}`, wantOK: true},
		{name: "required missing", schema: `{"type":"object","required":["a"]}`, data: `{}`, wantPath: "/a"},

		// properties recursion
		{name: "property ok", schema: `{"type":"object","properties":{"a":{"type":"string"}}}`, data: `{"a":"x"}`, wantOK: true},
		{name: "property type fail", schema: `{"type":"object","properties":{"a":{"type":"string"}}}`, data: `{"a":1}`, wantPath: "/a"},

		// additionalProperties
		{name: "additional allowed by default", schema: `{"type":"object","properties":{"a":{"type":"string"}}}`, data: `{"a":"x","b":1}`, wantOK: true},
		{name: "additional false ok", schema: `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`, data: `{"a":"x"}`, wantOK: true},
		{name: "additional false violation", schema: `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`, data: `{"a":"x","b":1}`, wantPath: "/b"},

		// items
		{name: "items ok", schema: `{"type":"array","items":{"type":"integer"}}`, data: `[1,2,3]`, wantOK: true},
		{name: "items fail", schema: `{"type":"array","items":{"type":"integer"}}`, data: `[1,"x"]`, wantPath: "/1"},

		// enum
		{name: "enum hit", schema: `{"enum":["a","b"]}`, data: `"b"`, wantOK: true},
		{name: "enum number hit", schema: `{"enum":[1,2,3]}`, data: `2`, wantOK: true},
		{name: "enum miss", schema: `{"enum":["a","b"]}`, data: `"c"`, wantPath: ""},

		// minimum / maximum
		{name: "minimum ok", schema: `{"type":"number","minimum":10}`, data: `10`, wantOK: true},
		{name: "minimum fail", schema: `{"type":"number","minimum":10}`, data: `9`, wantPath: ""},
		{name: "maximum ok", schema: `{"type":"number","maximum":10}`, data: `10`, wantOK: true},
		{name: "maximum fail", schema: `{"type":"number","maximum":10}`, data: `11`, wantPath: ""},

		// minLength / maxLength (rune length)
		{name: "minLength ok", schema: `{"type":"string","minLength":2}`, data: `"ab"`, wantOK: true},
		{name: "minLength fail", schema: `{"type":"string","minLength":2}`, data: `"a"`, wantPath: ""},
		{name: "maxLength ok", schema: `{"type":"string","maxLength":2}`, data: `"ab"`, wantOK: true},
		{name: "maxLength fail", schema: `{"type":"string","maxLength":2}`, data: `"abc"`, wantPath: ""},
		{name: "length counts runes", schema: `{"type":"string","maxLength":1}`, data: `"é"`, wantOK: true},

		// minItems / maxItems
		{name: "minItems ok", schema: `{"type":"array","minItems":2}`, data: `[1,2]`, wantOK: true},
		{name: "minItems fail", schema: `{"type":"array","minItems":2}`, data: `[1]`, wantPath: ""},
		{name: "maxItems ok", schema: `{"type":"array","maxItems":2}`, data: `[1,2]`, wantOK: true},
		{name: "maxItems fail", schema: `{"type":"array","maxItems":2}`, data: `[1,2,3]`, wantPath: ""},

		// invalid JSON data
		{name: "not json", schema: `{"type":"object"}`, data: `nope`, wantPath: ""},
		{name: "empty data", schema: `{"type":"object"}`, data: ``, wantPath: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := compileSchema([]byte(tt.schema))
			if err != nil {
				t.Fatalf("compileSchema: %v", err)
			}
			err = s.validate([]byte(tt.data))
			if tt.wantOK {
				if err != nil {
					t.Fatalf("validate: unexpected error: %v", err)
				}
				return
			}
			var se *SchemaError
			if !errors.As(err, &se) {
				t.Fatalf("validate: error = %v, want *SchemaError", err)
			}
			if se.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", se.Path, tt.wantPath)
			}
			if se.Msg == "" {
				t.Errorf("Msg is empty")
			}
		})
	}
}
