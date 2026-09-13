// Package service contains the private session-failure policy and state.
package service

import (
	"errors"
	"strconv"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

var _ sessionfailure.Service = (*Service)(nil)

const streamErrorMessage = "session stream error"

// Service owns one invocation's accepted failure and never retains provider
// values or callback-owned maps.
type Service struct {
	mu           sync.Mutex
	accepted     *sessionfailure.Observation
	dependencies sessionfailure.Dependencies
}

// New constructs an inert, invocation-local failure service.
func New(dependencies sessionfailure.Dependencies) *Service {
	return &Service{dependencies: dependencies}
}

func (s *Service) NormalizeErrorValue(v *messages.ErrorValue) (sessionfailure.Facts, error) {
	if v == nil || v.IsNonTerminal() || ignoredCancellation(v) {
		return sessionfailure.Facts{}, nil
	}
	facts := sessionfailure.Facts{
		Classification: v.Classification,
		TerminalReason: string(v.TerminalReason),
		Provenance:     string(v.TerminalProvenance),
		OutputState:    string(v.OutputState),
		ErrorType:      v.ErrorType,
		Code:           v.Code,
		FailingEvent:   string(messages.StreamTypeError),
	}
	if facts.Classification == "" {
		facts.Classification = sessionfailure.ErrorClassUnknown
	}
	if facts.TerminalReason == "" {
		facts.TerminalReason = string(messages.TerminalReasonTerminalFailure)
	}
	if facts.Provenance == "" {
		facts.Provenance = string(messages.TerminalProvenanceProvider)
	}
	if facts.OutputState == "" {
		facts.OutputState = string(messages.TerminalOutputNone)
	}
	var err error = v.Err
	if err == nil && v.Message != "" {
		err = errors.New(v.Message)
	}
	return facts, err
}

func ignoredCancellation(v *messages.ErrorValue) bool {
	return v.TerminalReason == messages.TerminalReasonCancellation ||
		(v.Classification == sessionfailure.ErrorClassCancellation && v.TerminalReason == "") ||
		v.Classification == sessionfailure.ErrorClassRoomBoundCancelled
}

func (s *Service) FactsFromSessionRunError(err error) *sessionfailure.Facts {
	if err == nil {
		return nil
	}
	var delta *engine.StreamDeltaError
	if !errors.As(err, &delta) || delta == nil || delta.Value == nil {
		return nil
	}
	facts, _ := s.NormalizeErrorValue(delta.Value)
	if facts.FailingEvent == "" {
		return nil
	}
	return &facts
}

func (s *Service) NormalizeClose(v *messages.SessionCloseValue, progress sessionfailure.Progress) sessionfailure.Facts {
	if v == nil {
		return sessionfailure.Facts{}
	}
	providerClose := v.TerminalReason == messages.TerminalReasonProviderClose
	if providerClose && v.Reason != "provider_closed" {
		return sessionfailure.Facts{}
	}
	switch v.TerminalReason {
	case messages.TerminalReasonProviderClose,
		messages.TerminalReasonTerminalFailure,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
	default:
		return sessionfailure.Facts{}
	}
	facts := sessionfailure.Facts{
		Classification: v.Classification,
		TerminalReason: string(v.TerminalReason),
		Provenance:     string(v.TerminalProvenance),
		OutputState:    string(v.OutputState),
		FailingEvent:   string(messages.StreamTypeSessionClose),
	}
	if facts.Classification == "" {
		facts.Classification = sessionfailure.ErrorClassUnknown
	}
	if facts.Provenance == "" {
		facts.Provenance = string(messages.TerminalProvenanceSession)
	}
	if facts.OutputState == "" || providerClose {
		facts.OutputState = s.OutputState(progress)
	}
	return facts
}

func (s *Service) Accept(facts sessionfailure.Facts, err error) bool {
	if s == nil || facts.FailingEvent == "" {
		return false
	}
	observation := sessionfailure.Observation{Facts: facts, Err: effectiveError(facts, err)}
	s.mu.Lock()
	if s.accepted != nil {
		s.mu.Unlock()
		return false
	}
	s.accepted = &observation
	accepted := s.accepted
	s.mu.Unlock()
	if s.dependencies.Publish != nil && !s.dependencies.Publish(clone(*accepted)) {
		s.rollback(accepted)
		return false
	}
	return true
}

func effectiveError(facts sessionfailure.Facts, err error) error {
	if err != nil {
		return err
	}
	if facts.ErrorType != "" {
		return errors.New(facts.ErrorType)
	}
	return errors.New(streamErrorMessage)
}

func clone(observation sessionfailure.Observation) sessionfailure.Observation {
	return sessionfailure.Observation{Facts: observation.Facts, Err: observation.Err}
}

func (s *Service) rollback(accepted *sessionfailure.Observation) {
	s.mu.Lock()
	if s.accepted == accepted {
		s.accepted = nil
	}
	s.mu.Unlock()
}

func (s *Service) Snapshot() *sessionfailure.Observation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.accepted == nil {
		s.mu.Unlock()
		return nil
	}
	copy := clone(*s.accepted)
	s.mu.Unlock()
	return &copy
}

func (s *Service) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.accepted = nil
	s.mu.Unlock()
}

func (s *Service) Projection(kind sessionfailure.Projection, failingEvent string, progress sessionfailure.Progress) sessionfailure.Facts {
	return sessionfailure.Facts{
		Classification: string(kind),
		TerminalReason: string(messages.TerminalReasonTerminalFailure),
		Provenance:     string(messages.TerminalProvenanceSession),
		OutputState:    s.OutputState(progress),
		FailingEvent:   failingEvent,
	}
}

func (s *Service) EmitUnsupportedTool(call sessionfailure.ToolCall) {
	if s == nil || s.dependencies.Sink == nil {
		return
	}
	s.dependencies.Sink.Record(sessionfailure.DiagnosticRecord{
		Event: sessionfailure.DiagnosticEventToolCall,
		Fields: map[string]string{
			sessionfailure.DiagnosticFieldToolName:      call.Name,
			sessionfailure.DiagnosticFieldToolCallID:    call.ID,
			sessionfailure.DiagnosticFieldFailureClass:  sessionfailure.ErrorClassUnsupportedRequest,
			sessionfailure.DiagnosticFieldFailureReason: sessionfailure.DiagnosticFailureNoToolRunner,
			sessionfailure.DiagnosticFieldTurnIndex:     strconv.Itoa(call.TurnIndex),
		},
	})
}

func (s *Service) OutputState(progress sessionfailure.Progress) string {
	if !progress.SessionOpened || progress.TurnsCompleted <= 0 {
		return string(messages.TerminalOutputNone)
	}
	return string(messages.TerminalOutputPartial)
}
