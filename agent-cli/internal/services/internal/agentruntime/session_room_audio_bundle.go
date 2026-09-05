package agentruntime

import (
	"errors"
	"os"
	"time"

	"encoding/json"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

var (
	// ErrRoomReplayDeltaReconstruction identifies a valid bundle whose recorded
	// PCM deltas do not reproduce the corresponding WAV payload.
	ErrRoomReplayDeltaReconstruction = errors.New("room replay delta reconstruction failed")
	// ErrRoomReplayAudioTimeline identifies an audio artifact or annotation
	// outside the finalized room timeline.
	ErrRoomReplayAudioTimeline = errors.New("room replay audio timeline is inconsistent")
	// ErrRoomReplayToleranceProfile identifies a fixture profile that is
	// malformed or attempts to weaken a suite default.
	ErrRoomReplayToleranceProfile = errors.New("invalid room replay tolerance profile")
)

// RoomReplayToleranceProfile is the fully expanded, immutable-by-convention
// analysis profile attached to a replay bundle. Omitting a profile in the
// manifest selects the documented suite defaults. A supplied profile may only
// tighten those defaults.
type RoomReplayToleranceProfile struct {
	Name         string
	StreamConfig audio.PCM16AnalysisConfig
	RoomConfig   audio.PCM16RoomAnalysisConfig
}

// DefaultRoomReplayToleranceProfile returns the complete suite profile used
// when a bundle omits a tolerance section.
func DefaultRoomReplayToleranceProfile() RoomReplayToleranceProfile {
	return RoomReplayToleranceProfile{
		Name:         "suite-default",
		StreamConfig: audio.DefaultPCM16AnalysisConfig,
		RoomConfig:   audio.DefaultPCM16RoomAnalysisConfig,
	}
}

// RoomReplayAudioDelta is one decoded audio delta in recorded JSONL order.
// PCM is a copy of the raw little-endian PCM16 payload. Sequence is the
// recorded sequence when the capture supplied one; LineNumber always points
// back to the source JSONL line.
type RoomReplayAudioDelta struct {
	ID          string
	Sequence    int64
	HasSequence bool
	Offset      time.Duration
	HasOffset   bool
	TurnID      string
	LineNumber  int
	PCM         []byte
}

// RoomReplayAudioStream is an identity- and time-aware mono PCM16 stream
// resolved from a room bundle. PCM and Samples are caller-owned copies; the
// embedded audio input is ready to pass to the analysis package.
type RoomReplayAudioStream struct {
	audio.PCM16TimedStream
	Role          string
	PCM           []byte
	SampleCount   int
	Artifact      RoomReplayArtifact
	DeltaArtifact RoomReplayArtifact
	Deltas        []RoomReplayAudioDelta
}

// RoomReplayAudioParticipant groups the independent output, sent, and
// received streams for one stable participant identity.
type RoomReplayAudioParticipant struct {
	ID          string
	WAV         RoomReplayAudioStream
	Sent        RoomReplayAudioStream
	Received    RoomReplayAudioStream
	Events      []json.RawMessage
	Diagnostics []json.RawMessage
}

// RoomReplayAudioAnnotation retains the generic annotation identity and
// interval while the typed slices on RoomReplayAudioBundle expose the
// analysis-ready overlap, barge-in, and loudness forms.
type RoomReplayAudioAnnotation struct {
	ID                       string
	Kind                     string
	Start                    time.Duration
	End                      time.Duration
	Participants             []string
	SourceParticipantID      string
	TargetParticipantID      string
	InterrupterParticipantID string
	InterruptedParticipantID string
	Raw                      json.RawMessage
}

// RoomReplayAudioBundle is the validated audio projection of a landed room
// replay plan. LoadRoomReplayAudioBundle performs all filesystem, hash,
// format, identity, timing, sidecar, tolerance, and delta reconstruction
// checks before returning this value.
type RoomReplayAudioBundle struct {
	Plan         RoomReplayPlan
	Format       RoomReplayPCMFormat
	Tolerances   RoomReplayToleranceProfile
	Participants []RoomReplayAudioParticipant
	RoomMix      RoomReplayAudioStream
	Annotations  []RoomReplayAudioAnnotation
	Overlaps     []audio.PCM16OverlapInterval
	BargeIns     []audio.PCM16BargeInAnnotation
	Loudness     []audio.PCM16LoudnessInterval
}

// Participant returns a participant's resolved audio evidence by stable ID.
func (b RoomReplayAudioBundle) Participant(id string) (RoomReplayAudioParticipant, bool) {
	for _, participant := range b.Participants {
		if participant.ID == id {
			return participant, true
		}
	}
	return RoomReplayAudioParticipant{}, false
}

// AnalysisInput converts the resolved streams and annotations into the
// side-effect-free audio analyzer input. Every stream identity remains
// independent, including room mix and sent/received evidence.
func (b RoomReplayAudioBundle) AnalysisInput() audio.PCM16RoomInput {
	input := audio.PCM16RoomInput{
		Overlaps: append([]audio.PCM16OverlapInterval(nil), b.Overlaps...),
		BargeIns: append([]audio.PCM16BargeInAnnotation(nil), b.BargeIns...),
		Loudness: append([]audio.PCM16LoudnessInterval(nil), b.Loudness...),
	}
	for _, participant := range b.Participants {
		input.Streams = append(input.Streams,
			cloneTimedStream(participant.WAV.PCM16TimedStream),
			cloneTimedStream(participant.Sent.PCM16TimedStream),
			cloneTimedStream(participant.Received.PCM16TimedStream),
		)
	}
	if b.RoomMix.StreamID != "" {
		input.Streams = append(input.Streams, cloneTimedStream(b.RoomMix.PCM16TimedStream))
	}
	return input
}

// AnalysisConfig returns the fully expanded room profile for ordinary replay
// assertions.
func (b RoomReplayAudioBundle) AnalysisConfig() audio.PCM16RoomAnalysisConfig {
	return b.Tolerances.RoomConfig
}

// LoadRoomReplayAudioBundle validates and resolves a complete room replay
// bundle. The existing LoadRoomReplayPlan is deliberately the first step so
// no audio property is evaluated against an untrusted or hash-inconsistent
// bundle.
func LoadRoomReplayAudioBundle(bundle string) (RoomReplayAudioBundle, error) {
	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	manifestData, err := os.ReadFile(plan.ManifestPath)
	if err != nil {
		return RoomReplayAudioBundle{}, roomReplayAudioIncomplete("run-manifest.json", "", "readable manifest", err.Error(), err)
	}
	manifest, err := roomReplayObject(manifestData)
	if err != nil {
		return RoomReplayAudioBundle{}, roomReplayAudioMismatch("run-manifest.json", "", "JSON object", "invalid", err)
	}
	profile, err := parseRoomReplayToleranceProfile(manifest)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	participantObjects, err := roomReplayAudioParticipantObjects(manifest)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}

	result := RoomReplayAudioBundle{
		Plan:         plan,
		Format:       plan.PCMFormat,
		Tolerances:   profile,
		Participants: make([]RoomReplayAudioParticipant, 0, len(plan.Participants)),
	}
	streamParticipants := make(map[string]string, len(plan.Participants)*3)
	for _, participant := range plan.Participants {
		participantObject := participantObjects[participant.ID]
		resolved, err := loadRoomReplayAudioParticipant(plan, participant, participantObject)
		if err != nil {
			return RoomReplayAudioBundle{}, err
		}
		result.Participants = append(result.Participants, resolved)
		for _, stream := range []RoomReplayAudioStream{resolved.WAV, resolved.Sent, resolved.Received} {
			if owner, exists := streamParticipants[stream.StreamID]; exists {
				return RoomReplayAudioBundle{}, roomReplayAudioMismatch("streams."+stream.StreamID, "run-manifest.json", "unique stream identity", owner+" and "+participant.ID, nil)
			}
			streamParticipants[stream.StreamID] = participant.ID
		}
	}

	roomMixArtifact, ok := findRoomReplayArtifact(plan.Artifacts, "room:mix")
	if !ok {
		return RoomReplayAudioBundle{}, roomReplayAudioIncomplete("artifacts.room_mix", "", "validated room mix artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	roomMix, err := loadRoomReplayWAVStream(plan, roomMixArtifact, "room:mix", "room", "room-mix")
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	if err := validateRoomReplayAudioStreamTimeline(roomMix, plan, "room_mix"); err != nil {
		return RoomReplayAudioBundle{}, err
	}
	if owner, exists := streamParticipants[roomMix.StreamID]; exists {
		return RoomReplayAudioBundle{}, roomReplayAudioMismatch("streams."+roomMix.StreamID, "run-manifest.json", "unique stream identity", owner+" and room", nil)
	}
	result.RoomMix = roomMix
	streamParticipants[roomMix.StreamID] = "room"

	annotations, overlaps, barges, loudness, err := parseRoomReplayAudioAnnotations(manifest, plan, result.Participants, streamParticipants)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	result.Annotations = annotations
	result.Overlaps = overlaps
	result.BargeIns = barges
	result.Loudness = loudness
	return result, nil
}

// ValidateRoomReplayAudioBundle is the admission-only form of
// LoadRoomReplayAudioBundle.
func ValidateRoomReplayAudioBundle(bundle string) error {
	_, err := LoadRoomReplayAudioBundle(bundle)
	return err
}
