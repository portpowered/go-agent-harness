package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

const (
	// RoomLatencyArtifactPath is the stable room-level artifact containing the
	// ordered timing ledger. The report is intentionally derived by the reader
	// so a finalized bundle remains sufficient to reproduce the calculation.
	RoomLatencyArtifactPath = "room-latency.json"

	RoomLatencyBundleSchemaVersion = 1

	RoomLatencyEventSpeakerPCM     RoomLatencyEventKind = "speaker_pcm_segment"
	RoomLatencyEventEndOfSpeech    RoomLatencyEventKind = "end_of_speech"
	RoomLatencyEventInputCommit    RoomLatencyEventKind = "input_commit"
	RoomLatencyEventResponseCreate RoomLatencyEventKind = "response_create"
	RoomLatencyEventProviderAudio  RoomLatencyEventKind = "provider_audio_delta"
	RoomLatencyEventPeerAudio      RoomLatencyEventKind = "peer_audio_emission"

	RoomLatencyReasonMissingSpeakerSample    = "missing_speaker_sample"
	RoomLatencyReasonMissingEndOfSpeech      = "missing_end_of_speech"
	RoomLatencyReasonMissingInputCommit      = "missing_input_commit"
	RoomLatencyReasonMissingResponseCreate   = "missing_response_create"
	RoomLatencyReasonMissingProviderAudio    = "missing_provider_audio"
	RoomLatencyReasonMissingPeerAudio        = "missing_peer_audio"
	RoomLatencyReasonDuplicateSpeakerSample  = "duplicate_speaker_sample"
	RoomLatencyReasonDuplicateEndOfSpeech    = "duplicate_end_of_speech"
	RoomLatencyReasonDuplicateInputCommit    = "duplicate_input_commit"
	RoomLatencyReasonDuplicateResponseCreate = "duplicate_response_create"
	RoomLatencyReasonDuplicateProviderAudio  = "duplicate_provider_audio"
	RoomLatencyReasonDuplicatePeerAudio      = "duplicate_peer_audio"
	RoomLatencyReasonUncorrelatedLandmarks   = "uncorrelated_landmarks"
	RoomLatencyReasonReorderedLandmarks      = "reordered_landmarks"
	RoomLatencyReasonInvalidSpeakerSample    = "invalid_speaker_sample"
	RoomLatencyReasonInvalidTimestamp        = "invalid_timestamp"
	RoomLatencyReasonGapOutsideTolerance     = "gap_outside_tolerance"
)

// RoomLatencyEventKind identifies one authoritative room timing boundary.
type RoomLatencyEventKind string

// RoomLatencyPCMFormat is the audio metadata needed to place the final sample
// of a recorded PCM segment on the shared clock.
type RoomLatencyPCMFormat struct {
	SampleRateHz    int   `json:"sample_rate_hz"`
	Channels        int   `json:"channels"`
	FrameDurationNS int64 `json:"frame_duration_ns"`
}

// RoomLatencyEvent is one ordered, clock-stamped observation in a finalized
// room bundle. Timestamp is the observation boundary except for a speaker PCM
// event, where it is the recorded segment start and PCMBytes/format identify
// the final-sample endpoint.
type RoomLatencyEvent struct {
	Sequence          uint64               `json:"sequence"`
	Kind              RoomLatencyEventKind `json:"kind"`
	TransitionID      string               `json:"transition_id,omitempty"`
	ParticipantID     string               `json:"participant_id,omitempty"`
	PeerParticipantID string               `json:"peer_participant_id,omitempty"`
	TurnIndex         int                  `json:"turn_index,omitempty"`
	ResponseID        string               `json:"response_id,omitempty"`
	Tick              uint64               `json:"tick"`
	Timestamp         time.Time            `json:"timestamp"`
	PCMBytes          int                  `json:"pcm_bytes,omitempty"`
	SampleRateHz      int                  `json:"sample_rate_hz,omitempty"`
	Channels          int                  `json:"channels,omitempty"`
}

// RoomLatencyBundle is the durable, source-of-truth timing ledger for one
// finalized room. It contains no provider credentials or live handles.
type RoomLatencyBundle struct {
	SchemaVersion int                  `json:"schema_version"`
	Format        RoomLatencyPCMFormat `json:"format"`
	Events        []RoomLatencyEvent   `json:"events"`
}

// RoomLatencyLandmark is a normalized boundary used by the derived report.
// For LastSpeakerSample, Timestamp is the segment's final sample endpoint.
type RoomLatencyLandmark struct {
	Sequence     uint64    `json:"sequence"`
	Tick         uint64    `json:"tick"`
	Timestamp    time.Time `json:"timestamp"`
	PCMBytes     int       `json:"pcm_bytes,omitempty"`
	SampleRateHz int       `json:"sample_rate_hz,omitempty"`
	Channels     int       `json:"channels,omitempty"`
}

// RoomLatencyTransition contains the correlated landmarks and derived
// durations for one participant's response after the peer's final segment.
type RoomLatencyTransition struct {
	TransitionID      string `json:"transition_id"`
	ParticipantID     string `json:"participant_id"`
	PeerParticipantID string `json:"peer_participant_id,omitempty"`
	TurnIndex         int    `json:"turn_index,omitempty"`
	ResponseID        string `json:"response_id,omitempty"`
	Eligible          bool   `json:"eligible"`
	ExclusionReason   string `json:"exclusion_reason,omitempty"`

	LastSpeakerSample  *RoomLatencyLandmark `json:"last_speaker_sample,omitempty"`
	EndOfSpeech        *RoomLatencyLandmark `json:"end_of_speech,omitempty"`
	InputCommit        *RoomLatencyLandmark `json:"input_commit,omitempty"`
	ResponseCreate     *RoomLatencyLandmark `json:"response_create,omitempty"`
	FirstProviderAudio *RoomLatencyLandmark `json:"first_provider_audio,omitempty"`
	FirstPeerAudio     *RoomLatencyLandmark `json:"first_peer_audio,omitempty"`

	DetectionMS           int64 `json:"detection_ms"`
	CommitAfterEndMS      int64 `json:"commit_after_end_ms"`
	ResponseAfterCommitMS int64 `json:"response_after_commit_ms"`
	DispatchMS            int64 `json:"dispatch_ms"`
	ProviderMS            int64 `json:"provider_ms"`
	LocalOutputMS         int64 `json:"local_output_ms"`
	HarnessOwnedMS        int64 `json:"harness_owned_ms"`
	FourBucketSumMS       int64 `json:"four_bucket_sum_ms"`
	DirectGapMS           int64 `json:"direct_gap_ms"`
	TotalMS               int64 `json:"total_ms"`
}

// RoomLatencyExclusion identifies evidence that could not be assigned to a
// complete transition. TransitionID is empty for an uncorrelated event with
// no trustworthy transition identity.
type RoomLatencyExclusion struct {
	TransitionID  string `json:"transition_id,omitempty"`
	ParticipantID string `json:"participant_id,omitempty"`
	Reason        string `json:"reason"`
	EventCount    int    `json:"event_count,omitempty"`
}

// RoomLatencyStatistics is the deterministic aggregate for one bucket.
type RoomLatencyStatistics struct {
	SampleCount int   `json:"sample_count"`
	MedianMS    int64 `json:"median_ms"`
	P95MS       int64 `json:"p95_ms"`
	MaxMS       int64 `json:"max_ms"`
}

// RoomLatencySummary contains aggregate values over eligible transitions.
type RoomLatencySummary struct {
	Detection    RoomLatencyStatistics `json:"detection"`
	Dispatch     RoomLatencyStatistics `json:"dispatch"`
	Provider     RoomLatencyStatistics `json:"provider"`
	LocalOutput  RoomLatencyStatistics `json:"local_output"`
	HarnessOwned RoomLatencyStatistics `json:"harness_owned"`
	Total        RoomLatencyStatistics `json:"total"`
}

// RoomLatencyReport is derived exclusively from a RoomLatencyBundle.
type RoomLatencyReport struct {
	EligibleCount int                     `json:"eligible_count"`
	ExcludedCount int                     `json:"excluded_count"`
	Transitions   []RoomLatencyTransition `json:"transitions"`
	Exclusions    []RoomLatencyExclusion  `json:"exclusions,omitempty"`
	Summary       RoomLatencySummary      `json:"summary"`
}

type roomLatencyTransitionState struct {
	id            string
	participantID string
	peerID        string
	turnIndex     int
	speakerSeq    uint64
	providerSeen  bool
	peerAudioSeen bool
	responseID    string
}

// roomLatencyRecorder is the one in-process ledger used by room evidence. It
// serializes observations from participant goroutines and writes only once at
// finalization, so the artifact has a stable causal sequence as well as the
// shared clock timestamp.
type roomLatencyRecorder struct {
	clock  platformclock.Source
	format room.PCM16Format

	mu             sync.Mutex
	sequence       uint64
	events         []RoomLatencyEvent
	nextTurn       map[string]int
	active         map[string]string
	lastSpeechStop map[string]uint64
	transitions    map[string]*roomLatencyTransitionState
}

func newRoomLatencyRecorder(source platformclock.Source, format room.PCM16Format) *roomLatencyRecorder {
	if format.SampleRate <= 0 || format.Channels <= 0 {
		format = room.DefaultPCM16Format()
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = room.DefaultPCM16FrameDuration
	}
	return &roomLatencyRecorder{
		clock:          platformclock.Ensure(source),
		format:         format,
		nextTurn:       make(map[string]int),
		active:         make(map[string]string),
		lastSpeechStop: make(map[string]uint64),
		transitions:    make(map[string]*roomLatencyTransitionState),
	}
}

func (r *roomLatencyRecorder) appendLocked(kind RoomLatencyEventKind, transitionID, participantID, peerID string, turnIndex int, responseID string, pcmBytes int) int {
	timestamp := r.clock.Now().UTC()
	tick := r.sequence + 1
	if source, ok := r.clock.(interface{ Tick() uint64 }); ok {
		tick = source.Tick()
	}
	return r.appendAtLocked(kind, transitionID, participantID, peerID, turnIndex, responseID, pcmBytes, timestamp, tick)
}

func (r *roomLatencyRecorder) appendAtLocked(kind RoomLatencyEventKind, transitionID, participantID, peerID string, turnIndex int, responseID string, pcmBytes int, timestamp time.Time, tick uint64) int {
	r.sequence++
	event := RoomLatencyEvent{
		Sequence:          r.sequence,
		Kind:              kind,
		TransitionID:      transitionID,
		ParticipantID:     participantID,
		PeerParticipantID: peerID,
		TurnIndex:         turnIndex,
		ResponseID:        responseID,
		Tick:              tick,
		Timestamp:         timestamp,
		PCMBytes:          pcmBytes,
		SampleRateHz:      r.format.SampleRate,
		Channels:          r.format.Channels,
	}
	if kind != RoomLatencyEventSpeakerPCM {
		event.SampleRateHz = 0
		event.Channels = 0
	}
	r.events = append(r.events, event)
	return len(r.events) - 1
}

func (r *roomLatencyRecorder) observeSpeakerAudio(sourceID string, targetIDs []string, pcm []byte) {
	if r == nil || strings.TrimSpace(sourceID) == "" || len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(targetIDs))
	for _, targetID := range targetIDs {
		targetID = strings.TrimSpace(targetID)
		if targetID == "" || targetID == sourceID {
			continue
		}
		if _, ok := seen[targetID]; ok {
			continue
		}
		seen[targetID] = struct{}{}
		r.appendLocked(RoomLatencyEventSpeakerPCM, "", sourceID, targetID, 0, "", len(pcm))
	}
	if len(seen) == 0 {
		r.appendLocked(RoomLatencyEventSpeakerPCM, "", sourceID, "", 0, "", len(pcm))
	}
}

func (r *roomLatencyRecorder) observeSpeechStopped(participantID string) {
	if r == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	participantID = strings.TrimSpace(participantID)
	r.nextTurn[participantID]++
	turnIndex := r.nextTurn[participantID]
	transitionID := fmt.Sprintf("%s-turn-%06d", participantID, turnIndex)
	state := &roomLatencyTransitionState{id: transitionID, participantID: participantID, turnIndex: turnIndex}
	for index := len(r.events) - 1; index >= 0; index-- {
		event := &r.events[index]
		if event.Kind != RoomLatencyEventSpeakerPCM || event.PeerParticipantID != participantID || event.ParticipantID == participantID || event.TransitionID != "" {
			continue
		}
		if event.Sequence <= r.lastSpeechStop[participantID] {
			continue
		}
		event.TransitionID = transitionID
		state.peerID = event.ParticipantID
		state.speakerSeq = event.Sequence
		break
	}
	r.transitions[transitionID] = state
	r.active[participantID] = transitionID
	stopIndex := r.appendLocked(RoomLatencyEventEndOfSpeech, transitionID, participantID, state.peerID, turnIndex, "", 0)
	r.lastSpeechStop[participantID] = r.events[stopIndex].Sequence
}

func (r *roomLatencyRecorder) observeRuntime(participantID string, observation SessionRuntimeObservation) {
	if r == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	var kind RoomLatencyEventKind
	switch observation.Kind {
	case SessionRuntimeObservationInputCommit:
		kind = RoomLatencyEventInputCommit
	case SessionRuntimeObservationResponseCreate:
		kind = RoomLatencyEventResponseCreate
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	participantID = strings.TrimSpace(participantID)
	transitionID := r.active[participantID]
	turnIndex := 0
	if state := r.transitions[transitionID]; state != nil {
		turnIndex = state.turnIndex
		if observation.ResponseID != "" {
			state.responseID = observation.ResponseID
		}
	}
	timestamp := observation.Timestamp
	if timestamp.IsZero() {
		timestamp = r.clock.Now().UTC()
	}
	r.appendAtLocked(kind, transitionID, participantID, "", turnIndex, observation.ResponseID, 0, timestamp.UTC(), observation.Tick)
}

func (r *roomLatencyRecorder) observeProviderAudio(participantID string, responseID string) {
	if r == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	participantID = strings.TrimSpace(participantID)
	transitionID := r.active[participantID]
	state := r.transitions[transitionID]
	if state != nil {
		if state.providerSeen {
			return
		}
		state.providerSeen = true
		if responseID != "" {
			state.responseID = responseID
		}
	}
	turnIndex := 0
	if state != nil {
		turnIndex = state.turnIndex
	}
	r.appendLocked(RoomLatencyEventProviderAudio, transitionID, participantID, "", turnIndex, responseID, 0)
}

func (r *roomLatencyRecorder) observePeerAudio(sourceID, targetID string, pcm []byte) {
	if r == nil || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(targetID) == "" || len(pcm) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sourceID = strings.TrimSpace(sourceID)
	targetID = strings.TrimSpace(targetID)
	transitionID := r.active[sourceID]
	state := r.transitions[transitionID]
	if state != nil {
		if state.peerAudioSeen {
			return
		}
		state.peerAudioSeen = true
	}
	turnIndex := 0
	responseID := ""
	if state != nil {
		turnIndex = state.turnIndex
		responseID = state.responseID
	}
	r.appendLocked(RoomLatencyEventPeerAudio, transitionID, sourceID, targetID, turnIndex, responseID, len(pcm))
}

func (r *roomLatencyRecorder) bundle() RoomLatencyBundle {
	if r == nil {
		return RoomLatencyBundle{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	events := append([]RoomLatencyEvent(nil), r.events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	return RoomLatencyBundle{
		SchemaVersion: RoomLatencyBundleSchemaVersion,
		Format: RoomLatencyPCMFormat{
			SampleRateHz:    r.format.SampleRate,
			Channels:        r.format.Channels,
			FrameDurationNS: int64(r.format.FrameDuration),
		},
		Events: events,
	}
}

func (r *roomLatencyRecorder) write(path string, secrets []string) error {
	if r == nil {
		return nil
	}
	data, err := json.MarshalIndent(r.bundle(), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room latency artifact: %w", err)
	}
	data = append(redactRoomEvidenceJSON(data, secrets), '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".room-latency-*.tmp")
	if err != nil {
		return fmt.Errorf("create room latency artifact temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write room latency artifact temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync room latency artifact temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close room latency artifact temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace room latency artifact: %w", err)
	}
	removeTemporary = false
	return nil
}

// ReadRoomLatencyBundle reads the durable timing ledger without opening any
// source/audio or depending on a running room.
func ReadRoomLatencyBundle(path string) (RoomLatencyBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RoomLatencyBundle{}, fmt.Errorf("read room latency artifact: %w", err)
	}
	var bundle RoomLatencyBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return RoomLatencyBundle{}, fmt.Errorf("decode room latency artifact: %w", err)
	}
	if bundle.SchemaVersion != RoomLatencyBundleSchemaVersion {
		return RoomLatencyBundle{}, fmt.Errorf("unsupported room latency schema version %d", bundle.SchemaVersion)
	}
	return bundle, nil
}

// ReadRoomLatencyReport derives a reproducible report from the room-level
// artifact referenced by the finalized run manifest.
func ReadRoomLatencyReport(destination string) (RoomLatencyReport, error) {
	manifestData, err := os.ReadFile(filepath.Join(destination, RoomEvidenceManifestPath))
	if err != nil {
		return RoomLatencyReport{}, fmt.Errorf("read room run manifest: %w", err)
	}
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return RoomLatencyReport{}, fmt.Errorf("decode room run manifest: %w", err)
	}
	artifactPath := manifest.Artifacts["room.latency"]
	if artifactPath == "" {
		artifactPath = RoomLatencyArtifactPath
	}
	cleanPath := filepath.Clean(artifactPath)
	if filepath.IsAbs(artifactPath) || cleanPath != artifactPath || strings.HasPrefix(cleanPath, "..") {
		return RoomLatencyReport{}, errors.New("room latency artifact path is unsafe")
	}
	bundle, err := ReadRoomLatencyBundle(filepath.Join(destination, cleanPath))
	if err != nil {
		return RoomLatencyReport{}, err
	}
	return AnalyzeRoomLatencyBundle(bundle)
}

// ReadRoomLatencyReportFromDirectory is a descriptive alias for callers that
// make the finalized-bundle boundary explicit.
func ReadRoomLatencyReportFromDirectory(destination string) (RoomLatencyReport, error) {
	return ReadRoomLatencyReport(destination)
}

// AnalyzeRoomLatencyBundle correlates and aggregates only the event data in a
// finalized bundle. It never falls back to WAV length, live callbacks, or
// provider transcript parsing.
func AnalyzeRoomLatencyBundle(bundle RoomLatencyBundle) (RoomLatencyReport, error) {
	if bundle.SchemaVersion != RoomLatencyBundleSchemaVersion {
		return RoomLatencyReport{}, fmt.Errorf("unsupported room latency schema version %d", bundle.SchemaVersion)
	}
	if bundle.Format.SampleRateHz <= 0 || bundle.Format.Channels <= 0 {
		return RoomLatencyReport{}, errors.New("room latency bundle has invalid PCM format")
	}
	events := append([]RoomLatencyEvent(nil), bundle.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Sequence == events[j].Sequence {
			return false
		}
		return events[i].Sequence < events[j].Sequence
	})

	type group struct {
		id     string
		events []RoomLatencyEvent
		first  uint64
	}
	groupsByID := make(map[string]*group)
	uncorrelatedCount := 0
	for _, event := range events {
		if strings.TrimSpace(event.TransitionID) == "" {
			switch event.Kind {
			case RoomLatencyEventSpeakerPCM, RoomLatencyEventEndOfSpeech, RoomLatencyEventInputCommit, RoomLatencyEventResponseCreate, RoomLatencyEventProviderAudio, RoomLatencyEventPeerAudio:
				uncorrelatedCount++
			}
			continue
		}
		current := groupsByID[event.TransitionID]
		if current == nil {
			current = &group{id: event.TransitionID, first: event.Sequence}
			groupsByID[event.TransitionID] = current
		}
		current.events = append(current.events, event)
	}
	groups := make([]*group, 0, len(groupsByID))
	for _, current := range groupsByID {
		groups = append(groups, current)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].first == groups[j].first {
			return groups[i].id < groups[j].id
		}
		return groups[i].first < groups[j].first
	})

	report := RoomLatencyReport{
		Transitions: make([]RoomLatencyTransition, 0, len(groups)),
		Exclusions:  make([]RoomLatencyExclusion, 0),
	}
	for _, current := range groups {
		transition := analyzeRoomLatencyTransition(current.id, current.events, bundle.Format)
		report.Transitions = append(report.Transitions, transition)
		if transition.Eligible {
			report.EligibleCount++
		} else {
			report.ExcludedCount++
			report.Exclusions = append(report.Exclusions, RoomLatencyExclusion{
				TransitionID:  transition.TransitionID,
				ParticipantID: transition.ParticipantID,
				Reason:        transition.ExclusionReason,
			})
		}
	}
	if uncorrelatedCount > 0 {
		report.ExcludedCount++
		report.Exclusions = append(report.Exclusions, RoomLatencyExclusion{
			Reason:     RoomLatencyReasonUncorrelatedLandmarks,
			EventCount: uncorrelatedCount,
		})
	}
	report.Summary = summarizeRoomLatency(report.Transitions)
	return report, nil
}

func analyzeRoomLatencyTransition(transitionID string, events []RoomLatencyEvent, format RoomLatencyPCMFormat) RoomLatencyTransition {
	transition := RoomLatencyTransition{TransitionID: transitionID}
	byKind := make(map[RoomLatencyEventKind][]RoomLatencyEvent)
	for _, event := range events {
		byKind[event.Kind] = append(byKind[event.Kind], event)
		if transition.ParticipantID == "" && event.Kind != RoomLatencyEventSpeakerPCM {
			transition.ParticipantID = event.ParticipantID
		}
		if transition.TurnIndex == 0 && event.TurnIndex > 0 {
			transition.TurnIndex = event.TurnIndex
		}
		if transition.ResponseID == "" && event.ResponseID != "" {
			transition.ResponseID = event.ResponseID
		}
	}
	if speech := byKind[RoomLatencyEventEndOfSpeech]; len(speech) == 1 {
		transition.ParticipantID = speech[0].ParticipantID
		transition.PeerParticipantID = speech[0].PeerParticipantID
	}

	if speakers := byKind[RoomLatencyEventSpeakerPCM]; len(speakers) > 0 {
		selected := speakers[len(speakers)-1]
		if speech := byKind[RoomLatencyEventEndOfSpeech]; len(speech) == 1 {
			for _, candidate := range speakers {
				if candidate.Sequence < speech[0].Sequence {
					selected = candidate
				}
			}
		}
		if selected.ParticipantID != "" && transition.PeerParticipantID == "" {
			transition.PeerParticipantID = selected.ParticipantID
		}
		if landmark, err := roomLatencySpeakerLandmark(selected, format); err == nil {
			transition.LastSpeakerSample = &landmark
		} else {
			transition.ExclusionReason = RoomLatencyReasonInvalidSpeakerSample
		}
	}
	if speech := byKind[RoomLatencyEventEndOfSpeech]; len(speech) == 1 {
		landmark := roomLatencyEventLandmark(speech[0])
		transition.EndOfSpeech = &landmark
	}
	if commits := byKind[RoomLatencyEventInputCommit]; len(commits) == 1 {
		landmark := roomLatencyEventLandmark(commits[0])
		transition.InputCommit = &landmark
	}
	if responses := byKind[RoomLatencyEventResponseCreate]; len(responses) == 1 {
		landmark := roomLatencyEventLandmark(responses[0])
		transition.ResponseCreate = &landmark
	}
	if provider := byKind[RoomLatencyEventProviderAudio]; len(provider) == 1 {
		landmark := roomLatencyEventLandmark(provider[0])
		transition.FirstProviderAudio = &landmark
	}
	if peer := byKind[RoomLatencyEventPeerAudio]; len(peer) == 1 {
		landmark := roomLatencyEventLandmark(peer[0])
		transition.FirstPeerAudio = &landmark
	}

	if transition.ExclusionReason == "" {
		transition.ExclusionReason = roomLatencyTransitionReason(byKind, transition)
	}
	if transition.ExclusionReason != "" {
		return transition
	}
	if reason := validateRoomLatencyCorrelation(byKind, transition); reason != "" {
		transition.ExclusionReason = reason
		return transition
	}
	landmarks := []*RoomLatencyLandmark{
		transition.LastSpeakerSample,
		transition.EndOfSpeech,
		transition.InputCommit,
		transition.ResponseCreate,
		transition.FirstProviderAudio,
		transition.FirstPeerAudio,
	}
	for _, landmark := range landmarks {
		if landmark == nil || landmark.Timestamp.IsZero() {
			transition.ExclusionReason = RoomLatencyReasonInvalidTimestamp
			return transition
		}
	}
	if !roomLatencyLandmarksOrdered(landmarks) {
		transition.ExclusionReason = RoomLatencyReasonReorderedLandmarks
		return transition
	}

	detection := transition.EndOfSpeech.Timestamp.Sub(transition.LastSpeakerSample.Timestamp)
	commitAfterEnd := transition.InputCommit.Timestamp.Sub(transition.EndOfSpeech.Timestamp)
	responseAfterCommit := transition.ResponseCreate.Timestamp.Sub(transition.InputCommit.Timestamp)
	dispatch := transition.ResponseCreate.Timestamp.Sub(transition.EndOfSpeech.Timestamp)
	provider := transition.FirstProviderAudio.Timestamp.Sub(transition.ResponseCreate.Timestamp)
	localOutput := transition.FirstPeerAudio.Timestamp.Sub(transition.FirstProviderAudio.Timestamp)
	total := transition.FirstPeerAudio.Timestamp.Sub(transition.LastSpeakerSample.Timestamp)
	fourBucketSum := detection + dispatch + provider + localOutput
	if detection < 0 || commitAfterEnd < 0 || responseAfterCommit < 0 || dispatch < 0 || provider < 0 || localOutput < 0 || total < 0 {
		transition.ExclusionReason = RoomLatencyReasonReorderedLandmarks
		return transition
	}
	frameDuration := time.Duration(format.FrameDurationNS)
	if frameDuration <= 0 {
		frameDuration = room.DefaultPCM16FrameDuration
	}
	if difference := total - fourBucketSum; difference > frameDuration+time.Millisecond || difference < -frameDuration-time.Millisecond {
		transition.ExclusionReason = RoomLatencyReasonGapOutsideTolerance
		return transition
	}

	transition.DetectionMS = detection.Milliseconds()
	transition.CommitAfterEndMS = commitAfterEnd.Milliseconds()
	transition.ResponseAfterCommitMS = responseAfterCommit.Milliseconds()
	transition.DispatchMS = dispatch.Milliseconds()
	transition.ProviderMS = provider.Milliseconds()
	transition.LocalOutputMS = localOutput.Milliseconds()
	transition.HarnessOwnedMS = transition.DetectionMS + transition.DispatchMS + transition.LocalOutputMS
	transition.FourBucketSumMS = fourBucketSum.Milliseconds()
	transition.DirectGapMS = total.Milliseconds()
	transition.TotalMS = transition.DirectGapMS
	transition.Eligible = true
	return transition
}

func roomLatencyTransitionReason(byKind map[RoomLatencyEventKind][]RoomLatencyEvent, transition RoomLatencyTransition) string {
	checks := []struct {
		kind      RoomLatencyEventKind
		missing   string
		duplicate string
	}{
		{RoomLatencyEventSpeakerPCM, RoomLatencyReasonMissingSpeakerSample, RoomLatencyReasonDuplicateSpeakerSample},
		{RoomLatencyEventEndOfSpeech, RoomLatencyReasonMissingEndOfSpeech, RoomLatencyReasonDuplicateEndOfSpeech},
		{RoomLatencyEventInputCommit, RoomLatencyReasonMissingInputCommit, RoomLatencyReasonDuplicateInputCommit},
		{RoomLatencyEventResponseCreate, RoomLatencyReasonMissingResponseCreate, RoomLatencyReasonDuplicateResponseCreate},
		{RoomLatencyEventProviderAudio, RoomLatencyReasonMissingProviderAudio, RoomLatencyReasonDuplicateProviderAudio},
		{RoomLatencyEventPeerAudio, RoomLatencyReasonMissingPeerAudio, RoomLatencyReasonDuplicatePeerAudio},
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
		return RoomLatencyReasonUncorrelatedLandmarks
	}
	return ""
}

func validateRoomLatencyCorrelation(byKind map[RoomLatencyEventKind][]RoomLatencyEvent, transition RoomLatencyTransition) string {
	participantID := transition.ParticipantID
	peerID := transition.PeerParticipantID
	for _, event := range byKind[RoomLatencyEventEndOfSpeech] {
		if event.ParticipantID != participantID || event.PeerParticipantID != peerID {
			return RoomLatencyReasonUncorrelatedLandmarks
		}
	}
	for _, kind := range []RoomLatencyEventKind{RoomLatencyEventInputCommit, RoomLatencyEventResponseCreate, RoomLatencyEventProviderAudio} {
		for _, event := range byKind[kind] {
			if event.ParticipantID != participantID {
				return RoomLatencyReasonUncorrelatedLandmarks
			}
		}
	}
	for _, event := range byKind[RoomLatencyEventPeerAudio] {
		if event.ParticipantID != participantID || event.PeerParticipantID != peerID {
			return RoomLatencyReasonUncorrelatedLandmarks
		}
	}
	for _, event := range byKind[RoomLatencyEventSpeakerPCM] {
		if event.ParticipantID != peerID || event.PeerParticipantID != participantID {
			return RoomLatencyReasonUncorrelatedLandmarks
		}
	}
	responseID := ""
	for _, kind := range []RoomLatencyEventKind{RoomLatencyEventResponseCreate, RoomLatencyEventProviderAudio, RoomLatencyEventPeerAudio} {
		for _, event := range byKind[kind] {
			if event.ResponseID == "" {
				continue
			}
			if responseID == "" {
				responseID = event.ResponseID
				continue
			}
			if responseID != event.ResponseID {
				return RoomLatencyReasonUncorrelatedLandmarks
			}
		}
	}
	return ""
}

func roomLatencySpeakerLandmark(event RoomLatencyEvent, format RoomLatencyPCMFormat) (RoomLatencyLandmark, error) {
	sampleRate := event.SampleRateHz
	if sampleRate <= 0 {
		sampleRate = format.SampleRateHz
	}
	channels := event.Channels
	if channels <= 0 {
		channels = format.Channels
	}
	if event.Timestamp.IsZero() || event.PCMBytes <= 0 || sampleRate <= 0 || channels <= 0 || event.PCMBytes%(2*channels) != 0 {
		return RoomLatencyLandmark{}, errors.New("invalid speaker PCM segment")
	}
	sampleCount := event.PCMBytes / (2 * channels)
	duration := time.Duration((int64(sampleCount) * int64(time.Second)) / int64(sampleRate))
	if duration < 0 {
		return RoomLatencyLandmark{}, errors.New("negative speaker PCM duration")
	}
	return RoomLatencyLandmark{
		Sequence:     event.Sequence,
		Tick:         event.Tick,
		Timestamp:    event.Timestamp.Add(duration),
		PCMBytes:     event.PCMBytes,
		SampleRateHz: sampleRate,
		Channels:     channels,
	}, nil
}

func roomLatencyEventLandmark(event RoomLatencyEvent) RoomLatencyLandmark {
	return RoomLatencyLandmark{Sequence: event.Sequence, Tick: event.Tick, Timestamp: event.Timestamp}
}

func roomLatencyLandmarksOrdered(landmarks []*RoomLatencyLandmark) bool {
	for index := 1; index < len(landmarks); index++ {
		previous, current := landmarks[index-1], landmarks[index]
		if current.Timestamp.After(previous.Timestamp) || current.Timestamp.Equal(previous.Timestamp) && current.Sequence >= previous.Sequence {
			continue
		}
		return false
	}
	return true
}

func summarizeRoomLatency(transitions []RoomLatencyTransition) RoomLatencySummary {
	detection := make([]int64, 0)
	dispatch := make([]int64, 0)
	provider := make([]int64, 0)
	localOutput := make([]int64, 0)
	harnessOwned := make([]int64, 0)
	total := make([]int64, 0)
	for _, transition := range transitions {
		if !transition.Eligible {
			continue
		}
		detection = append(detection, transition.DetectionMS)
		dispatch = append(dispatch, transition.DispatchMS)
		provider = append(provider, transition.ProviderMS)
		localOutput = append(localOutput, transition.LocalOutputMS)
		harnessOwned = append(harnessOwned, transition.HarnessOwnedMS)
		total = append(total, transition.TotalMS)
	}
	return RoomLatencySummary{
		Detection:    roomLatencyStatistics(detection),
		Dispatch:     roomLatencyStatistics(dispatch),
		Provider:     roomLatencyStatistics(provider),
		LocalOutput:  roomLatencyStatistics(localOutput),
		HarnessOwned: roomLatencyStatistics(harnessOwned),
		Total:        roomLatencyStatistics(total),
	}
}

func roomLatencyStatistics(values []int64) RoomLatencyStatistics {
	if len(values) == 0 {
		return RoomLatencyStatistics{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	rank := int(math.Ceil(0.95*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return RoomLatencyStatistics{SampleCount: len(sorted), MedianMS: median, P95MS: sorted[rank], MaxMS: sorted[len(sorted)-1]}
}

type roomLatencyRuntimeObserver struct {
	recorder      *roomLatencyRecorder
	participantID string
}

func (o roomLatencyRuntimeObserver) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if o.recorder == nil {
		return
	}
	o.recorder.observeRuntime(o.participantID, observation)
}
