package tool

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestUpdatePlanNameAndSpec(t *testing.T) {
	tp := NewUpdatePlan()
	if tp.Name() != "update_plan" {
		t.Fatalf("Name() = %q, want update_plan", tp.Name())
	}
	spec := tp.Spec()
	if spec.Type != "object" {
		t.Fatalf("Spec().Type = %q, want object", spec.Type)
	}
	if len(spec.Required) != 1 || spec.Required[0] != "entries" {
		t.Fatalf("Spec().Required = %v, want [entries]", spec.Required)
	}
	entries, ok := spec.Properties["entries"]
	if !ok || entries.Type != "array" || entries.Items == nil {
		t.Fatalf("entries property = %+v, want an array with items", entries)
	}
	item := entries.Items
	for _, field := range []string{"content", "priority", "status"} {
		if _, ok := item.Properties[field]; !ok {
			t.Fatalf("entry item missing %q property", field)
		}
	}
	if got := item.Properties["priority"].Enum; !reflect.DeepEqual(got, []string{"high", "medium", "low"}) {
		t.Fatalf("priority enum = %v", got)
	}
	if got := item.Properties["status"].Enum; !reflect.DeepEqual(got, []string{"pending", "in_progress", "completed"}) {
		t.Fatalf("status enum = %v", got)
	}
}

func TestUpdatePlanRun(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantErr     bool // non-nil error return (invalid call)
		wantIsError bool // IsError Result (model-correctable)
		wantPlan    []PlanEntry
		wantContent string
	}{
		{
			name:  "valid multi-entry plan",
			input: `{"entries":[{"content":"Read the code","priority":"high","status":"completed"},{"content":"Write the fix","priority":"medium","status":"in_progress"}]}`,
			wantPlan: []PlanEntry{
				{Content: "Read the code", Priority: "high", Status: "completed"},
				{Content: "Write the fix", Priority: "medium", Status: "in_progress"},
			},
			wantContent: "Plan updated: 2 entries.",
		},
		{
			name:        "single entry",
			input:       `{"entries":[{"content":"Ship it","priority":"low","status":"pending"}]}`,
			wantPlan:    []PlanEntry{{Content: "Ship it", Priority: "low", Status: "pending"}},
			wantContent: "Plan updated: 1 entry.",
		},
		{
			name:        "empty plan clears",
			input:       `{"entries":[]}`,
			wantPlan:    []PlanEntry{},
			wantContent: "Plan cleared.",
		},
		{
			name:        "missing entries key clears",
			input:       `{}`,
			wantPlan:    []PlanEntry{},
			wantContent: "Plan cleared.",
		},
		{
			name:        "empty content is a tool error",
			input:       `{"entries":[{"content":"","priority":"high","status":"pending"}]}`,
			wantIsError: true,
		},
		{
			name:        "bad priority is a tool error",
			input:       `{"entries":[{"content":"x","priority":"urgent","status":"pending"}]}`,
			wantIsError: true,
		},
		{
			name:        "bad status is a tool error",
			input:       `{"entries":[{"content":"x","priority":"high","status":"blocked"}]}`,
			wantIsError: true,
		},
		{
			name:    "malformed json is an invalid call",
			input:   `{"entries":`,
			wantErr: true,
		},
	}

	tp := NewUpdatePlan()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tp.Run(context.Background(), json.RawMessage(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Run() err = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() err = %v, want nil", err)
			}
			if res.IsError != tc.wantIsError {
				t.Fatalf("IsError = %v, want %v (content %q)", res.IsError, tc.wantIsError, res.Content)
			}
			if tc.wantIsError {
				if res.Metadata.Plan != nil {
					t.Fatalf("Metadata.Plan = %v on error result, want nil (no malformed plan emitted)", res.Metadata.Plan)
				}
				return
			}
			if res.Content != tc.wantContent {
				t.Fatalf("Content = %q, want %q", res.Content, tc.wantContent)
			}
			if res.Metadata.Plan == nil {
				t.Fatal("Metadata.Plan = nil, want a non-nil snapshot")
			}
			if !reflect.DeepEqual(res.Metadata.Plan, tc.wantPlan) {
				t.Fatalf("Metadata.Plan = %+v, want %+v", res.Metadata.Plan, tc.wantPlan)
			}
		})
	}
}

func TestUpdatePlanCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewUpdatePlan().Run(ctx, json.RawMessage(`{"entries":[]}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() err = %v, want context.Canceled", err)
	}
}
