package latency

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

type roomLatencyTransitionState struct {
	id            string
	participantID string
	peerID        string
	turnIndex     int
	speakerSeq    uint64
	commitSeen    bool
	responseSeen  bool
	providerSeen  bool
	peerAudioSeen bool
	responseID    string
}

// Recorder is the one in-process ledger used by room evidence. It
// serializes observations from participant goroutines and writes only once at
// finalization, so the artifact has a stable causal sequence as well as the
// shared clock timestamp.
// Recorder owns the bounded room latency observation ledger. It is kept
// private to room evidence while its durable event types remain public for
// host tooling and replay analysis.
type Recorder struct {
	clock  platformclock.Source
	format rooms.AudioFormat

	mu             sync.Mutex
	sequence       uint64
	events         []RoomLatencyEvent
	nextTurn       map[string]int
	active         map[string]string
	lastSpeechStop map[string]uint64
	transitions    map[string]*roomLatencyTransitionState
}

func New(source platformclock.Source, format rooms.AudioFormat) *Recorder {
	if format.SampleRate <= 0 || format.Channels <= 0 {
		format = mixer.DefaultFormat()
	}
	if format.FrameDuration <= 0 {
		format.FrameDuration = mixer.DefaultFormat().FrameDuration
	}
	return &Recorder{
		clock:          platformclock.Ensure(source),
		format:         format,
		nextTurn:       make(map[string]int),
		active:         make(map[string]string),
		lastSpeechStop: make(map[string]uint64),
		transitions:    make(map[string]*roomLatencyTransitionState),
	}
}

func (r *Recorder) appendLocked(kind RoomLatencyEventKind, transitionID, participantID, peerID string, turnIndex int, responseID string, pcmBytes int) int {
	timestamp := r.clock.Now().UTC()
	tick := r.sequence + 1
	if source, ok := r.clock.(interface{ Tick() uint64 }); ok {
		tick = source.Tick()
	}
	return r.appendAtLocked(kind, transitionID, participantID, peerID, turnIndex, responseID, pcmBytes, timestamp, tick)
}

func (r *Recorder) appendAtLocked(kind RoomLatencyEventKind, transitionID, participantID, peerID string, turnIndex int, responseID string, pcmBytes int, timestamp time.Time, tick uint64) int {
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

func (r *Recorder) ObserveSpeakerAudio(sourceID string, targetIDs []string, frame audio.PCMFrame) {
	pcmBytes := r.framePCMBytes(frame)
	r.observeSpeakerBytes(sourceID, targetIDs, pcmBytes)
}

// ObserveSpeakerBytes is the count-aware form used by adapters that already
// own encoded PCM. It records the byte count without allocating a second
// sample buffer merely for timing evidence.
func (r *Recorder) ObserveSpeakerBytes(sourceID string, targetIDs []string, pcmBytes int) {
	r.observeSpeakerBytes(sourceID, targetIDs, pcmBytes)
}

func (r *Recorder) observeSpeakerBytes(sourceID string, targetIDs []string, pcmBytes int) {
	if r == nil || strings.TrimSpace(sourceID) == "" || pcmBytes <= 0 {
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
		r.appendLocked(RoomLatencyEventSpeakerPCM, "", sourceID, targetID, 0, "", pcmBytes)
	}
	if len(seen) == 0 {
		r.appendLocked(RoomLatencyEventSpeakerPCM, "", sourceID, "", 0, "", pcmBytes)
	}
}

func (r *Recorder) ObserveSpeechStopped(participantID string) {
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

func (r *Recorder) ObserveRuntime(participantID string, observation Observation) {
	if r == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	kind, ok := runtimeObservationKind(observation.Kind)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observeRuntimeLocked(strings.TrimSpace(participantID), observation, kind)
}

func runtimeObservationKind(kind ObservationKind) (RoomLatencyEventKind, bool) {
	switch kind {
	case ObservationInputCommit:
		return RoomLatencyEventInputCommit, true
	case ObservationResponseCreate:
		return RoomLatencyEventResponseCreate, true
	default:
		return "", false
	}
}

func (r *Recorder) observeRuntimeLocked(participantID string, observation Observation, kind RoomLatencyEventKind) {
	transitionID := r.active[participantID]
	state := r.transitions[transitionID]
	if state == nil {
		// A provider may emit an opening response before any peer speech has
		// created a measurable transition. Keep that response out of the
		// latency ledger; its later peer handoff cannot be assigned causally.
		return
	}
	if observation.ResponseID != "" {
		state.responseID = observation.ResponseID
	}
	if r.duplicateRuntimeObservationLocked(kind, transitionID, state, observation.ResponseID) {
		return
	}
	timestamp := observation.Timestamp
	if timestamp.IsZero() {
		timestamp = r.clock.Now().UTC()
	}
	r.appendAtLocked(kind, transitionID, participantID, "", state.turnIndex, observation.ResponseID, 0, timestamp.UTC(), observation.Tick)
}

func (r *Recorder) duplicateRuntimeObservationLocked(kind RoomLatencyEventKind, transitionID string, state *roomLatencyTransitionState, responseID string) bool {
	switch kind {
	case RoomLatencyEventInputCommit:
		if state.commitSeen {
			return true
		}
		state.commitSeen = true
	case RoomLatencyEventResponseCreate:
		if state.responseSeen {
			// A client-owned MESSAGE.END can be followed by the provider's
			// response.created boundary. Preserve an ID learned from the latter
			// without adding a duplicate landmark.
			if responseID != "" {
				r.updateResponseCreateIDLocked(transitionID, responseID)
			}
			return true
		}
		state.responseSeen = true
	}
	return false
}

func (r *Recorder) updateResponseCreateIDLocked(transitionID, responseID string) {
	for index := len(r.events) - 1; index >= 0; index-- {
		event := &r.events[index]
		if event.TransitionID == transitionID && event.Kind == RoomLatencyEventResponseCreate {
			event.ResponseID = responseID
			return
		}
	}
}

func (r *Recorder) ObserveProviderAudio(participantID string, responseID string) {
	r.observeProviderAudioAt(participantID, responseID, time.Time{}, 0)
}

// ObserveProviderAudioAt preserves the event's capture timestamp when the
// live event drain is scheduled after the media worker. Using the recorder's
// current clock here would move the provider landmark forward with unrelated
// mixer cadence and can make an otherwise ordered transition look reordered.
func (r *Recorder) ObserveProviderAudioAt(participantID string, responseID string, timestamp time.Time, tick uint64) {
	r.observeProviderAudioAt(participantID, responseID, timestamp, tick)
}

func (r *Recorder) observeProviderAudioAt(participantID string, responseID string, timestamp time.Time, tick uint64) {
	if r == nil || strings.TrimSpace(participantID) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	participantID = strings.TrimSpace(participantID)
	transitionID := r.active[participantID]
	state := r.transitions[transitionID]
	if state == nil {
		return
	}
	if state.providerSeen {
		return
	}
	state.providerSeen = true
	if responseID != "" {
		state.responseID = responseID
	}
	turnIndex := state.turnIndex
	if timestamp.IsZero() {
		r.appendLocked(RoomLatencyEventProviderAudio, transitionID, participantID, "", turnIndex, responseID, 0)
		return
	}
	r.appendAtLocked(RoomLatencyEventProviderAudio, transitionID, participantID, "", turnIndex, responseID, 0, timestamp.UTC(), tick)
}

func (r *Recorder) ObservePeerAudio(sourceID, targetID string, frame audio.PCMFrame) {
	pcmBytes := r.framePCMBytes(frame)
	r.observePeerBytes(sourceID, targetID, pcmBytes)
}

// ObservePeerBytes is the count-aware form used by encoded-PCM adapters.
func (r *Recorder) ObservePeerBytes(sourceID, targetID string, pcmBytes int) {
	r.observePeerBytes(sourceID, targetID, pcmBytes)
}

func (r *Recorder) observePeerBytes(sourceID, targetID string, pcmBytes int) {
	if r == nil || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(targetID) == "" || pcmBytes <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sourceID = strings.TrimSpace(sourceID)
	targetID = strings.TrimSpace(targetID)
	transitionID := r.active[sourceID]
	state := r.transitions[transitionID]
	if state == nil {
		return
	}
	if state.peerAudioSeen {
		return
	}
	state.peerAudioSeen = true
	turnIndex := state.turnIndex
	responseID := state.responseID
	r.appendLocked(RoomLatencyEventPeerAudio, transitionID, sourceID, targetID, turnIndex, responseID, pcmBytes)
}

func (r *Recorder) framePCMBytes(frame audio.PCMFrame) int {
	if r == nil || len(frame.Samples) == 0 {
		return 0
	}
	channels := frame.Format.Channels
	if channels <= 0 {
		channels = r.format.Channels
	}
	maxInt := int(^uint(0) >> 1)
	if channels <= 0 || len(frame.Samples)%channels != 0 || len(frame.Samples) > maxInt/2 {
		return 0
	}
	// PCMFrame samples are already interleaved across channels.
	return len(frame.Samples) * 2
}

func (r *Recorder) Bundle() RoomLatencyBundle {
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
