// Package probe defines a validated, transport-neutral probe scenario.
package probe

import (
	"encoding/json"
	"errors"
	"fmt"
)

type StepKind string

const (
	StepSendText           StepKind = "send_text"
	StepSendAudio          StepKind = "send_audio"
	StepSendToolResult     StepKind = "send_tool_result"
	StepAdvanceTo          StepKind = "advance_to"
	StepWait               StepKind = "wait"
	StepClose              StepKind = "close"
	StepKindSendText                = StepSendText
	StepKindSendAudio               = StepSendAudio
	StepKindSendToolResult          = StepSendToolResult
	StepKindAdvanceTo               = StepAdvanceTo
	StepKindWait                    = StepWait
	StepKindClose                   = StepClose
)

type StepType = StepKind
type LogicalTime int64

type AudioCorpusReference struct {
	ID       string `json:"-"`
	CorpusID string `json:"corpus_id"`
}
type CorpusReference = AudioCorpusReference

// These payload names are public vocabulary for callers that want to build a
// scenario in Go; Step keeps the same data in serializable scalar fields.
type SendText struct{ Text string }
type SendAudio struct {
	Corpus   AudioCorpusReference
	CorpusID string
}
type SendToolResult struct {
	ToolCallID, ToolName string
	Result               json.RawMessage
}
type AdvanceTo struct{ At, Time LogicalTime }
type Wait struct{ Duration LogicalTime }
type Close struct{}

type Step struct {
	Type       StepKind             `json:"type"`
	Kind       StepKind             `json:"-"`
	CorpusID   string               `json:"corpus_id,omitempty"`
	Text       string               `json:"text,omitempty"`
	Corpus     AudioCorpusReference `json:"-"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolName   string               `json:"tool_name,omitempty"`
	ToolResult json.RawMessage      `json:"-"`
	Result     json.RawMessage      `json:"result,omitempty"`
	At         LogicalTime          `json:"at,omitempty"`
	Time       LogicalTime          `json:"-"`
	Duration   LogicalTime          `json:"duration,omitempty"`
}

type ExpectationKind string

const (
	ExpectText       ExpectationKind = "text"
	ExpectAudio      ExpectationKind = "audio"
	ExpectToolCall   ExpectationKind = "tool_call"
	ExpectToolResult ExpectationKind = "tool_result"
	ExpectClose      ExpectationKind = "close"
	ExpectTime       ExpectationKind = "time"
	ExpectEvent      ExpectationKind = "event"
	ExpectContains   ExpectationKind = "contains"
	ExpectTranscript ExpectationKind = "transcript"
)

type ExpectedBehavior struct {
	Type       ExpectationKind `json:"type"`
	Kind       ExpectationKind `json:"-"`
	Text       string          `json:"text,omitempty"`
	Value      string          `json:"value,omitempty"`
	CorpusID   string          `json:"corpus_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	At         LogicalTime     `json:"at,omitempty"`
	Time       LogicalTime     `json:"-"`
	Count      int             `json:"count,omitempty"`
	StepIndex  int             `json:"-"`
	Step       int             `json:"-"`
	HasStep    bool            `json:"-"`
	AfterStep  int             `json:"-"`
	BeforeStep int             `json:"-"`
	HasAfter   bool            `json:"-"`
	HasBefore  bool            `json:"-"`
	HasAt      bool            `json:"-"`
}
type Expectation = ExpectedBehavior
type ExpectedKind = ExpectationKind

type Scenario struct {
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	Description      string             `json:"description,omitempty"`
	Steps            []Step             `json:"steps"`
	Expectations     []ExpectedBehavior `json:"expectations"`
	Expected         []ExpectedBehavior `json:"-"`
	ExpectedBehavior []ExpectedBehavior `json:"-"`
}

type ErrorCategory string

const (
	CategoryMalformed      ErrorCategory = "malformed"
	CategoryEmpty          ErrorCategory = "empty"
	CategoryMissingField   ErrorCategory = "missing_field"
	CategoryInvalidField   ErrorCategory = "invalid_field"
	CategoryUnknownVariant ErrorCategory = "unknown_variant"
	CategoryUnknownCorpus  ErrorCategory = "unknown_corpus"
	CategoryContradictory  ErrorCategory = "contradictory"
	CategoryUnsatisfiable  ErrorCategory = "unsatisfiable"
)

type ErrorKind = ErrorCategory

var (
	ErrMalformed         = errors.New("malformed scenario")
	ErrEmptyScenario     = errors.New("empty scenario")
	ErrMissingField      = errors.New("missing scenario field")
	ErrInvalidField      = errors.New("invalid scenario field")
	ErrUnknownVariant    = errors.New("unknown scenario variant")
	ErrUnknownCorpus     = errors.New("unknown audio corpus")
	ErrContradictory     = errors.New("contradictory scenario")
	ErrUnsatisfiable     = errors.New("unsatisfiable expectation")
	ErrMalformedScenario = ErrMalformed
	ErrUnknownCorpusID   = ErrUnknownCorpus
)

type ScenarioError struct {
	Category    ErrorCategory
	Kind        ErrorCategory
	Location    string
	Message     string
	CorpusID    string
	StepIndex   int
	Expectation int
}

func makeError(category ErrorCategory, location, format string, args ...any) *ScenarioError {
	return &ScenarioError{Category: category, Kind: category, Location: location,
		Message: fmt.Sprintf(format, args...), StepIndex: -1, Expectation: -1}
}
func (e *ScenarioError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Location == "" {
		return fmt.Sprintf("%s: %s", e.Category, e.Message)
	}
	return fmt.Sprintf("%s at %s: %s", e.Category, e.Location, e.Message)
}
func (e *ScenarioError) Unwrap() error {
	switch e.Category {
	case CategoryMalformed:
		return ErrMalformed
	case CategoryEmpty:
		return ErrEmptyScenario
	case CategoryMissingField:
		return ErrMissingField
	case CategoryInvalidField:
		return ErrInvalidField
	case CategoryUnknownVariant:
		return ErrUnknownVariant
	case CategoryUnknownCorpus:
		return ErrUnknownCorpus
	case CategoryContradictory:
		return ErrContradictory
	case CategoryUnsatisfiable:
		return ErrUnsatisfiable
	default:
		return nil
	}
}

// CorpusLookup is the read-only corpus identity lookup used by the loader.
// The audiofixture package is not part of the current baseline, so this is the
// narrow seam that the eventual audiofixture implementation must satisfy.
type CorpusLookup interface{ Has(string) bool }

func Load(input any, lookups ...CorpusLookup) (Scenario, error) {
	if len(lookups) > 1 {
		return Scenario{}, makeError(CategoryInvalidField, "corpus_lookup", "only one corpus lookup is permitted")
	}
	data, err := readInput(input)
	if err != nil {
		return Scenario{}, makeError(CategoryMalformed, "document", "%v", err)
	}
	value, err := decodeObject(data)
	if err != nil {
		return Scenario{}, err
	}
	scenario, err := parseScenario(value)
	if err != nil {
		return Scenario{}, err
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	if err := scenario.validate(lookup); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}
func LoadScenario(input any, lookups ...CorpusLookup) (Scenario, error) {
	return Load(input, lookups...)
}
func Decode(input any, lookups ...CorpusLookup) (Scenario, error) { return Load(input, lookups...) }
func (s Scenario) Validate(lookups ...CorpusLookup) error {
	if len(lookups) > 1 {
		return makeError(CategoryInvalidField, "corpus_lookup", "only one corpus lookup is permitted")
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	return s.validate(lookup)
}
func (s Scenario) Valid() bool { return s.Validate() == nil }
func (s Scenario) expectedValues() []ExpectedBehavior {
	if s.Expectations != nil {
		return s.Expectations
	}
	if s.ExpectedBehavior != nil {
		return s.ExpectedBehavior
	}
	return s.Expected
}
