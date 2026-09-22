package agentruntime

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type invalidSessionStragglerDrainPolicyError string

func (e invalidSessionStragglerDrainPolicyError) Error() string { return string(e) }

const errInvalidSessionStragglerDrainPolicy invalidSessionStragglerDrainPolicyError = "session straggler drain requires a positive quiet period"

// sessionReplayMessageWriter is implemented by the stateful terminal renderer
// used by a complete session run. Keeping the interface private preserves the
// small writeSessionReplayMessage seam used by cancellation and unit tests.
type sessionReplayMessageWriter interface {
	writeSessionReplayMessage(messages.StreamMessage) error
}

func (c *roomCoordinator) forceBoundShutdownOnce() {
	c.mu.Lock()
	if !c.bound {
		c.mu.Unlock()
		return
	}
	c.boundForced = true
	runtimes := c.boundRuntimes
	firstFailure := roomBoundShutdownFailure(runtimes)
	if firstFailure != nil {
		c.reason, c.err, c.bound = RoomTerminationFailed, firstFailure, false
	}
	c.mu.Unlock()
	if firstFailure == nil {
		for _, runtime := range runtimes {
			if runtime != nil && runtime.lifecycle != nil {
				runtime.lifecycle.cancelActiveResponse()
			}
		}
	}
	c.boundCancellationOnce.Do(func() { close(c.boundCancellation) })
	c.doneOnce.Do(func() { close(c.done) })
	if c.cancel != nil {
		c.cancel()
	}
}

func roomBoundShutdownFailure(runtimes []*roomParticipantRuntime) error {
	var firstFailure error
	for _, runtime := range runtimes {
		if runtime == nil || runtime.lifecycle == nil {
			continue
		}
		runtime.lifecycle.markBoundCancellation()
		observation := runtime.lifecycle.terminalObservationSnapshot()
		if firstFailure == nil && observation.failure {
			failureErr := observation.err
			if failureErr == nil {
				failureErr = errors.New("session stream error")
			}
			firstFailure = roomParticipantFailure(runtime.plan.manifest.ID, failureErr, secretsForPlan(runtime.plan))
		}
	}
	return firstFailure
}

func applyRoomParticipantTerminalMetadata(result *RoomParticipantResult, lifecycle *roomParticipantLifecycle, err error) {
	if result == nil {
		return
	}
	classification, terminalReason, provenance, outputState := "", messages.TerminalReason(""), messages.TerminalProvenance(""), messages.TerminalOutputState("")
	if lifecycle != nil {
		classification, terminalReason, provenance, outputState = lifecycle.terminalMetadata()
	}
	if classification == "" {
		classification, terminalReason, provenance, outputState = sessiontracewire.LivenessMetadata(err)
	}
	if classification == "" {
		return
	}
	result.Classification = classification
	result.TerminalReason = string(terminalReason)
	result.TerminalProvenance = string(provenance)
	result.OutputState = string(outputState)
}

type roomParticipantTerminalObservation struct {
	terminationTrigger     string
	terminationDisposition string
	classification         string
	terminalReason         string
	terminalProvenance     string
	outputState            string
	err                    error
	failure                bool
}

func defaultRoomTerminalProvenance(disposition, reason string) string {
	if disposition == ParticipantTerminationDispositionCancelledAfterGrace {
		return string(messages.TerminalProvenanceRoom)
	}
	switch messages.TerminalReason(reason) {
	case messages.TerminalReasonProviderAuthoredCompletion:
		return string(messages.TerminalProvenanceProvider)
	case messages.TerminalReasonLoopSynthesizedCompletion:
		return string(messages.TerminalProvenanceLoop)
	case messages.TerminalReasonProviderClose:
		return string(messages.TerminalProvenanceSession)
	case messages.TerminalReasonReplayComplete,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
		return string(messages.TerminalProvenanceReplay)
	case messages.TerminalReasonCancellation,
		messages.TerminalReasonPartialOutput:
		return string(messages.TerminalProvenanceLoop)
	case messages.TerminalReasonTerminalFailure:
		return string(messages.TerminalProvenanceSession)
	}
	switch disposition {
	case ParticipantTerminationDispositionCancelledAfterGrace,
		ParticipantTerminationDispositionStopped:
		return string(messages.TerminalProvenanceLoop)
	default:
		return string(messages.TerminalProvenanceSession)
	}
}

func roomBoundTerminationTrigger(reason RoomTerminationReason, midResponse bool) string {
	switch reason {
	case RoomTerminationMaxTurnsReached:
		if midResponse {
			return ParticipantTerminationTriggerMaxTurnsReachedMidResponse
		}
		return ParticipantTerminationTriggerMaxTurnsReached
	case RoomTerminationMaxDurationReached:
		if midResponse {
			return ParticipantTerminationTriggerMaxDurationReachedMidResponse
		}
		return ParticipantTerminationTriggerMaxDurationReached
	default:
		return string(reason)
	}
}

func participantTerminalFields(result RoomParticipantResult) map[string]string {
	return map[string]string{
		"termination_trigger":     result.TerminationTrigger,
		"termination_disposition": result.TerminationDisposition,
		"classification":          result.Classification,
		"terminal_reason":         result.TerminalReason,
		"terminal_provenance":     result.TerminalProvenance,
		"output_state":            result.OutputState,
		"reason":                  string(result.TerminationReason),
	}
}

func participantTerminationDiagnostic(result RoomParticipantResult) SessionDiagnosticRecord {
	return SessionDiagnosticRecord{
		Event:  SessionDiagnosticEventRoomBound,
		Fields: participantTerminalFields(result),
	}
}

func isRoomBoundParticipantTrigger(trigger string) bool {
	return strings.HasPrefix(trigger, "max_duration_reached") || strings.HasPrefix(trigger, "max_turns_reached")
}

func recordRoomParticipantBoundDiagnostic(opts RoomRunOptions, evidence *roomEvidence, result RoomParticipantResult) {
	if !isRoomBoundParticipantTrigger(result.TerminationTrigger) {
		return
	}
	record := participantTerminationDiagnostic(result)
	if evidence != nil {
		if participant := evidence.participant(result.ParticipantID); participant != nil {
			participant.RecordSessionDiagnostic(record)
		}
		evidence.recordTimelineEvent("room_bound_shutdown", result.ParticipantID, record.Fields)
	}
	if opts.OnDiagnostic != nil {
		opts.OnDiagnostic(result.ParticipantID, record)
	}
}

func playbackOverflowDiagnosticFields(id string, stats audio.PlaybackQueueStats) map[string]string {
	return map[string]string{
		SessionDiagnosticFieldPlaybackDeviceID:            id,
		SessionDiagnosticFieldPlaybackSampleRate:          strconv.Itoa(stats.Format.SampleRate),
		SessionDiagnosticFieldPlaybackChannels:            strconv.Itoa(stats.Format.Channels),
		SessionDiagnosticFieldPlaybackLatencyTargetMillis: strconv.FormatInt(stats.LatencyTarget.Milliseconds(), 10),
		SessionDiagnosticFieldPlaybackCapacitySamples:     strconv.Itoa(stats.CapacitySamples),
		SessionDiagnosticFieldPlaybackQueuedSamples:       strconv.Itoa(stats.QueuedSamples),
		SessionDiagnosticFieldPlaybackPeakQueuedSamples:   strconv.Itoa(stats.PeakQueuedSamples),
		SessionDiagnosticFieldPlaybackDroppedSamples:      strconv.FormatUint(stats.DroppedSamples, 10),
		SessionDiagnosticFieldPlaybackOverflowEvents:      strconv.FormatUint(stats.OverflowEvents, 10),
	}
}

func emitRoomParticipantPlaybackOverflowDiagnostic(participantID string, handle runtimeDevices.Handle, sink SessionDiagnosticSink) {
	if handle == nil {
		return
	}
	provider, ok := handle.(runtimeDevices.PlaybackStatsProvider)
	if !ok {
		return
	}
	deviceID, stats := provider.PlaybackStats()
	if stats.DroppedSamples == 0 {
		return
	}
	fields := playbackOverflowDiagnosticFields(deviceID, stats)
	fields[SessionDiagnosticFieldPlaybackParticipantID] = participantID
	if sink != nil {
		sink.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: SessionDiagnosticEventPlaybackOverflow, Fields: fields})
	}
}

func startDurationSessionUpdatedTimer(durationClock SessionDurationClock, opts sessionLoopOptions, timer SessionDurationTimer, timeout <-chan time.Time) (SessionDurationTimer, <-chan time.Time, error) {
	if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.ScheduledAudioAwaitingConfiguration() || timer != nil {
		return timer, timeout, nil
	}
	configuredTimeout := opts.SessionUpdatedTimeout
	if configuredTimeout <= 0 {
		configuredTimeout = sessionScheduledAudioConfigTimeout
	}
	timer = durationClock.NewTimer(configuredTimeout)
	if timer == nil {
		return nil, nil, errors.New("session duration clock returned a nil session-updated timer")
	}
	return timer, timer.C(), nil
}

func stopDurationSessionUpdatedTimer(timer *SessionDurationTimer, timeout *<-chan time.Time) {
	if timer == nil || *timer == nil {
		return
	}
	(*timer).Stop()
	*timer = nil
	*timeout = nil
}

func waitForDurationSessionLoopStragglers(out io.Writer, loop *agentloop.AgentLoop, policy sessionStragglerDrainPolicy, durationClock SessionDurationClock, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs sessiontrace.Observer, terminalState *sessionDurationTerminalState) error {
	quiet := policy.quietPeriod
	if quiet <= 0 {
		return errInvalidSessionStragglerDrainPolicy
	}
	timer, err := newDurationStragglerTimer(durationClock, quiet)
	if err != nil {
		return err
	}
	defer func() { timer.Stop() }()
	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			timer, err = processDurationStragglerMessage(out, msg, planned, terminalWritten, artifacts, obs, terminalState, timer, durationClock, quiet)
			if err != nil {
				return err
			}
		case <-timer.C():
			return nil
		}
	}
}

func processDurationStragglerMessage(out io.Writer, msg messages.StreamMessage, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs sessiontrace.Observer, terminalState *sessionDurationTerminalState, timer SessionDurationTimer, durationClock SessionDurationClock, quiet time.Duration) (SessionDurationTimer, error) {
	if terminalState != nil {
		terminalState.observe(msg)
		var shouldWrite bool
		msg, shouldWrite = terminalState.admitTerminal(planned, msg)
		*terminalWritten = terminalState.written()
		if !shouldWrite {
			return timer, nil
		}
	}
	if obs != nil {
		obs.Observe(msg)
	}
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return timer, err
	}
	return resetDurationStragglerTimer(timer, durationClock, quiet)
}
