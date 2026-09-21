package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

type RoomReplayPlan = roomreplay.RoomReplayPlan
type RoomReplayArtifact = roomreplay.RoomReplayArtifact
type RoomReplayParticipant = roomreplay.RoomReplayParticipant
type RoomReplayPCMFormat = roomreplay.RoomReplayPCMFormat
type RoomReplayTimelineEvent = roomreplay.RoomReplayTimelineEvent
type roomReplayJSONObject map[string]json.RawMessage

const (
	RoomReplayBundleSchemaVersion     = roomreplay.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath      = roomreplay.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch          = roomreplay.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete        = roomreplay.RoomReplayBundleIncomplete
	ErrInvalidRoomReplayBundle        = roomreplay.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete     = roomreplay.ErrRoomReplayBundleIncomplete
	roomReplayArtifactRoleWAV         = roomreplay.ArtifactRoleWAV
	roomReplayArtifactRoleDiagnostics = roomreplay.ArtifactRoleDiagnostics
	roomReplayArtifactRoleDeltas      = roomreplay.ArtifactRoleDeltas
	roomReplayArtifactRoleSentPCM     = roomreplay.ArtifactRoleSentPCM
	roomReplayArtifactRoleReceivedPCM = roomreplay.ArtifactRoleReceivedPCM
	roomReplayArtifactRoleEvents      = roomreplay.ArtifactRoleEvents
	roomReplayArtifactRoleCapture     = roomreplay.ArtifactRoleCapture
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
	StreamConfig roomanalysis.PCM16AnalysisConfig
	RoomConfig   roomanalysis.PCM16RoomAnalysisConfig
}

// DefaultRoomReplayToleranceProfile returns the complete suite profile used
// when a bundle omits a tolerance section.
func DefaultRoomReplayToleranceProfile() RoomReplayToleranceProfile {
	return RoomReplayToleranceProfile{
		Name:         "suite-default",
		StreamConfig: roomanalysis.DefaultPCM16AnalysisConfig(),
		RoomConfig:   roomanalysis.DefaultPCM16RoomAnalysisConfig(),
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
	roomanalysis.PCM16TimedStream
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
	Overlaps     []roomanalysis.PCM16OverlapInterval
	BargeIns     []roomanalysis.PCM16BargeInAnnotation
	Loudness     []roomanalysis.PCM16LoudnessInterval
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
func (b RoomReplayAudioBundle) AnalysisInput() roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), b.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), b.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), b.Loudness...),
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
func (b RoomReplayAudioBundle) AnalysisConfig() roomanalysis.PCM16RoomAnalysisConfig {
	return b.Tolerances.RoomConfig
}

// LoadRoomReplayAudioBundle validates and resolves a complete room replay
// bundle. The existing LoadRoomReplayPlan is deliberately the first step so
// no audio property is evaluated against an untrusted or hash-inconsistent
// bundle.
func LoadRoomReplayAudioBundle(replayService roomreplay.Service, bundle string) (RoomReplayAudioBundle, error) {
	plan, err := loadRoomReplayPlan(replayService, bundle)
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

func loadRoomReplayPlan(replayService roomreplay.Service, bundle string) (RoomReplayPlan, error) {
	if replayService == nil {
		return RoomReplayPlan{}, errors.New("room replay service is required")
	}
	return replayService.Load(bundle)
}

// ValidateRoomReplayAudioBundle is the admission-only form of LoadRoomReplayAudioBundle.
func ValidateRoomReplayAudioBundle(replayService roomreplay.Service, bundle string) error {
	_, err := LoadRoomReplayAudioBundle(replayService, bundle)
	return err
}

func resolveRoomReplayPlan(opts RoomRunOptions) (RoomReplayPlan, bool, error) {
	if opts.ReplayPlan != nil {
		plan := *opts.ReplayPlan
		if !plan.Finalized || len(plan.Participants) < 2 {
			return RoomReplayPlan{}, true, &roomreplay.RoomReplayBundleError{Kind: roomreplay.RoomReplayBundleIncomplete, Field: "replay_plan", Expected: "admitted finalized plan with at least two participants", Actual: "incomplete", Err: roomreplay.ErrRoomReplayBundleIncomplete}
		}
		return plan, true, nil
	}
	path := strings.TrimSpace(opts.ReplayPath)
	if path == "" {
		return RoomReplayPlan{}, false, nil
	}
	if opts.ReplayService == nil {
		return RoomReplayPlan{}, true, errors.New("room replay service is required for replay path admission")
	}
	plan, err := opts.ReplayService.Load(path)
	return plan, true, err
}

func buildRoomReplaySchedule(ctx context.Context, replayMode bool, opts RoomRunOptions, plans []*roomParticipantPlan) (roomreplay.Schedule, error) {
	if !replayMode || opts.ReplayPlan == nil {
		return nil, nil
	}
	if opts.ReplayService == nil {
		return nil, errors.New("room replay service is required")
	}
	format := roomFormatForOptions(opts)
	request := roomreplay.BuildRequest{
		SourceFormat: roomreplay.SourcePCM16Format{
			SampleRate: opts.ReplayPlan.PCMFormat.SampleRate, Channels: opts.ReplayPlan.PCMFormat.Channels,
			SampleWidthBits: opts.ReplayPlan.PCMFormat.SampleWidthBits, SampleWidthBit: opts.ReplayPlan.PCMFormat.SampleWidthBit,
			ByteOrder: opts.ReplayPlan.PCMFormat.ByteOrder, Encoding: opts.ReplayPlan.PCMFormat.Encoding,
		},
		TargetFormat: roomreplay.PCM16Format{SampleRate: format.SampleRate, Channels: format.Channels, FrameDuration: format.FrameDuration},
	}
	for _, plan := range plans {
		if plan == nil || roomParticipantIsHuman(plan) {
			continue
		}
		recorded, ok := opts.ReplayPlan.Participant(plan.manifest.ID)
		if !ok {
			return nil, fmt.Errorf("replay participant %q is missing", plan.manifest.ID)
		}
		sent, ok := replayArtifactByRole(recorded, roomreplay.ArtifactRoleSentPCM)
		if !ok {
			return nil, fmt.Errorf("replay participant %q sent PCM is missing", plan.manifest.ID)
		}
		request.Participants = append(request.Participants, roomreplay.Participant{ID: recorded.ID, CapturePath: recorded.CapturePath, SentPCMPath: sent.AbsolutePath})
		request.TargetIDs = append(request.TargetIDs, plan.manifest.ID)
	}
	for _, event := range opts.ReplayPlan.Timeline {
		request.Timeline = append(request.Timeline, roomreplay.TimelineEvent{Sequence: event.Sequence, OffsetMS: event.OffsetMS, OffsetNanos: event.OffsetNanos, Type: event.Type, ParticipantID: event.ParticipantID})
	}
	return opts.ReplayService.Build(ctx, request)
}

func replayArtifactByRole(participant roomreplay.RoomReplayParticipant, role string) (roomreplay.RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role {
			return artifact, true
		}
	}
	return roomreplay.RoomReplayArtifact{}, false
}

func roomReplayObject(data []byte) (roomReplayJSONObject, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("JSON object is null")
	}
	return object, nil
}

func firstRoomReplayStringField(object, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	for _, source := range []roomReplayJSONObject{object, fallback} {
		if source == nil {
			continue
		}
		for _, name := range names {
			raw, ok := source[name]
			if !ok {
				continue
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return "", true, err
			}
			return strings.TrimSpace(value), true, nil
		}
	}
	return "", false, nil
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func errOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Role == role || artifact.Owner == role || artifact.Owner == "room:"+role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func newRoomReplayBundleError(kind roomreplay.RoomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	if kind == "" {
		kind = roomreplay.RoomReplayBundleMismatch
	}
	if kind == roomreplay.RoomReplayBundleIncomplete {
		cause = gateway.NewReplayIncompleteError(expected, actual, cause)
	} else {
		cause = gateway.NewReplayMismatchError(expected, actual, cause)
	}
	return &roomreplay.RoomReplayBundleError{Kind: kind, Field: field, Artifact: artifact, Expected: expected, Actual: actual, Err: cause}
}
