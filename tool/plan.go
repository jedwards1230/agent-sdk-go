package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// Plan priority and status enums, matching the ACP v1 plan entry schema.
const (
	planPriorityHigh   = "high"
	planPriorityMedium = "medium"
	planPriorityLow    = "low"

	planStatusPending    = "pending"
	planStatusInProgress = "in_progress"
	planStatusCompleted  = "completed"
)

// PlanEntry is one item in the agent's task plan: its human-readable content,
// how it is prioritized, and where it stands. It mirrors the ACP v1 plan-entry
// shape; the loop bridges it to an event.PlanEntry, which projects to a `plan`
// ACP session/update so a client can render a live checklist.
type PlanEntry struct {
	// Content is the human-readable description of the task.
	Content string
	// Priority is one of "high", "medium", "low".
	Priority string
	// Status is one of "pending", "in_progress", "completed".
	Status string
}

// UpdatePlan is a policy-free builtin tool the model calls to publish or revise
// its task plan. Each call carries the full current plan; the SDK records the
// validated entries on the result's [Metadata] so the loop can emit an
// authoritative plan snapshot event, which projects to an ACP `plan`
// session/update. The plan's content is the model's; the tool and its
// projection are the SDK's.
type UpdatePlan struct{}

// NewUpdatePlan returns an UpdatePlan tool. It is stateless — unlike the
// filesystem builtins it takes no root directory.
func NewUpdatePlan() *UpdatePlan { return &UpdatePlan{} }

// Name returns "update_plan".
func (*UpdatePlan) Name() string { return "update_plan" }

// Description returns the model-facing description of UpdatePlan.
func (*UpdatePlan) Description() string {
	return "Publish or update your task plan as a checklist the user can see. " +
		"Call it with the full current plan every time — the complete list of " +
		"entries, not a delta. Each entry has content (what the step is), a " +
		"priority (\"high\", \"medium\", or \"low\"), and a status (\"pending\", " +
		"\"in_progress\", or \"completed\"). Revise and re-send the whole plan as " +
		"work progresses; send an empty list to clear it."
}

// Spec returns the JSON Schema for UpdatePlan's input: an object with a required
// "entries" array of plan-entry objects.
func (*UpdatePlan) Spec() Schema {
	entry := Property{
		Type: "object",
		Properties: map[string]Property{
			"content": {
				Type:        "string",
				Description: "Human-readable description of the task.",
			},
			"priority": {
				Type:        "string",
				Enum:        []string{planPriorityHigh, planPriorityMedium, planPriorityLow},
				Description: "Task priority.",
			},
			"status": {
				Type:        "string",
				Enum:        []string{planStatusPending, planStatusInProgress, planStatusCompleted},
				Description: "Task status.",
			},
		},
	}
	return ObjectSchema([]string{"entries"}, map[string]Property{
		"entries": {
			Type:        "array",
			Description: "The full current plan, in order. Each entry needs content, priority, and status.",
			Items:       &entry,
		},
	})
}

// updatePlanInput is the decoded shape of UpdatePlan's Run argument.
type updatePlanInput struct {
	Entries []planEntryInput `json:"entries"`
}

// planEntryInput is one decoded plan entry as the model supplies it.
type planEntryInput struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

// Run validates the supplied plan and records it on the result's [Metadata] so
// the loop emits a plan snapshot event. A malformed argument object is returned
// as a non-nil error (an invalid call); an entry with empty content or an
// out-of-range priority/status is returned as an IsError [Result] the model can
// correct — either way no plan is recorded, so a malformed plan is never
// emitted. An empty entries list is valid and clears the plan.
func (t *UpdatePlan) Run(ctx context.Context, input json.RawMessage) (Result, error) {
	if err := ctxErr(ctx); err != nil {
		return Result{}, err
	}
	var in updatePlanInput
	if err := decodeInput(input, &in); err != nil {
		return Result{}, err
	}

	entries := make([]PlanEntry, 0, len(in.Entries))
	for i, e := range in.Entries {
		if e.Content == "" {
			return errorResult("plan entry %d: content must not be empty", i), nil
		}
		if !validPlanPriority(e.Priority) {
			return errorResult("plan entry %d: priority %q must be one of %q, %q, %q",
				i, e.Priority, planPriorityHigh, planPriorityMedium, planPriorityLow), nil
		}
		if !validPlanStatus(e.Status) {
			return errorResult("plan entry %d: status %q must be one of %q, %q, %q",
				i, e.Status, planStatusPending, planStatusInProgress, planStatusCompleted), nil
		}
		entries = append(entries, PlanEntry(e))
	}

	return Result{
		Content:  planConfirmation(len(entries)),
		Metadata: Metadata{Plan: entries},
	}, nil
}

// planConfirmation is the short model-facing acknowledgement the tool returns so
// the loop continues after the plan is recorded.
func planConfirmation(n int) string {
	if n == 0 {
		return "Plan cleared."
	}
	if n == 1 {
		return "Plan updated: 1 entry."
	}
	return fmt.Sprintf("Plan updated: %d entries.", n)
}

func validPlanPriority(p string) bool {
	switch p {
	case planPriorityHigh, planPriorityMedium, planPriorityLow:
		return true
	default:
		return false
	}
}

func validPlanStatus(s string) bool {
	switch s {
	case planStatusPending, planStatusInProgress, planStatusCompleted:
		return true
	default:
		return false
	}
}
