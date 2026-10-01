package browserconversation

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// Error is a stable, immutable error identity for contract boundaries.
// Constants avoid mutable package state while remaining compatible with
// errors.Is and errors.Join.
type Error string

func (e Error) Error() string { return string(e) }

const (
	ErrInvalidBrowserConversationScenario      Error = "invalid WebMCP browser conversation scenario"
	ErrBrowserConversationRunFinalized         Error = "WebMCP browser conversation run is finalized"
	ErrBrowserConversationDuplicateObservation Error = "duplicate WebMCP browser conversation observation"
	ErrInvalidBrowserConversationResult        Error = "invalid WebMCP browser conversation result"

	ErrBrowserConversationFixtureStartup          Error = "WebMCP browser conversation fixture startup failed"
	ErrBrowserConversationSessionStartup          Error = "WebMCP browser conversation session startup failed"
	ErrBrowserConversationSession                 Error = "WebMCP browser conversation session failed"
	ErrBrowserConversationEvidence                Error = "WebMCP browser conversation evidence failed"
	ErrBrowserConversationTimeout                 Error = "WebMCP browser conversation timed out"
	ErrBrowserConversationCleanup                 Error = "WebMCP browser conversation cleanup failed"
	ErrBrowserConversationValidator               Error = "WebMCP browser conversation validator failed"
	ErrBrowserConversationSessionBoundaryRequired Error = "WebMCP browser conversation requires an injected session boundary"

	ErrBrowserConversationValidatorCommand Error = "browser conversation validator command is invalid"
	ErrBrowserConversationValidatorStart   Error = "browser conversation validator failed to start"
	ErrBrowserConversationValidatorFailed  Error = "browser conversation validator command failed"
	ErrBrowserConversationValidatorTimeout Error = "browser conversation validator command timed out"
	ErrBrowserConversationValidatorOutput  Error = "browser conversation validator output exceeded bound"
	ErrBrowserConversationValidatorVerdict Error = "browser conversation validator verdict is invalid"
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
	ParseScenarioJSON([]byte) (BrowserConversationScenario, error)
	Run(context.Context, RunRequest) (BrowserConversationResult, error)
	ValidateScenario(BrowserConversationScenario) (BrowserConversationScenario, error)
	AdmitScenario(BrowserConversationScenario) (BrowserConversationScenario, error)
	ScheduleAudioInputs(BrowserConversationScenario, map[string][]byte) ([]ScheduledAudioInput, error)
	NewScenarioValue(BrowserConversationScenario) (BrowserConversationScenarioForSession, error)
	NewRun(BrowserConversationScenario) (Run, error)
	ValidateResult(BrowserConversationResult) error
	ComputeInputJSONValidity([]BrowserConversationBrokerCall) BrowserConversationInputJSONValidity
	SanitizeResult(BrowserConversationResult) BrowserConversationResult
	NewReport(BrowserConversationResult, BrowserConversationReportMetadata) (BrowserConversationReport, error)
	Report(BrowserConversationResult, BrowserConversationReportMetadata) (BrowserConversationReport, error)
	NewValidatorInput(BrowserConversationResult) (BrowserConversationValidatorInput, error)
	RenderReport(BrowserConversationResult, BrowserConversationReportMetadata) (string, error)
	WriteReport(io.Writer, BrowserConversationResult, BrowserConversationReportMetadata) error
	DeriveCorrections(BrowserConversationScenario, BrowserConversationResult) []BrowserConversationCorrectionEvidence
	DeriveRecovery(BrowserConversationScenario, BrowserConversationResult) []BrowserConversationRecoveryEvidence
	Evaluate(BrowserConversationScenario, BrowserConversationResult, error) (BrowserConversationMechanicalEvaluation, error)
	ValidateJSONObject(string, json.RawMessage) error
	NewCommandValidator(BrowserConversationValidatorCommand) (BrowserConversationValidator, error)
	NewRecorder(RecordingRequest) (Recorder, error)
}
