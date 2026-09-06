package latency

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

// AnalyzeRoomLatencyBundle correlates and aggregates only the event data in a
// finalized bundle. It never falls back to WAV length, live callbacks, or
// provider transcript parsing.
func Analyze(bundle rooms.RoomLatencyBundle) (rooms.RoomLatencyReport, error) {
	if err := validateLatencyBundle(bundle); err != nil {
		return rooms.RoomLatencyReport{}, err
	}
	groups, uncorrelatedCount := groupLatencyEvents(bundle.Events)
	report := rooms.RoomLatencyReport{Transitions: make([]rooms.RoomLatencyTransition, 0, len(groups))}
	for _, current := range groups {
		appendLatencyTransition(&report, analyzeRoomLatencyTransition(current.id, current.events, bundle.Format))
	}
	appendUncorrelatedExclusion(&report, uncorrelatedCount)
	report.Summary = summarizeRoomLatency(report.Transitions)
	return report, nil
}

type latencyEventGroup struct {
	id     string
	events []rooms.RoomLatencyEvent
	first  uint64
}

func validateLatencyBundle(bundle rooms.RoomLatencyBundle) error {
	if bundle.SchemaVersion != rooms.RoomLatencyBundleSchemaVersion {
		return fmt.Errorf("unsupported room latency schema version %d", bundle.SchemaVersion)
	}
	if bundle.Format.SampleRateHz <= 0 || bundle.Format.Channels <= 0 {
		return errors.New("room latency bundle has invalid PCM format")
	}
	return nil
}

func groupLatencyEvents(input []rooms.RoomLatencyEvent) ([]*latencyEventGroup, int) {
	events := append([]rooms.RoomLatencyEvent(nil), input...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	groupsByID := make(map[string]*latencyEventGroup)
	uncorrelatedCount := 0
	for _, event := range events {
		if strings.TrimSpace(event.TransitionID) == "" {
			if isLatencyEvent(event.Kind) {
				uncorrelatedCount++
			}
			continue
		}
		current := groupsByID[event.TransitionID]
		if current == nil {
			current = &latencyEventGroup{id: event.TransitionID, first: event.Sequence}
			groupsByID[event.TransitionID] = current
		}
		current.events = append(current.events, event)
	}
	groups := make([]*latencyEventGroup, 0, len(groupsByID))
	for _, current := range groupsByID {
		groups = append(groups, current)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].first == groups[j].first {
			return groups[i].id < groups[j].id
		}
		return groups[i].first < groups[j].first
	})
	return groups, uncorrelatedCount
}

func isLatencyEvent(kind rooms.RoomLatencyEventKind) bool {
	switch kind {
	case rooms.RoomLatencyEventSpeakerPCM, rooms.RoomLatencyEventEndOfSpeech, rooms.RoomLatencyEventInputCommit, rooms.RoomLatencyEventResponseCreate, rooms.RoomLatencyEventProviderAudio, rooms.RoomLatencyEventPeerAudio:
		return true
	default:
		return false
	}
}

func appendLatencyTransition(report *rooms.RoomLatencyReport, transition rooms.RoomLatencyTransition) {
	if report == nil {
		return
	}
	report.Transitions = append(report.Transitions, transition)
	if transition.Eligible {
		report.EligibleCount++
		return
	}
	report.ExcludedCount++
	report.Exclusions = append(report.Exclusions, rooms.RoomLatencyExclusion{
		TransitionID: transition.TransitionID, ParticipantID: transition.ParticipantID, Reason: transition.ExclusionReason,
	})
}

func appendUncorrelatedExclusion(report *rooms.RoomLatencyReport, count int) {
	if report == nil || count <= 0 {
		return
	}
	report.ExcludedCount++
	report.Exclusions = append(report.Exclusions, rooms.RoomLatencyExclusion{
		Reason: rooms.RoomLatencyReasonUncorrelatedLandmarks, EventCount: count,
	})
}

func analyzeRoomLatencyTransition(transitionID string, events []rooms.RoomLatencyEvent, format rooms.RoomLatencyPCMFormat) rooms.RoomLatencyTransition {
	transition, byKind := collectTransition(transitionID, events)
	populateTransitionLandmarks(&transition, byKind, format)
	if reason := transitionValidationReason(byKind, transition); reason != "" {
		transition.ExclusionReason = reason
		return transition
	}
	durations, reason := transitionDurations(transition, format)
	if reason != "" {
		transition.ExclusionReason = reason
		return transition
	}
	applyTransitionDurations(&transition, durations)
	return transition
}

func collectTransition(transitionID string, events []rooms.RoomLatencyEvent) (rooms.RoomLatencyTransition, map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent) {
	transition := rooms.RoomLatencyTransition{TransitionID: transitionID}
	byKind := make(map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent)
	for _, event := range events {
		byKind[event.Kind] = append(byKind[event.Kind], event)
		if transition.ParticipantID == "" && event.Kind != rooms.RoomLatencyEventSpeakerPCM {
			transition.ParticipantID = event.ParticipantID
		}
		if transition.TurnIndex == 0 && event.TurnIndex > 0 {
			transition.TurnIndex = event.TurnIndex
		}
		if transition.ResponseID == "" && event.ResponseID != "" {
			transition.ResponseID = event.ResponseID
		}
	}
	if speech := byKind[rooms.RoomLatencyEventEndOfSpeech]; len(speech) == 1 {
		transition.ParticipantID = speech[0].ParticipantID
		transition.PeerParticipantID = speech[0].PeerParticipantID
	}
	return transition, byKind
}

func populateTransitionLandmarks(transition *rooms.RoomLatencyTransition, byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, format rooms.RoomLatencyPCMFormat) {
	if transition == nil {
		return
	}
	speakers := byKind[rooms.RoomLatencyEventSpeakerPCM]
	if len(speakers) > 0 {
		selected := selectSpeakerEvent(speakers, byKind[rooms.RoomLatencyEventEndOfSpeech])
		if selected.ParticipantID != "" && transition.PeerParticipantID == "" {
			transition.PeerParticipantID = selected.ParticipantID
		}
		if landmark, err := roomLatencySpeakerLandmark(selected, format); err == nil {
			transition.LastSpeakerSample = &landmark
		} else {
			transition.ExclusionReason = rooms.RoomLatencyReasonInvalidSpeakerSample
		}
	}
	setEventLandmark(&transition.EndOfSpeech, byKind[rooms.RoomLatencyEventEndOfSpeech])
	setEventLandmark(&transition.InputCommit, byKind[rooms.RoomLatencyEventInputCommit])
	setEventLandmark(&transition.ResponseCreate, byKind[rooms.RoomLatencyEventResponseCreate])
	setEventLandmark(&transition.FirstProviderAudio, byKind[rooms.RoomLatencyEventProviderAudio])
	setEventLandmark(&transition.FirstPeerAudio, byKind[rooms.RoomLatencyEventPeerAudio])
}

func selectSpeakerEvent(speakers, speech []rooms.RoomLatencyEvent) rooms.RoomLatencyEvent {
	selected := speakers[len(speakers)-1]
	if len(speech) != 1 {
		return selected
	}
	for _, candidate := range speakers {
		if candidate.Sequence < speech[0].Sequence {
			selected = candidate
		}
	}
	return selected
}

func setEventLandmark(destination **rooms.RoomLatencyLandmark, events []rooms.RoomLatencyEvent) {
	if destination == nil || len(events) != 1 {
		return
	}
	landmark := roomLatencyEventLandmark(events[0])
	*destination = &landmark
}

func transitionValidationReason(byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, transition rooms.RoomLatencyTransition) string {
	if transition.ExclusionReason != "" {
		return transition.ExclusionReason
	}
	if reason := roomLatencyTransitionReason(byKind, transition); reason != "" {
		return reason
	}
	return validateRoomLatencyCorrelation(byKind, transition)
}

type latencyDurations struct {
	detection, commitAfterEnd, responseAfterCommit, dispatch time.Duration
	provider, localOutput, total, fourBucketSum              time.Duration
}

func transitionDurations(transition rooms.RoomLatencyTransition, format rooms.RoomLatencyPCMFormat) (latencyDurations, string) {
	landmarks := []*rooms.RoomLatencyLandmark{transition.LastSpeakerSample, transition.EndOfSpeech, transition.InputCommit, transition.ResponseCreate, transition.FirstProviderAudio, transition.FirstPeerAudio}
	if !validLatencyLandmarks(landmarks) {
		return latencyDurations{}, rooms.RoomLatencyReasonInvalidTimestamp
	}
	if !roomLatencyLandmarksOrdered(landmarks) {
		return latencyDurations{}, rooms.RoomLatencyReasonReorderedLandmarks
	}
	durations := latencyDurations{
		detection:           transition.EndOfSpeech.Timestamp.Sub(transition.LastSpeakerSample.Timestamp),
		commitAfterEnd:      transition.InputCommit.Timestamp.Sub(transition.EndOfSpeech.Timestamp),
		responseAfterCommit: transition.ResponseCreate.Timestamp.Sub(transition.InputCommit.Timestamp),
		dispatch:            transition.ResponseCreate.Timestamp.Sub(transition.EndOfSpeech.Timestamp),
		provider:            transition.FirstProviderAudio.Timestamp.Sub(transition.ResponseCreate.Timestamp),
		localOutput:         transition.FirstPeerAudio.Timestamp.Sub(transition.FirstProviderAudio.Timestamp),
		total:               transition.FirstPeerAudio.Timestamp.Sub(transition.LastSpeakerSample.Timestamp),
	}
	durations.fourBucketSum = durations.detection + durations.dispatch + durations.provider + durations.localOutput
	if anyNegativeDuration(durations) {
		return latencyDurations{}, rooms.RoomLatencyReasonReorderedLandmarks
	}
	frameDuration := time.Duration(format.FrameDurationNS)
	if frameDuration <= 0 {
		frameDuration = mixer.DefaultFormat().FrameDuration
	}
	difference := durations.total - durations.fourBucketSum
	if difference > frameDuration+time.Millisecond || difference < -frameDuration-time.Millisecond {
		return latencyDurations{}, rooms.RoomLatencyReasonGapOutsideTolerance
	}
	return durations, ""
}

func validLatencyLandmarks(landmarks []*rooms.RoomLatencyLandmark) bool {
	for _, landmark := range landmarks {
		if landmark == nil || landmark.Timestamp.IsZero() {
			return false
		}
	}
	return true
}

func anyNegativeDuration(durations latencyDurations) bool {
	return durations.detection < 0 || durations.commitAfterEnd < 0 || durations.responseAfterCommit < 0 || durations.dispatch < 0 || durations.provider < 0 || durations.localOutput < 0 || durations.total < 0
}

func applyTransitionDurations(transition *rooms.RoomLatencyTransition, durations latencyDurations) {
	if transition == nil {
		return
	}
	transition.DetectionMS = durations.detection.Milliseconds()
	transition.CommitAfterEndMS = durations.commitAfterEnd.Milliseconds()
	transition.ResponseAfterCommitMS = durations.responseAfterCommit.Milliseconds()
	transition.DispatchMS = durations.dispatch.Milliseconds()
	transition.ProviderMS = durations.provider.Milliseconds()
	transition.LocalOutputMS = durations.localOutput.Milliseconds()
	transition.HarnessOwnedMS = transition.DetectionMS + transition.DispatchMS + transition.LocalOutputMS
	transition.FourBucketSumMS = durations.fourBucketSum.Milliseconds()
	transition.DirectGapMS = durations.total.Milliseconds()
	transition.TotalMS = transition.DirectGapMS
	transition.Eligible = true
}

func roomLatencyTransitionReason(byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, transition rooms.RoomLatencyTransition) string {
	checks := []struct {
		kind      rooms.RoomLatencyEventKind
		missing   string
		duplicate string
	}{
		{rooms.RoomLatencyEventSpeakerPCM, rooms.RoomLatencyReasonMissingSpeakerSample, rooms.RoomLatencyReasonDuplicateSpeakerSample},
		{rooms.RoomLatencyEventEndOfSpeech, rooms.RoomLatencyReasonMissingEndOfSpeech, rooms.RoomLatencyReasonDuplicateEndOfSpeech},
		{rooms.RoomLatencyEventInputCommit, rooms.RoomLatencyReasonMissingInputCommit, rooms.RoomLatencyReasonDuplicateInputCommit},
		{rooms.RoomLatencyEventResponseCreate, rooms.RoomLatencyReasonMissingResponseCreate, rooms.RoomLatencyReasonDuplicateResponseCreate},
		{rooms.RoomLatencyEventProviderAudio, rooms.RoomLatencyReasonMissingProviderAudio, rooms.RoomLatencyReasonDuplicateProviderAudio},
		{rooms.RoomLatencyEventPeerAudio, rooms.RoomLatencyReasonMissingPeerAudio, rooms.RoomLatencyReasonDuplicatePeerAudio},
	}
	for _, check := range checks {
		count := len(byKind[check.kind])
		if count == 0 {
			return check.missing
		}
		if count > 1 {
			return check.duplicate
		}
	}
	if transition.ParticipantID == "" || transition.PeerParticipantID == "" {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	return ""
}

func validateRoomLatencyCorrelation(byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, transition rooms.RoomLatencyTransition) string {
	participantID := transition.ParticipantID
	peerID := transition.PeerParticipantID
	if !latencyPairMatches(byKind[rooms.RoomLatencyEventEndOfSpeech], participantID, peerID) {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	if !latencyParticipantGroupsMatch(byKind, participantID,
		rooms.RoomLatencyEventInputCommit,
		rooms.RoomLatencyEventResponseCreate,
		rooms.RoomLatencyEventProviderAudio,
	) {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	if !latencyPairMatches(byKind[rooms.RoomLatencyEventPeerAudio], participantID, peerID) {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	if !latencyPairMatches(byKind[rooms.RoomLatencyEventSpeakerPCM], peerID, participantID) {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	if !latencyResponseIDsMatch(byKind,
		rooms.RoomLatencyEventResponseCreate,
		rooms.RoomLatencyEventProviderAudio,
		rooms.RoomLatencyEventPeerAudio,
	) {
		return rooms.RoomLatencyReasonUncorrelatedLandmarks
	}
	return ""
}

func latencyPairMatches(events []rooms.RoomLatencyEvent, participantID, peerID string) bool {
	for _, event := range events {
		if event.ParticipantID != participantID || event.PeerParticipantID != peerID {
			return false
		}
	}
	return true
}

func latencyParticipantGroupsMatch(byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, participantID string, kinds ...rooms.RoomLatencyEventKind) bool {
	for _, kind := range kinds {
		if !latencyParticipantsMatch(byKind[kind], participantID) {
			return false
		}
	}
	return true
}

func latencyParticipantsMatch(events []rooms.RoomLatencyEvent, participantID string) bool {
	for _, event := range events {
		if event.ParticipantID != participantID {
			return false
		}
	}
	return true
}

func latencyResponseIDsMatch(byKind map[rooms.RoomLatencyEventKind][]rooms.RoomLatencyEvent, kinds ...rooms.RoomLatencyEventKind) bool {
	responseID := ""
	for _, kind := range kinds {
		for _, event := range byKind[kind] {
			if event.ResponseID == "" {
				continue
			}
			if responseID == "" {
				responseID = event.ResponseID
				continue
			}
			if responseID != event.ResponseID {
				return false
			}
		}
	}
	return true
}
