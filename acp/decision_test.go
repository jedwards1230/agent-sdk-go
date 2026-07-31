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

// TestDecisionQuestionUnmarshalJSON decodes a hand-written, independently
// spelled JSON literal and asserts on the resulting fields one by one. This is
// deliberately not a marshal->unmarshal round trip: a round trip passes even
// when both sides of a would-be typo agree with each other, so it can't catch
// an unmarshal-side key that has drifted from the marshal-side key. Only a
// raw literal written independently of the Marshal call exercises that.
func TestDecisionQuestionUnmarshalJSON(t *testing.T) {
	raw := `{
		"questionId": "q-9",
		"title": "Rollback",
		"question": "Roll back the last deploy?",
		"context": "Error rate spiked after deploy 42.",
		"options": [
			{"optionId": "opt-yes", "label": "Roll back", "rationale": "Error rate is climbing.", "reference": "deploy/42", "recommended": true},
			{"optionId": "opt-no", "label": "Hold"}
		],
		"allowFreeText": true,
		"allowChat": true
	}`

	var got acp.DecisionQuestion
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.QuestionID != "q-9" {
		t.Errorf("QuestionID = %q, want %q", got.QuestionID, "q-9")
	}
	if got.Title != "Rollback" {
		t.Errorf("Title = %q, want %q", got.Title, "Rollback")
	}
	if got.Question != "Roll back the last deploy?" {
		t.Errorf("Question = %q, want %q", got.Question, "Roll back the last deploy?")
	}
	if got.Context != "Error rate spiked after deploy 42." {
		t.Errorf("Context = %q, want %q", got.Context, "Error rate spiked after deploy 42.")
	}
	if !got.AllowFreeText {
		t.Error("AllowFreeText = false, want true")
	}
	if !got.AllowChat {
		t.Error("AllowChat = false, want true")
	}
	if len(got.Options) != 2 {
		t.Fatalf("len(Options) = %d, want 2", len(got.Options))
	}
	wantOpt0 := acp.DecisionOption{OptionID: "opt-yes", Label: "Roll back", Rationale: "Error rate is climbing.", Reference: "deploy/42", Recommended: true}
	if got.Options[0] != wantOpt0 {
		t.Errorf("Options[0] = %#v, want %#v", got.Options[0], wantOpt0)
	}
	wantOpt1 := acp.DecisionOption{OptionID: "opt-no", Label: "Hold"}
	if got.Options[1] != wantOpt1 {
		t.Errorf("Options[1] = %#v, want %#v", got.Options[1], wantOpt1)
	}
}

// TestDecisionQuestionUnmarshalJSONOmittedFields confirms an absent options
// field decodes to a nil slice (mirroring encoding/json's default behavior,
// since UnmarshalJSON adds no special handling on the decode side — only
// MarshalJSON normalizes nil to "[]" on encode).
func TestDecisionQuestionUnmarshalJSONOmittedFields(t *testing.T) {
	raw := `{"questionId":"q-1","title":"Freeform","question":"What should I do?"}`

	var got acp.DecisionQuestion
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.QuestionID != "q-1" || got.Title != "Freeform" || got.Question != "What should I do?" {
		t.Errorf("got = %#v, want QuestionID=q-1 Title=Freeform Question=%q", got, "What should I do?")
	}
	if got.Options != nil {
		t.Errorf("Options = %#v, want nil", got.Options)
	}
	if got.AllowFreeText || got.AllowChat {
		t.Errorf("AllowFreeText/AllowChat = %v/%v, want false/false", got.AllowFreeText, got.AllowChat)
	}
}

// TestRequestDecisionRequestUnmarshalJSON decodes a hand-written JSON literal
// (independently spelled from the Marshal call) and asserts field by field,
// for the same reason as TestDecisionQuestionUnmarshalJSON above.
func TestRequestDecisionRequestUnmarshalJSON(t *testing.T) {
	raw := `{
		"sessionId": "sess-7",
		"questions": [
			{"questionId": "q-1", "title": "Deploy", "question": "Deploy now or wait?", "options": [
				{"optionId": "now", "label": "Deploy now"}
			]}
		]
	}`

	var got acp.RequestDecisionRequest
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.SessionID != "sess-7" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "sess-7")
	}
	if len(got.Questions) != 1 {
		t.Fatalf("len(Questions) = %d, want 1", len(got.Questions))
	}
	q := got.Questions[0]
	if q.QuestionID != "q-1" {
		t.Errorf("Questions[0].QuestionID = %q, want %q", q.QuestionID, "q-1")
	}
	if q.Title != "Deploy" {
		t.Errorf("Questions[0].Title = %q, want %q", q.Title, "Deploy")
	}
	if q.Question != "Deploy now or wait?" {
		t.Errorf("Questions[0].Question = %q, want %q", q.Question, "Deploy now or wait?")
	}
	if len(q.Options) != 1 || q.Options[0] != (acp.DecisionOption{OptionID: "now", Label: "Deploy now"}) {
		t.Errorf("Questions[0].Options = %#v, want [{now Deploy now}]", q.Options)
	}
}

func TestUnmarshalDecisionOutcomeUnknown(t *testing.T) {
	_, err := acp.UnmarshalDecisionOutcome([]byte(`{"outcome":"bogus"}`))
	if err == nil {
		t.Fatal("UnmarshalDecisionOutcome() error = nil, want error for unmodeled outcome")
	}
}
