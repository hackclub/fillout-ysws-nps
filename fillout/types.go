package fillout

import (
	"encoding/json"
	"time"
)

// Form is a summary of a form as returned by Client.ListForms.
type Form struct {
	// ID is Fillout's internal numeric identifier for the form.
	ID int `json:"id"`
	// FormID is the public identifier used in all other API calls.
	FormID string `json:"formId"`
	// Name is the form's display name.
	Name string `json:"name"`
	// Tags are the labels applied to the form, if any.
	Tags []string `json:"tags,omitempty"`
	// IsPublished reports whether the form is currently published.
	IsPublished bool `json:"isPublished"`
}

// FormMetadata describes a single form and its fields, as returned by
// Client.GetForm.
type FormMetadata struct {
	// ID is the public identifier of the form (same as Form.FormID).
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Tags          []string          `json:"tags,omitempty"`
	Questions     []QuestionDef     `json:"questions"`
	Calculations  []CalculationDef  `json:"calculations,omitempty"`
	URLParameters []URLParameterDef `json:"urlParameters,omitempty"`
	Scheduling    []SchedulingDef   `json:"scheduling,omitempty"`
	Payments      []PaymentDef      `json:"payments,omitempty"`
	// Documents holds document-generation configs. Its shape is left raw so
	// callers can decode it without the package losing data on API changes.
	Documents []json.RawMessage `json:"documents,omitempty"`
	Quiz      *QuizConfig       `json:"quiz,omitempty"`
}

// QuestionType enumerates the question/field types Fillout supports.
type QuestionType string

// The set of question types documented by the Fillout API. Fillout may add
// new types over time; treat QuestionType as an open enum and compare against
// these constants rather than assuming exhaustiveness.
const (
	QuestionAddress             QuestionType = "Address"
	QuestionAudioRecording      QuestionType = "AudioRecording"
	QuestionCalcom              QuestionType = "Calcom"
	QuestionCalendly            QuestionType = "Calendly"
	QuestionCaptcha             QuestionType = "Captcha"
	QuestionCheckbox            QuestionType = "Checkbox"
	QuestionCheckboxes          QuestionType = "Checkboxes"
	QuestionColorPicker         QuestionType = "ColorPicker"
	QuestionCurrencyInput       QuestionType = "CurrencyInput"
	QuestionDatePicker          QuestionType = "DatePicker"
	QuestionDateRange           QuestionType = "DateRange"
	QuestionDateTimePicker      QuestionType = "DateTimePicker"
	QuestionDropdown            QuestionType = "Dropdown"
	QuestionEmailInput          QuestionType = "EmailInput"
	QuestionFileUpload          QuestionType = "FileUpload"
	QuestionImagePicker         QuestionType = "ImagePicker"
	QuestionLocationCoordinates QuestionType = "LocationCoordinates"
	QuestionLongAnswer          QuestionType = "LongAnswer"
	QuestionMatrix              QuestionType = "Matrix"
	QuestionMultiSelect         QuestionType = "MultiSelect"
	QuestionMultipleChoice      QuestionType = "MultipleChoice"
	QuestionNumberInput         QuestionType = "NumberInput"
	QuestionOpinionScale        QuestionType = "OpinionScale"
	QuestionPassword            QuestionType = "Password"
	QuestionPayment             QuestionType = "Payment"
	QuestionPhoneNumber         QuestionType = "PhoneNumber"
	QuestionRanking             QuestionType = "Ranking"
	QuestionRecordPicker        QuestionType = "RecordPicker"
	QuestionShortAnswer         QuestionType = "ShortAnswer"
	QuestionSignature           QuestionType = "Signature"
	QuestionSlider              QuestionType = "Slider"
	QuestionStarRating          QuestionType = "StarRating"
	QuestionSubform             QuestionType = "Subform"
	QuestionSubmissionPicker    QuestionType = "SubmissionPicker"
	QuestionSwitch              QuestionType = "Switch"
	QuestionTable               QuestionType = "Table"
	QuestionTimePicker          QuestionType = "TimePicker"
	QuestionURLInput            QuestionType = "URLInput"
)

// QuestionDef describes a question/field in a form's metadata.
type QuestionDef struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Type QuestionType `json:"type"`
}

// CalculationType enumerates the result types of a form calculation.
type CalculationType string

const (
	CalculationNumber   CalculationType = "number"
	CalculationText     CalculationType = "text"
	CalculationDuration CalculationType = "duration"
)

// CalculationDef describes a calculation field in a form's metadata.
type CalculationDef struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Type CalculationType `json:"type"`
}

// URLParameterDef describes a URL parameter captured by a form.
type URLParameterDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SchedulingDef describes a scheduling field in a form's metadata.
type SchedulingDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PaymentDef describes a payment field in a form's metadata.
type PaymentDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// QuizConfig reports whether quiz mode is enabled on a form.
type QuizConfig struct {
	Enabled bool `json:"enabled"`
}

// Submission is a single form response.
type Submission struct {
	SubmissionID   string             `json:"submissionId"`
	SubmissionTime time.Time          `json:"submissionTime"`
	LastUpdatedAt  time.Time          `json:"lastUpdatedAt"`
	StartedAt      *time.Time         `json:"startedAt,omitempty"`
	Questions      []QuestionAnswer   `json:"questions"`
	Calculations   []CalculationValue `json:"calculations,omitempty"`
	URLParameters  []URLParameter     `json:"urlParameters,omitempty"`
	Scheduling     []SchedulingValue  `json:"scheduling,omitempty"`
	Payments       []PaymentValue     `json:"payments,omitempty"`
	Documents      []json.RawMessage  `json:"documents,omitempty"`
	Quiz           *QuizResult        `json:"quiz,omitempty"`
	Login          *Login             `json:"login,omitempty"`
	// EditLink is only populated when the request opts into it via
	// GetSubmissionsParams.IncludeEditLink / GetSubmissionParams.IncludeEditLink.
	EditLink string `json:"editLink,omitempty"`
}

// QuestionAnswer is a single answer within a submission. Value is polymorphic
// (string, number, bool, array, or object depending on the question type) and
// is kept as raw JSON; use the As* helpers or Decode to read it.
type QuestionAnswer struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Type  QuestionType    `json:"type"`
	Value json.RawMessage `json:"value"`
}

// CalculationValue is a calculation result within a submission.
type CalculationValue struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Type  CalculationType `json:"type"`
	Value string          `json:"value"`
}

// URLParameter is a captured URL parameter, used both in submissions and as
// input when creating submissions.
type URLParameter struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value"`
}

// SchedulingValue is a scheduling answer within a submission.
type SchedulingValue struct {
	ID    string          `json:"id"`
	Name  string          `json:"name,omitempty"`
	Value SchedulingEvent `json:"value"`
}

// SchedulingEvent holds the details of a scheduled meeting.
type SchedulingEvent struct {
	FullName              string     `json:"fullName,omitempty"`
	Email                 string     `json:"email,omitempty"`
	Timezone              string     `json:"timezone,omitempty"`
	EventStartTime        *time.Time `json:"eventStartTime,omitempty"`
	EventEndTime          *time.Time `json:"eventEndTime,omitempty"`
	EventID               string     `json:"eventId,omitempty"`
	EventURL              string     `json:"eventUrl,omitempty"`
	RescheduleOrCancelURL string     `json:"rescheduleOrCancelUrl,omitempty"`
	UserID                *int       `json:"userId,omitempty"`
	ScheduledUserEmail    string     `json:"scheduledUserEmail,omitempty"`
	MeetingNotes          string     `json:"meetingNotes,omitempty"`
}

// PaymentValue is a payment answer within a submission.
type PaymentValue struct {
	ID    string  `json:"id"`
	Name  string  `json:"name,omitempty"`
	Value Payment `json:"value"`
}

// Payment holds the details of a payment captured by a form.
type Payment struct {
	PaymentID            string `json:"paymentId,omitempty"`
	StripeCustomerID     string `json:"stripeCustomerId,omitempty"`
	StripeCustomerURL    string `json:"stripeCustomerUrl,omitempty"`
	StripePaymentURL     string `json:"stripePaymentUrl,omitempty"`
	TotalAmount          *int   `json:"totalAmount,omitempty"`
	Currency             string `json:"currency,omitempty"`
	Email                string `json:"email,omitempty"`
	DiscountCode         string `json:"discountCode,omitempty"`
	Status               string `json:"status,omitempty"`
	StripeSubscriptionID string `json:"stripeSubscriptionId,omitempty"`
}

// QuizResult holds a submission's quiz score. Fields are zero when the form is
// not a quiz.
type QuizResult struct {
	Score    int `json:"score"`
	MaxScore int `json:"maxScore"`
}

// Login holds the authenticated email associated with a submission.
type Login struct {
	Email string `json:"email"`
}

// Decode unmarshals the raw answer value into v.
func (a QuestionAnswer) Decode(v any) error {
	return json.Unmarshal(a.Value, v)
}

// AsString decodes the answer value as a string.
func (a QuestionAnswer) AsString() (string, error) {
	var s string
	err := json.Unmarshal(a.Value, &s)
	return s, err
}

// AsStringSlice decodes the answer value as a slice of strings, as produced by
// multi-select question types.
func (a QuestionAnswer) AsStringSlice() ([]string, error) {
	var s []string
	err := json.Unmarshal(a.Value, &s)
	return s, err
}

// AsFloat decodes the answer value as a float64.
func (a QuestionAnswer) AsFloat() (float64, error) {
	var f float64
	err := json.Unmarshal(a.Value, &f)
	return f, err
}

// AsBool decodes the answer value as a bool.
func (a QuestionAnswer) AsBool() (bool, error) {
	var b bool
	err := json.Unmarshal(a.Value, &b)
	return b, err
}
