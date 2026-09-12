package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type commitPayloadPreference interface {
	RetainCommitPayload() bool
}

type providerBoundaryPreference interface {
	ObserveProviderBoundaries() bool
}

const maxCommitPayloadBytes = 16 << 20

// Service records one invocation without retaining ownership of its observer.
type Service struct {
	observer sessionobservation.SessionRuntimeObserver
	clock    platformclock.Source

	sequence                  atomic.Uint64
	providerBoundaryObserving atomic.Bool
	retainCommitPayload       bool

	terminalOnce sync.Once
	inputMu      sync.Mutex
	inputPayload []byte
	inputCommits int
}

func New(observer sessionobservation.SessionRuntimeObserver, source platformclock.Source) *Service {
	service := &Service{
		observer:            observer,
		clock:               platformclock.Ensure(source),
		retainCommitPayload: true,
	}
	if preference, ok := observer.(commitPayloadPreference); ok {
		service.retainCommitPayload = preference.RetainCommitPayload()
	}
	if preference, ok := observer.(providerBoundaryPreference); ok {
		service.providerBoundaryObserving.Store(preference.ObserveProviderBoundaries())
	}
	return service
}

func (s *Service) EnableProviderBoundaryObservations() {
	if s != nil {
		s.providerBoundaryObserving.Store(true)
	}
}

func (s *Service) ProviderBoundaryObservationsEnabled() bool {
	return s != nil && s.providerBoundaryObserving.Load()
}

func (s *Service) Observe(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns int, clean bool, runErr error) {
	s.ObserveWithMetadata(kind, payload, turns, 0, clean, runErr, "", "")
}

func (s *Service) ObserveWithInputCommit(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error) {
	s.ObserveFinalWithMetadata(kind, payload, turns, inputCommit, clean, runErr, nil, "", "")
}

func (s *Service) ObserveFinal(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, accounting *sessionobservation.SessionFinalAccounting) {
	s.ObserveFinalWithMetadata(kind, payload, turns, inputCommit, clean, runErr, accounting, "", "")
}

func (s *Service) ObserveWithMetadata(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, responseID string, responsePurpose messages.ResponsePurpose) {
	s.ObserveWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, responseID, responsePurpose, "", 0, 0)
}

func (s *Service) ObserveWithMetadataAndIdentity(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, responseID string, responsePurpose messages.ResponsePurpose, streamID string, loopPassID int, epoch uint64) {
	s.ObserveFinalWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, nil, responseID, responsePurpose, streamID, loopPassID, epoch)
}

func (s *Service) ObserveFinalWithMetadata(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, accounting *sessionobservation.SessionFinalAccounting, responseID string, responsePurpose messages.ResponsePurpose) {
	s.ObserveFinalWithMetadataAndIdentity(kind, payload, turns, inputCommit, clean, runErr, accounting, responseID, responsePurpose, "", 0, 0)
}

func (s *Service) ObserveFinalWithMetadataAndIdentity(kind sessionobservation.SessionRuntimeObservationKind, payload []byte, turns, inputCommit int, clean bool, runErr error, accounting *sessionobservation.SessionFinalAccounting, responseID string, responsePurpose messages.ResponsePurpose, streamID string, loopPassID int, epoch uint64) {
	if s == nil || s.observer == nil {
		return
	}
	tick, timestamp := s.snapshot()
	s.observer.ObserveSessionRuntime(sessionobservation.SessionRuntimeObservation{
		Kind:            kind,
		Tick:            tick,
		Timestamp:       timestamp,
		Payload:         append([]byte(nil), payload...),
		TurnsCompleted:  turns,
		InputCommit:     inputCommit,
		ResponseID:      responseID,
		ResponsePurpose: responsePurpose,
		StreamID:        streamID,
		LoopPassID:      loopPassID,
		Epoch:           epoch,
		Clean:           clean,
		Error:           observationError(runErr),
		FinalAccounting: cloneFinalAccounting(accounting),
	})
}

func (s *Service) AudioOutputMessage(payload []byte, msg messages.StreamMessage) {
	s.ObserveWithMetadataAndIdentity(sessionobservation.SessionRuntimeObservationAudioOutput, payload, 0, 0, false, nil, msg.ResponseID, msg.ResponsePurpose, msg.ActorStreamID, msg.LoopPassID, 0)
}

func (s *Service) AudioPlaybackReceipt(receipt sessionobservation.PlaybackReceipt) {
	if s == nil {
		return
	}
	errorText := observationError(receipt.Err)
	payload, err := json.Marshal(struct {
		CommandID  uint64 `json:"command_id"`
		Epoch      uint64 `json:"epoch,omitempty"`
		Applied    bool   `json:"applied"`
		AudioEndMS int    `json:"audio_end_ms,omitempty"`
		Error      string `json:"error,omitempty"`
	}{
		CommandID:  receipt.CommandID,
		Epoch:      receipt.Epoch,
		Applied:    receipt.Applied,
		AudioEndMS: receipt.AudioEndMS,
		Error:      errorText,
	})
	if err != nil {
		s.Observe(sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt, nil, 0, false, err)
		return
	}
	s.Observe(sessionobservation.SessionRuntimeObservationAudioPlaybackReceipt, payload, 0, false, receipt.Err)
}

func (s *Service) AudioInput(payload []byte) {
	s.Observe(sessionobservation.SessionRuntimeObservationAudioInput, payload, 0, false, nil)
}

func (s *Service) ProviderAudioSent(payload []byte) {
	if s == nil {
		return
	}
	s.inputMu.Lock()
	if s.retainCommitPayload {
		remaining := maxCommitPayloadBytes - len(s.inputPayload)
		if remaining > 0 {
			if len(payload) > remaining {
				payload = payload[:remaining]
			}
			s.inputPayload = append(s.inputPayload, payload...)
		}
	}
	s.inputMu.Unlock()
}

func (s *Service) InputCommit() {
	if s == nil {
		return
	}
	s.inputMu.Lock()
	s.inputCommits++
	commit := s.inputCommits
	payload := append([]byte(nil), s.inputPayload...)
	s.inputPayload = nil
	s.inputMu.Unlock()
	s.ObserveWithInputCommit(sessionobservation.SessionRuntimeObservationInputCommit, payload, 0, commit, true, nil)
}

func (s *Service) ProviderInputCommit() {
	if s == nil {
		return
	}
	s.inputMu.Lock()
	payload := append([]byte(nil), s.inputPayload...)
	s.inputPayload = nil
	s.inputMu.Unlock()
	s.ObserveWithInputCommit(sessionobservation.SessionRuntimeObservationInputCommit, payload, 0, 0, true, nil)
}

func (s *Service) ResponseCreate(msg messages.StreamMessage) {
	s.ObserveWithMetadata(sessionobservation.SessionRuntimeObservationResponseCreate, nil, 0, 0, true, nil, msg.ResponseID, msg.ResponsePurpose)
}

func (s *Service) TurnCompleted(turns int) {
	s.Observe(sessionobservation.SessionRuntimeObservationTurnCompleted, nil, turns, true, nil)
}

func (s *Service) TerminalWithAccounting(turns int, runErr error, accounting *sessionobservation.SessionFinalAccounting) {
	if s == nil || s.observer == nil {
		return
	}
	s.terminalOnce.Do(func() {
		s.ObserveFinal(sessionobservation.SessionRuntimeObservationTerminal, nil, turns, 0, runErr == nil, runErr, accounting)
	})
}

func (s *Service) ObserveToolCall(call messages.ToolCall) {
	if s == nil {
		return
	}
	payload, err := json.Marshal(call)
	s.Observe("tool_call", payload, 0, err == nil, err)
}

func (s *Service) ObserveToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool) {
	if s == nil {
		return
	}
	payload, err := json.Marshal(struct {
		CallID   string                    `json:"call_id"`
		Name     string                    `json:"name"`
		Response messages.ToolCallResponse `json:"response"`
		Failed   bool                      `json:"failed"`
	}{call.ID, call.Name, response, failed})
	s.Observe("tool_result", payload, 0, !failed && err == nil, err)
}

func (s *Service) snapshot() (uint64, time.Time) {
	tick := s.sequence.Add(1)
	if source, ok := s.clock.(interface{ Tick() uint64 }); ok {
		tick = source.Tick()
	}
	return tick, s.clock.Now()
}

func observationError(err error) string {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	return err.Error()
}

func cloneFinalAccounting(accounting *sessionobservation.SessionFinalAccounting) *sessionobservation.SessionFinalAccounting {
	if accounting == nil {
		return nil
	}
	clone := *accounting
	clone.Metrics.HistogramBounds = append([]int64(nil), accounting.Metrics.HistogramBounds...)
	clone.Metrics.Series = make([]metrics.SeriesSnapshot, len(accounting.Metrics.Series))
	for index, series := range accounting.Metrics.Series {
		clone.Metrics.Series[index] = series
		clone.Metrics.Series[index].Histogram.Bounds = append([]int64(nil), series.Histogram.Bounds...)
		clone.Metrics.Series[index].Histogram.BucketCounts = append([]uint64(nil), series.Histogram.BucketCounts...)
	}
	return &clone
}

// Compile-time method-shape checks keep the private implementation honest.
var _ sessionobservation.Service = (*Service)(nil)
