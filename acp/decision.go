package acp

import (
	"encoding/json"
	"fmt"
)

// DecisionOption is one enumerable choice offered for a [DecisionQuestion].
// Unlike a [PermissionOption] (an allow/deny verdict on a tool call), it is a
// general answer that carries an inline rationale the client renders beneath the
// label, so the user can decide without scrolling back through the transcript.
type DecisionOption struct {
	// OptionID identifies the option; a [DecisionOutcomeSelected] answer
	// references it, not the label.
	OptionID string `json:"optionId"`
	// Label is a short human-readable label for the choice.
	Label string `json:"label"`
	// Rationale is an optional indented explanatory body (reasoning/risk) the
	// client renders beneath the label.
	Rationale string `json:"rationale,omitempty"`
	// Reference is an optional opaque, client-rendered locator for supporting
	// material behind the choice.
	Reference string `json:"reference,omitempty"`
	// Recommended marks the option the client renders as "(Recommended)".
	Recommended bool `json:"recommended,omitempty"`
}

// DecisionQuestion is one titled question with N [DecisionOption] choices. A
// single-question prompt is a slice of length one; a multi-question prompt
// batches several so an agent needing several sign-offs asks only once.
type DecisionQuestion struct {
	// QuestionID is a stable id correlating a [DecisionAnswer] to its question.
	QuestionID string
	// Title is a short chip label for the decision.
	Title string
	// Question is the question text.
	Question string
	// Context is optional supporting context for a side panel.
	Context string
	// Options are the choices offered; a nil slice marshals to "[]" (a question
	// may legitimately carry zero options when only free text is offered).
	Options []DecisionOption
	// AllowFreeText offers a free-text "type something" answer.
	AllowFreeText bool
	// AllowChat offers the "chat about this" escape hatch.
	AllowChat bool
}

// MarshalJSON encodes {questionId, title, question, context?, options,
// allowFreeText?, allowChat?}. A nil [DecisionQuestion.Options] marshals to
// "[]" so a client can distinguish a free-text-only question from an absent
// field.
func (q DecisionQuestion) MarshalJSON() ([]byte, error) {
	options := q.Options
	if options == nil {
		options = []DecisionOption{}
	}
	return json.Marshal(struct {
		QuestionID    string           `json:"questionId"`
		Title         string           `json:"title"`
		Question      string           `json:"question"`
		Context       string           `json:"context,omitempty"`
		Options       []DecisionOption `json:"options"`
		AllowFreeText bool             `json:"allowFreeText,omitempty"`
		AllowChat     bool             `json:"allowChat,omitempty"`
	}{q.QuestionID, q.Title, q.Question, q.Context, options, q.AllowFreeText, q.AllowChat})
}

// RequestDecisionRequest is the payload of a session/request_decision request,
// sent agent-to-client to ask the user one or more structured questions. It is
// distinct from a [RequestPermissionRequest]: not a policy gate on a tool call,
// but a general question set.
type RequestDecisionRequest struct {
	// SessionID identifies the session the questions belong to.
	SessionID string
	// Questions are the questions to answer; a nil slice marshals to "[]".
	Questions []DecisionQuestion
}

// MarshalJSON encodes {"sessionId":...,"questions":[...]}. A nil
// [RequestDecisionRequest.Questions] marshals to "[]".
func (r RequestDecisionRequest) MarshalJSON() ([]byte, error) {
	questions := r.Questions
	if questions == nil {
		questions = []DecisionQuestion{}
	}
	return json.Marshal(struct {
		SessionID string             `json:"sessionId"`
		Questions []DecisionQuestion `json:"questions"`
	}{r.SessionID, questions})
}

// DecisionOutcome is the tagged union carried by a [DecisionAnswer]: the client
// selected an option, typed a free-text answer, asked to chat instead, or the
// question was left unanswered (e.g. the batch was cancelled).
type DecisionOutcome interface {
	// Outcome returns the variant's "outcome" discriminator value.
	Outcome() string

	json.Marshaler
}

// DecisionOutcomeSelected reports the option the client chose.
type DecisionOutcomeSelected struct {
	// OptionID is the chosen [DecisionOption.OptionID].
	OptionID string
}

// Outcome returns "selected".
func (DecisionOutcomeSelected) Outcome() string { return "selected" }

// MarshalJSON encodes {"outcome":"selected","optionId":...}.
func (o DecisionOutcomeSelected) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	}{o.Outcome(), o.OptionID})
}

// DecisionOutcomeText reports the free-text answer the client typed.
type DecisionOutcomeText struct {
	// Text is the free-text answer.
	Text string
}

// Outcome returns "text".
func (DecisionOutcomeText) Outcome() string { return "text" }

// MarshalJSON encodes {"outcome":"text","text":...}.
func (o DecisionOutcomeText) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome string `json:"outcome"`
		Text    string `json:"text"`
	}{o.Outcome(), o.Text})
}

// DecisionOutcomeChat reports that the client chose the "none of these, let's
// talk" escape hatch instead of answering.
type DecisionOutcomeChat struct{}

// Outcome returns "chat".
func (DecisionOutcomeChat) Outcome() string { return "chat" }

// MarshalJSON encodes {"outcome":"chat"}.
func (o DecisionOutcomeChat) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome string `json:"outcome"`
	}{o.Outcome()})
}

// DecisionOutcomeCancelled reports that the question was left unanswered before
// the client resolved it (e.g. the batch was cancelled).
type DecisionOutcomeCancelled struct{}

// Outcome returns "cancelled".
func (DecisionOutcomeCancelled) Outcome() string { return "cancelled" }

// MarshalJSON encodes {"outcome":"cancelled"}.
func (o DecisionOutcomeCancelled) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Outcome string `json:"outcome"`
	}{o.Outcome()})
}

// UnmarshalDecisionOutcome decodes a [DecisionOutcome] from its "outcome"
// discriminator.
func UnmarshalDecisionOutcome(data []byte) (DecisionOutcome, error) {
	var disc struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return nil, fmt.Errorf("acp: decode decision outcome: %w", err)
	}
	switch disc.Outcome {
	case "selected":
		var v struct {
			OptionID string `json:"optionId"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode selected outcome: %w", err)
		}
		return DecisionOutcomeSelected{OptionID: v.OptionID}, nil
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("acp: decode text outcome: %w", err)
		}
		return DecisionOutcomeText{Text: v.Text}, nil
	case "chat":
		return DecisionOutcomeChat{}, nil
	case "cancelled":
		return DecisionOutcomeCancelled{}, nil
	default:
		return nil, fmt.Errorf("acp: unknown decision outcome %q", disc.Outcome)
	}
}

// DecisionAnswer is the client's answer to one [DecisionQuestion], correlated by
// QuestionID, with an optional free-text note attached.
type DecisionAnswer struct {
	// QuestionID is the answered [DecisionQuestion.QuestionID].
	QuestionID string
	// Outcome is the resolved outcome.
	Outcome DecisionOutcome
	// Notes is an optional free-text note the client attached to the answer.
	Notes string
}

// MarshalJSON encodes {"questionId":...,"outcome":...,"notes"?:...}.
func (a DecisionAnswer) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		QuestionID string          `json:"questionId"`
		Outcome    DecisionOutcome `json:"outcome"`
		Notes      string          `json:"notes,omitempty"`
	}{a.QuestionID, a.Outcome, a.Notes})
}

// UnmarshalJSON decodes {"questionId":...,"outcome":...,"notes"?:...}, resolving
// the outcome's concrete [DecisionOutcome] variant.
func (a *DecisionAnswer) UnmarshalJSON(data []byte) error {
	var wire struct {
		QuestionID string          `json:"questionId"`
		Outcome    json.RawMessage `json:"outcome"`
		Notes      string          `json:"notes"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode DecisionAnswer: %w", err)
	}
	outcome, err := UnmarshalDecisionOutcome(wire.Outcome)
	if err != nil {
		return err
	}
	a.QuestionID = wire.QuestionID
	a.Outcome = outcome
	a.Notes = wire.Notes
	return nil
}

// RequestDecisionResponse is the payload of a session/request_decision response,
// sent client-to-agent with one [DecisionAnswer] per question. Unanswered
// questions in a batch are represented by a [DecisionOutcomeCancelled] answer
// (or omitted).
type RequestDecisionResponse struct {
	// Answers is one answer per question; a nil slice marshals to "[]".
	Answers []DecisionAnswer
}

// MarshalJSON encodes {"answers":[...]}. A nil
// [RequestDecisionResponse.Answers] marshals to "[]".
func (r RequestDecisionResponse) MarshalJSON() ([]byte, error) {
	answers := r.Answers
	if answers == nil {
		answers = []DecisionAnswer{}
	}
	return json.Marshal(struct {
		Answers []DecisionAnswer `json:"answers"`
	}{answers})
}

// UnmarshalJSON decodes {"answers":[...]}, resolving each answer's concrete
// [DecisionOutcome] variant.
func (r *RequestDecisionResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Answers []DecisionAnswer `json:"answers"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("acp: decode RequestDecisionResponse: %w", err)
	}
	r.Answers = wire.Answers
	return nil
}

// ToRequestDecision builds a session/request_decision request carrying one or
// more structured questions for a client to answer.
func ToRequestDecision(sessionID string, questions []DecisionQuestion) RequestDecisionRequest {
	return RequestDecisionRequest{SessionID: sessionID, Questions: questions}
}
