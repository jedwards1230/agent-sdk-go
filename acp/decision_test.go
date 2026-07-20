package acp_test

import (
	"encoding/json"
	"testing"

	"github.com/jedwards1230/agent-sdk-go/acp"
)

func TestToRequestDecisionSingleQuestion(t *testing.T) {
	questions := []acp.DecisionQuestion{
		{
			QuestionID: "q-1",
			Title:      "Migration",
			Question:   "Which migration should I run?",
			Context:    "The schema is behind by two revisions.",
			Options: []acp.DecisionOption{
				{OptionID: "opt-1", Label: "Additive only", Rationale: "Safest; no readers break.", Recommended: true},
				{OptionID: "opt-2", Label: "Full rewrite", Rationale: "Cleaner, but risky.", Reference: "migrations/0002.sql"},
			},
			AllowFreeText: true,
			AllowChat:     true,
		},
	}
	req := acp.ToRequestDecision("sess-1", questions)

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sessionId":"sess-1","questions":[` +
		`{"questionId":"q-1","title":"Migration","question":"Which migration should I run?",` +
		`"context":"The schema is behind by two revisions.","options":[` +
		`{"optionId":"opt-1","label":"Additive only","rationale":"Safest; no readers break.","recommended":true},` +
		`{"optionId":"opt-2","label":"Full rewrite","rationale":"Cleaner, but risky.","reference":"migrations/0002.sql"}` +
		`],"allowFreeText":true,"allowChat":true}]}`
	assertJSONEqual(t, got, want)
}

func TestToRequestDecisionMultiQuestion(t *testing.T) {
	questions := []acp.DecisionQuestion{
		{
			QuestionID: "q-1",
			Title:      "Deploy",
			Question:   "Deploy now or wait?",
			Options: []acp.DecisionOption{
				{OptionID: "now", Label: "Deploy now", Recommended: true},
				{OptionID: "wait", Label: "Wait for review"},
			},
		},
		{
			QuestionID: "q-2",
			Title:      "Notify",
			Question:   "Who should I notify?",
			Options: []acp.DecisionOption{
				{OptionID: "team", Label: "The whole team"},
				{OptionID: "oncall", Label: "On-call only"},
			},
			AllowFreeText: true,
		},
	}
	req := acp.ToRequestDecision("sess-2", questions)

	got, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sessionId":"sess-2","questions":[` +
		`{"questionId":"q-1","title":"Deploy","question":"Deploy now or wait?","options":[` +
		`{"optionId":"now","label":"Deploy now","recommended":true},` +
		`{"optionId":"wait","label":"Wait for review"}]},` +
		`{"questionId":"q-2","title":"Notify","question":"Who should I notify?","options":[` +
		`{"optionId":"team","label":"The whole team"},` +
		`{"optionId":"oncall","label":"On-call only"}],"allowFreeText":true}]}`
	assertJSONEqual(t, got, want)
}

func TestRequestDecisionNilSlices(t *testing.T) {
	t.Run("request nil questions", func(t *testing.T) {
		got, err := json.Marshal(acp.RequestDecisionRequest{SessionID: "sess-1"})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		assertJSONEqual(t, got, `{"sessionId":"sess-1","questions":[]}`)
	})

	t.Run("question nil options", func(t *testing.T) {
		got, err := json.Marshal(acp.DecisionQuestion{
			QuestionID:    "q-1",
			Title:         "Freeform",
			Question:      "What should I do?",
			AllowFreeText: true,
		})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		assertJSONEqual(t, got,
			`{"questionId":"q-1","title":"Freeform","question":"What should I do?","options":[],"allowFreeText":true}`)
	})

	t.Run("response nil answers", func(t *testing.T) {
		got, err := json.Marshal(acp.RequestDecisionResponse{})
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		assertJSONEqual(t, got, `{"answers":[]}`)
	})
}

func TestRequestDecisionResponseRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		resp    acp.RequestDecisionResponse
		want    string
		wantLen int
	}{
		{
			name:    "selected with notes",
			resp:    acp.RequestDecisionResponse{Answers: []acp.DecisionAnswer{{QuestionID: "q-1", Outcome: acp.DecisionOutcomeSelected{OptionID: "opt-1"}, Notes: "double-checked the diff"}}},
			want:    `{"answers":[{"questionId":"q-1","outcome":{"outcome":"selected","optionId":"opt-1"},"notes":"double-checked the diff"}]}`,
			wantLen: 1,
		},
		{
			name:    "text",
			resp:    acp.RequestDecisionResponse{Answers: []acp.DecisionAnswer{{QuestionID: "q-1", Outcome: acp.DecisionOutcomeText{Text: "let's do a canary first"}}}},
			want:    `{"answers":[{"questionId":"q-1","outcome":{"outcome":"text","text":"let's do a canary first"}}]}`,
			wantLen: 1,
		},
		{
			name:    "chat",
			resp:    acp.RequestDecisionResponse{Answers: []acp.DecisionAnswer{{QuestionID: "q-1", Outcome: acp.DecisionOutcomeChat{}}}},
			want:    `{"answers":[{"questionId":"q-1","outcome":{"outcome":"chat"}}]}`,
			wantLen: 1,
		},
		{
			name:    "cancelled",
			resp:    acp.RequestDecisionResponse{Answers: []acp.DecisionAnswer{{QuestionID: "q-1", Outcome: acp.DecisionOutcomeCancelled{}}}},
			want:    `{"answers":[{"questionId":"q-1","outcome":{"outcome":"cancelled"}}]}`,
			wantLen: 1,
		},
		{
			name: "multi-answer batch",
			resp: acp.RequestDecisionResponse{Answers: []acp.DecisionAnswer{
				{QuestionID: "q-1", Outcome: acp.DecisionOutcomeSelected{OptionID: "now"}},
				{QuestionID: "q-2", Outcome: acp.DecisionOutcomeCancelled{}},
			}},
			want: `{"answers":[` +
				`{"questionId":"q-1","outcome":{"outcome":"selected","optionId":"now"}},` +
				`{"questionId":"q-2","outcome":{"outcome":"cancelled"}}]}`,
			wantLen: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			assertJSONEqual(t, data, tc.want)

			var got acp.RequestDecisionResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if len(got.Answers) != tc.wantLen {
				t.Fatalf("round trip answers len = %d, want %d", len(got.Answers), tc.wantLen)
			}
			for i, want := range tc.resp.Answers {
				if got.Answers[i].QuestionID != want.QuestionID {
					t.Errorf("answer[%d] questionId = %q, want %q", i, got.Answers[i].QuestionID, want.QuestionID)
				}
				if got.Answers[i].Notes != want.Notes {
					t.Errorf("answer[%d] notes = %q, want %q", i, got.Answers[i].Notes, want.Notes)
				}
				if got.Answers[i].Outcome != want.Outcome {
					t.Errorf("answer[%d] outcome = %#v, want %#v", i, got.Answers[i].Outcome, want.Outcome)
				}
			}
		})
	}
}

func TestUnmarshalDecisionOutcomeUnknown(t *testing.T) {
	_, err := acp.UnmarshalDecisionOutcome([]byte(`{"outcome":"bogus"}`))
	if err == nil {
		t.Fatal("UnmarshalDecisionOutcome() error = nil, want error for unmodeled outcome")
	}
}
