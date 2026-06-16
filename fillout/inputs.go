package fillout

import "time"

// SubmissionInput is a single submission to create via Client.CreateSubmissions.
type SubmissionInput struct {
	Questions      []QuestionInput   `json:"questions,omitempty"`
	URLParameters  []URLParameter    `json:"urlParameters,omitempty"`
	SubmissionTime *time.Time        `json:"submissionTime,omitempty"`
	LastUpdatedAt  *time.Time        `json:"lastUpdatedAt,omitempty"`
	Scheduling     []SchedulingInput `json:"scheduling,omitempty"`
	Payments       []PaymentInput    `json:"payments,omitempty"`
	Login          *Login            `json:"login,omitempty"`
}

// QuestionInput sets the value of a single question when creating a submission.
// Value is encoded as JSON, so it may be a string, number, bool, slice, etc.,
// matching the question's type.
type QuestionInput struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

// SchedulingInput sets a scheduling field when creating a submission.
type SchedulingInput struct {
	ID    string          `json:"id"`
	Value SchedulingEvent `json:"value"`
}

// PaymentInput sets a payment field when creating a submission.
type PaymentInput struct {
	ID    string  `json:"id"`
	Value Payment `json:"value"`
}
