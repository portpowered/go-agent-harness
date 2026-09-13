package browserscenario

import (
	"encoding/json"
	"io"
	"time"
)

// ErrorCode is a stable, immutable error identity for contract boundaries.
// Constants avoid mutable package state while remaining compatible with
// errors.Is and errors.Join.
type ErrorCode string

func (e ErrorCode) Error() string { return string(e) }

const (
	ErrInvalidBrowserConversationScenario      ErrorCode = "invalid WebMCP browser conversation scenario"
	ErrBrowserConversationRunFinalized         ErrorCode = "WebMCP browser conversation run is finalized"
	ErrBrowserConversationDuplicateObservation ErrorCode = "duplicate WebMCP browser conversation observation"
	ErrInvalidBrowserConversationResult        ErrorCode = "invalid WebMCP browser conversation result"

	ErrBrowserConversationFixtureStartup          ErrorCode = "WebMCP browser conversation fixture startup failed"
	ErrBrowserConversationSessionStartup          ErrorCode = "WebMCP browser conversation session startup failed"
	ErrBrowserConversationSession                 ErrorCode = "WebMCP browser conversation session failed"
	ErrBrowserConversationEvidence                ErrorCode = "WebMCP browser conversation evidence failed"
	ErrBrowserConversationTimeout                 ErrorCode = "WebMCP browser conversation timed out"
	ErrBrowserConversationCleanup                 ErrorCode = "WebMCP browser conversation cleanup failed"
	ErrBrowserConversationValidator               ErrorCode = "WebMCP browser conversation validator failed"
	ErrBrowserConversationSessionBoundaryRequired ErrorCode = "WebMCP browser conversation requires an injected session boundary"

	ErrBrowserConversationValidatorCommand ErrorCode = "browser conversation validator command is invalid"
	ErrBrowserConversationValidatorStart   ErrorCode = "browser conversation validator failed to start"
	ErrBrowserConversationValidatorFailed  ErrorCode = "browser conversation validator command failed"
	ErrBrowserConversationValidatorTimeout ErrorCode = "browser conversation validator command timed out"
	ErrBrowserConversationValidatorOutput  ErrorCode = "browser conversation validator output exceeded bound"
	ErrBrowserConversationValidatorVerdict ErrorCode = "browser conversation validator verdict is invalid"
)

// BrowserConversationValidatorCommand describes an external validator
// boundary. Command, directory, and environment are copied and checked by
// the private implementation before a process is started.
type BrowserConversationValidatorCommand struct {
	Command []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

// Service owns browser-conversation admission, immutable evidence, report
// derivation, and bounded validator orchestration. Hosts depend on this
// contract; constructors and effectful implementation live in services/.../wire
// and services/.../internal/service.
type Service interface {
	AdmitScenario(BrowserConversationScenario) (BrowserConversationScenario, error)
	ScheduleAudioInputs(BrowserConversationScenario, map[string][]byte) ([]ScheduledAudioInput, error)
	NewScenarioValue(BrowserConversationScenario) (BrowserConversationScenarioForSession, error)
	NewRun(BrowserConversationScenario) (*BrowserConversationRun, error)
	ComputeInputJSONValidity([]BrowserConversationBrokerCall) BrowserConversationInputJSONValidity
	SanitizeResult(BrowserConversationResult) BrowserConversationResult
	NewReport(BrowserConversationResult, BrowserConversationReportMetadata) (BrowserConversationReport, error)
	NewValidatorInput(BrowserConversationResult) (BrowserConversationValidatorInput, error)
	RenderReport(BrowserConversationResult, BrowserConversationReportMetadata) (string, error)
	WriteReport(io.Writer, BrowserConversationResult, BrowserConversationReportMetadata) error
	DeriveCorrections(BrowserConversationScenario, BrowserConversationResult) []BrowserConversationCorrectionEvidence
	DeriveRecovery(BrowserConversationScenario, BrowserConversationResult) []BrowserConversationRecoveryEvidence
	Evaluate(BrowserConversationScenario, BrowserConversationResult, error) (BrowserConversationMechanicalEvaluation, error)
	ValidateJSONObject(string, json.RawMessage) error
	NewCommandValidator(BrowserConversationValidatorCommand) (BrowserConversationValidator, error)
}
