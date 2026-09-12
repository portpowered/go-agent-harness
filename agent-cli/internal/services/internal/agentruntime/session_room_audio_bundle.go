package agentruntime

import (
	"encoding/json"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	roomaudiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio/wire"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

// The audio projection is now owned by the host-neutral runtime service. The
// aliases keep the CLI's historical test and command vocabulary source
// compatible while preventing the decoder from depending on CLI state.

const (
	ErrRoomReplayDeltaReconstruction = roomaudio.ErrRoomReplayDeltaReconstruction
	ErrRoomReplayAudioTimeline       = roomaudio.ErrRoomReplayAudioTimeline
	ErrRoomReplayToleranceProfile    = roomaudio.ErrRoomReplayToleranceProfile
)

type (
	RoomReplayToleranceProfile         = roomaudio.RoomReplayToleranceProfile
	RoomReplayAudioDelta               = roomaudio.RoomReplayAudioDelta
	RoomReplayDeltaReconstructionError = roomaudio.RoomReplayDeltaReconstructionError
	RoomReplayAudioStream              = roomaudio.RoomReplayAudioStream
	RoomReplayAudioParticipant         = roomaudio.RoomReplayAudioParticipant
	RoomReplayAudioAnnotation          = roomaudio.RoomReplayAudioAnnotation
	RoomReplayAudioBundle              = roomaudio.RoomReplayAudioBundle
)

func DefaultRoomReplayToleranceProfile() RoomReplayToleranceProfile {
	return RoomReplayToleranceProfile{
		Name:         "suite-default",
		StreamConfig: roomanalysis.DefaultPCM16AnalysisConfig(),
		RoomConfig:   roomanalysis.DefaultPCM16RoomAnalysisConfig(),
	}
}

// LoadRoomReplayAudioBundle retains the CLI path-admission boundary and then
// delegates all audio parsing to the public roomaudio service.
func LoadRoomReplayAudioBundle(bundle string) (RoomReplayAudioBundle, error) {
	plan, err := LoadRoomReplayPlan(bundle)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	loaded, err := roomaudiowire.NewService().Load(toRoomAudioPlan(plan))
	if err != nil {
		return RoomReplayAudioBundle{}, adaptRoomReplayAudioError(err)
	}
	return loaded, nil
}

func ValidateRoomReplayAudioBundle(bundle string) error {
	_, err := LoadRoomReplayAudioBundle(bundle)
	return err
}

func adaptRoomReplayAudioError(err error) error {
	var detail *roomaudio.RoomReplayBundleError
	if !errors.As(err, &detail) {
		return err
	}
	legacy := &RoomReplayBundleError{
		Kind:     RoomReplayBundleErrorKind(detail.Kind),
		Field:    detail.Field,
		Artifact: detail.Artifact,
		Expected: detail.Expected,
		Actual:   detail.Actual,
		Err:      detail.Err,
	}
	// Keep both the legacy error identity used by CLI callers and the public
	// service detail available to diagnostics and errors.As callers.
	return errors.Join(legacy, err)
}

func toRoomAudioPlan(plan RoomReplayPlan) roomaudio.RoomReplayPlan {
	converted := roomaudio.RoomReplayPlan{
		BundlePath:    plan.BundlePath,
		ManifestPath:  plan.ManifestPath,
		SchemaVersion: plan.SchemaVersion,
		Finalized:     plan.Finalized,
		ClockBase:     plan.ClockBase,
		StartedAt:     plan.StartedAt,
		EndedAt:       plan.EndedAt,
		PCMFormat:     runtimeRooms.RoomReplayPCMFormat{SampleRate: plan.PCMFormat.SampleRate, Channels: plan.PCMFormat.Channels, SampleWidthBits: plan.PCMFormat.SampleWidthBits, SampleWidthBit: plan.PCMFormat.SampleWidthBit, ByteOrder: plan.PCMFormat.ByteOrder, Encoding: plan.PCMFormat.Encoding},
		TimelinePath:  plan.TimelinePath,
		RoomMixPath:   plan.RoomMixPath,
	}
	converted.Participants = make([]runtimeRooms.RoomReplayParticipant, 0, len(plan.Participants))
	for _, participant := range plan.Participants {
		convertedParticipant := runtimeRooms.RoomReplayParticipant{
			ID: participant.ID, Kind: runtimeRooms.ParticipantKind(participant.Kind), Provider: participant.Provider, Model: participant.Model,
			Voice: participant.Voice, OpeningPrompt: participant.OpeningPrompt, SystemPrompt: participant.SystemPrompt,
			CapturePath: participant.CapturePath, Capture: toRoomAudioArtifact(participant.Capture), RecordedTurnCount: participant.RecordedTurnCount,
			Artifacts: make([]runtimeRooms.RoomReplayArtifact, 0, len(participant.Artifacts)),
		}
		for _, artifact := range participant.Artifacts {
			convertedParticipant.Artifacts = append(convertedParticipant.Artifacts, toRoomAudioArtifact(artifact))
		}
		converted.Participants = append(converted.Participants, convertedParticipant)
	}
	converted.Timeline = make([]runtimeRooms.RoomReplayTimelineEvent, 0, len(plan.Timeline))
	for _, event := range plan.Timeline {
		converted.Timeline = append(converted.Timeline, runtimeRooms.RoomReplayTimelineEvent{
			Sequence: event.Sequence, OffsetMS: event.OffsetMS, OffsetNanos: event.OffsetNanos, UnixMS: event.UnixMS,
			Type: event.Type, ParticipantID: event.ParticipantID, Raw: append(json.RawMessage(nil), event.Raw...),
		})
	}
	converted.Artifacts = make([]runtimeRooms.RoomReplayArtifact, 0, len(plan.Artifacts))
	for _, artifact := range plan.Artifacts {
		converted.Artifacts = append(converted.Artifacts, toRoomAudioArtifact(artifact))
	}
	return converted
}

func toRoomAudioArtifact(artifact RoomReplayArtifact) runtimeRooms.RoomReplayArtifact {
	return runtimeRooms.RoomReplayArtifact{
		Name: artifact.Name, Role: artifact.Role, Owner: artifact.Owner, Path: artifact.Path,
		AbsolutePath: artifact.AbsolutePath, Size: artifact.Size, SHA256: artifact.SHA256,
	}
}

// roomReplayParticipantArtifact remains a decision-free CLI lookup for the
// scheduler's existing session composition. Audio decoding itself is owned by
// roomaudio/internal/service.
func roomReplayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}
