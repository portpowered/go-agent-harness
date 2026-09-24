package agentruntime

import (
	"errors"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

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

func recordRoomParticipantBoundDiagnostic(opts RoomRunOptions, evidence roomevidence.Recorder, result RoomParticipantResult) {
	if !isRoomBoundParticipantTrigger(result.TerminationTrigger) {
		return
	}
	record := participantTerminationDiagnostic(result)
	if evidence != nil {
		evidence.RecordSessionDiagnostic(roomevidence.DiagnosticRecord{ParticipantID: result.ParticipantID, Event: record.Event, Fields: record.Fields})
		evidence.MarkError(result.ParticipantID, roomevidence.TimelinePath, evidence.RecordTimeline("room_bound_shutdown", result.ParticipantID, record.Fields))
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

func roomParticipantOutstandingWork(runtime *roomParticipantRuntime) []string {
	if runtime == nil || runtime.plan == nil {
		return []string{"participant runtime"}
	}
	id := runtime.plan.manifest.ID
	outstanding := roomParticipantStartupWork(runtime, id)
	if runtime.lifecycle == nil {
		return append(outstanding, roomLifecycleWorkLabel(id, "lifecycle"))
	}
	return append(outstanding, roomParticipantOwnedWork(runtime, id)...)
}

func roomParticipantStartupWork(runtime *roomParticipantRuntime, id string) []string {
	outstanding := make([]string, 0, 1)
	if runtime.plan.startupErr != nil {
		return outstanding
	}
	if roomParticipantIsHuman(runtime.plan) {
		return roomParticipantDeviceWork(runtime, id)
	}
	if runtime.plan.tracker == nil {
		return append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	}
	connectErr, ready := runtime.plan.tracker.outcome()
	if connectErr == nil && !ready {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	}
	return outstanding
}

func roomParticipantDeviceWork(runtime *roomParticipantRuntime, id string) []string {
	if runtime.lifecycle == nil || !runtime.lifecycle.deviceHasReady() {
		return []string{roomLifecycleWorkLabel(id, "devices")}
	}
	return nil
}

func roomParticipantOwnedWork(runtime *roomParticipantRuntime, id string) []string {
	outstanding := roomParticipantSessionWork(runtime, id)
	return append(outstanding, roomParticipantLoopWork(runtime, id)...)
}

func roomParticipantSessionWork(runtime *roomParticipantRuntime, id string) []string {
	created, closed, transportDone, closeErr := runtime.lifecycle.ownedSessionSnapshot()
	var outstanding []string
	if created && !closed {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close"))
	}
	if closeErr != nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close.error"))
	}
	if created && !roomChannelClosed(transportDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.transport"))
	}
	return outstanding
}

func roomParticipantLoopWork(runtime *roomParticipantRuntime, id string) []string {
	var outstanding []string
	if runtime.participantDone != nil && !roomChannelClosed(runtime.participantDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.loop"))
	}
	if runtime.mixerDone != nil && !roomChannelClosed(runtime.mixerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "mixer"))
	}
	if runtime.observerDone != nil && !roomChannelClosed(runtime.observerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "observer"))
	}
	return outstanding
}
