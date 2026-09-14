package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
)

// Service is stateless; every Load call reads only the paths already admitted
// into the supplied room plan and returns detached audio data.
type Service struct{}

func New() *Service { return &Service{} }

var _ roomaudio.Service = (*Service)(nil)

func (s *Service) Load(plan RoomReplayPlan) (RoomReplayAudioBundle, error) {
	if err := validateRoomReplayPlan(plan); err != nil {
		return RoomReplayAudioBundle{}, err
	}
	manifest, profile, participantObjects, err := loadRoomReplayManifest(plan)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	admittedPlan := cloneRoomReplayPlan(plan)
	participants, streamOwners, err := loadRoomReplayParticipants(admittedPlan, participantObjects)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	roomMix, err := loadRoomReplayMix(admittedPlan, streamOwners)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	result := RoomReplayAudioBundle{Plan: admittedPlan, Format: admittedPlan.PCMFormat, Tolerances: profile, Participants: participants, RoomMix: roomMix}
	annotations, overlaps, barges, loudness, err := parseRoomReplayAudioAnnotations(manifest, admittedPlan, participants, streamOwners)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	result.Annotations, result.Overlaps, result.BargeIns, result.Loudness = annotations, overlaps, barges, loudness
	return result, nil
}

func validateRoomReplayPlan(plan RoomReplayPlan) error {
	if strings.TrimSpace(plan.ManifestPath) == "" {
		return roomReplayAudioIncomplete("run-manifest.json", "", "admitted manifest path", "missing", ErrRoomReplayBundleIncomplete)
	}
	if plan.EndedAt.Before(plan.ClockBase) {
		return roomReplayAudioTimeline("timing", plan.ManifestPath, "non-negative room duration", plan.EndedAt.Sub(plan.ClockBase).String())
	}
	width := plan.PCMFormat.SampleWidthBits
	if width == 0 {
		width = plan.PCMFormat.SampleWidthBit
	}
	if plan.PCMFormat.SampleRate <= 0 || plan.PCMFormat.Channels != 1 || width != 16 || !strings.EqualFold(plan.PCMFormat.ByteOrder, "little") {
		actual := fmt.Sprintf("rate=%d channels=%d bits=%d byte_order=%q", plan.PCMFormat.SampleRate, plan.PCMFormat.Channels, width, plan.PCMFormat.ByteOrder)
		return roomReplayAudioMismatch("pcm_format", plan.ManifestPath, "positive mono little-endian PCM16 format", actual, nil)
	}
	return nil
}

func loadRoomReplayManifest(plan RoomReplayPlan) (roomReplayJSONObject, RoomReplayToleranceProfile, map[string]roomReplayJSONObject, error) {
	data, err := readRoomReplayPath(plan.ManifestPath, maxRoomReplayManifestBytes, "run-manifest.json")
	if err != nil {
		return nil, RoomReplayToleranceProfile{}, nil, err
	}
	manifest, err := roomReplayObject(data)
	if err != nil {
		return nil, RoomReplayToleranceProfile{}, nil, roomReplayAudioMismatch("run-manifest.json", "", "JSON object", "invalid", err)
	}
	profile, err := parseRoomReplayToleranceProfile(manifest)
	if err != nil {
		return nil, RoomReplayToleranceProfile{}, nil, err
	}
	participants, err := roomReplayAudioParticipantObjects(manifest)
	if err != nil {
		return nil, RoomReplayToleranceProfile{}, nil, err
	}
	return manifest, profile, participants, nil
}

func loadRoomReplayParticipants(plan RoomReplayPlan, objects map[string]roomReplayJSONObject) ([]RoomReplayAudioParticipant, map[string]string, error) {
	participants := make([]RoomReplayAudioParticipant, 0, len(plan.Participants))
	owners := make(map[string]string, len(plan.Participants)*3+1)
	for _, participant := range plan.Participants {
		object, ok := objects[participant.ID]
		if !ok {
			return nil, nil, roomReplayAudioIncomplete("participants["+participant.ID+"]", "run-manifest.json", "participant object", "missing", ErrRoomReplayBundleIncomplete)
		}
		resolved, err := loadRoomReplayAudioParticipant(plan, participant, object)
		if err != nil {
			return nil, nil, err
		}
		if err := registerRoomReplayStreams(owners, resolved, participant.ID); err != nil {
			return nil, nil, err
		}
		participants = append(participants, resolved)
	}
	return participants, owners, nil
}

func registerRoomReplayStreams(owners map[string]string, participant RoomReplayAudioParticipant, owner string) error {
	for _, stream := range []RoomReplayAudioStream{participant.WAV, participant.Sent, participant.Received} {
		if previous, exists := owners[stream.StreamID]; exists {
			return roomReplayAudioMismatch("streams."+stream.StreamID, "run-manifest.json", "unique stream identity", previous+" and "+owner, nil)
		}
		owners[stream.StreamID] = owner
	}
	return nil
}

func loadRoomReplayMix(plan RoomReplayPlan, owners map[string]string) (RoomReplayAudioStream, error) {
	artifact, ok := findRoomReplayArtifact(plan.Artifacts, "room:mix")
	if !ok {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifacts.room_mix", "", "validated room mix artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	mix, err := loadRoomReplayWAVStream(plan, artifact, "room:mix", "room", "room-mix")
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	if err := validateRoomReplayAudioStreamTimeline(mix, plan, "room_mix"); err != nil {
		return RoomReplayAudioStream{}, err
	}
	if previous, exists := owners[mix.StreamID]; exists {
		return RoomReplayAudioStream{}, roomReplayAudioMismatch("streams."+mix.StreamID, "run-manifest.json", "unique stream identity", previous+" and room", nil)
	}
	owners[mix.StreamID] = "room"
	return mix, nil
}

func Validate(service roomaudio.Service, plan RoomReplayPlan) error {
	if service == nil {
		return errors.New("room audio service is required")
	}
	_, err := service.Load(plan)
	return err
}
