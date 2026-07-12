package tool

import (
	"encoding/json"
	"testing"
)

func TestSchemaMarshaling(t *testing.T) {
	schema := ObjectSchema([]string{"path"}, map[string]Property{
		"path": {Type: "string", Description: "file path"},
		"limit": {
			Type:    "integer",
			Default: 2000,
		},
	})

	got, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var round Schema
	if err := json.Unmarshal(got, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if round.Type != "object" {
		t.Errorf("Type = %q, want object", round.Type)
	}
	if len(round.Required) != 1 || round.Required[0] != "path" {
		t.Errorf("Required = %v, want [path]", round.Required)
	}
	if round.Properties["path"].Description != "file path" {
		t.Errorf("Properties[path].Description = %q", round.Properties["path"].Description)
	}
}

func TestObjectSchema(t *testing.T) {
	s := ObjectSchema(nil, nil)
	if s.Type != "object" {
		t.Errorf("Type = %q, want object", s.Type)
	}
	if s.Properties != nil {
		t.Errorf("Properties = %v, want nil", s.Properties)
	}
	if s.Required != nil {
		t.Errorf("Required = %v, want nil", s.Required)
	}
}

func TestResultMetadataBasics(t *testing.T) {
	exitCode := 1
	r := Result{
		Content: "boom",
		IsError: true,
		Metadata: Metadata{
			ExitCode:  &exitCode,
			Truncated: true,
			Diagnostics: []Diagnostic{
				{File: "a.go", Line: 1, Col: 2, Severity: "error", Message: "bad"},
			},
			Extra: map[string]any{"lines": 3},
		},
	}
	if r.Content != "boom" || !r.IsError {
		t.Fatalf("unexpected Result: %+v", r)
	}
	if r.Metadata.ExitCode == nil || *r.Metadata.ExitCode != 1 {
		t.Errorf("ExitCode = %v, want 1", r.Metadata.ExitCode)
	}
	if !r.Metadata.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if len(r.Metadata.Diagnostics) != 1 {
		t.Errorf("Diagnostics = %v, want len 1", r.Metadata.Diagnostics)
	}
}

func TestErrorResult(t *testing.T) {
	r := errorResult("bad thing: %s", "oops")
	if !r.IsError {
		t.Errorf("IsError = false, want true")
	}
	if r.Content != "bad thing: oops" {
		t.Errorf("Content = %q, want %q", r.Content, "bad thing: oops")
	}
}

func TestDiagnosticJSON(t *testing.T) {
	d := Diagnostic{File: "a.go", Line: 1, Col: 2, Severity: "warning", Message: "m"}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round Diagnostic
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if round != d {
		t.Errorf("round trip = %+v, want %+v", round, d)
	}
}
