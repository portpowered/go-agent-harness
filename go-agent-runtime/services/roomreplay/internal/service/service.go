package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/internal/audiobundle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/internal/support"
)

type RoomReplayBundleErrorKind = roomreplay.RoomReplayBundleErrorKind
type RoomReplayBundleError = roomreplay.RoomReplayBundleError
type RoomReplayPlan = roomreplay.RoomReplayPlan
type RoomReplayPCMFormat = roomreplay.RoomReplayPCMFormat
type RoomReplayArtifact = roomreplay.RoomReplayArtifact
type RoomReplayParticipant = roomreplay.RoomReplayParticipant
type RoomReplayTimelineEvent = roomreplay.RoomReplayTimelineEvent
type ParticipantKind = roomreplay.ParticipantKind

const (
	RoomReplayBundleSchemaVersion = roomreplay.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath  = roomreplay.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch      = roomreplay.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete    = roomreplay.RoomReplayBundleIncomplete
)

const (
	ErrInvalidRoomReplayBundle    = roomreplay.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete = roomreplay.ErrRoomReplayBundleIncomplete
)

// Service owns the private parser, filesystem admission, and integrity
// validation implementation. It has no invocation state and is safe to share
// as a constructor across independent hosts.
type Service struct {
	replayService replay.CaptureInspector
}

func New(replayService replay.CaptureInspector) *Service {
	return &Service{replayService: replayService}
}

func (s *Service) Load(bundle string) (RoomReplayPlan, error) {
	root, manifestPath, manifestRelative, err := resolveRoomReplayBundle(bundle)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	data, err := readRoomReplayManifest(manifestPath)
	if err != nil {
		kind := RoomReplayBundleIncomplete
		if !errors.Is(err, os.ErrNotExist) {
			kind = RoomReplayBundleMismatch
		}
		return RoomReplayPlan{}, newRoomReplayBundleError(kind, "run-manifest.json", manifestRelative, "readable JSON manifest", err.Error(), err)
	}
	return validateRoomReplayManifest(root, manifestPath, data, s.replayService)
}

func (s *Service) LoadAudioBundle(bundle string) (roomreplay.RoomReplayAudioBundle, error) {
	plan, err := s.Load(bundle)
	if err != nil {
		return roomreplay.RoomReplayAudioBundle{}, err
	}
	return audiobundle.Load(plan)
}

func prepareReplayBuildRequest(request roomreplay.BuildRequest) (roomreplay.BuildRequest, error) {
	plan := request.ReplayPlan
	if plan == nil {
		return request, nil
	}
	if len(request.Participants) != 0 || len(request.Timeline) != 0 || request.SourceFormat != (roomreplay.SourcePCM16Format{}) {
		return roomreplay.BuildRequest{}, fmt.Errorf("%w: replay plan cannot be combined with explicit replay sources", roomreplay.ErrInvalidRequest)
	}
	request.SourceFormat = roomreplay.SourcePCM16Format{
		SampleRate: plan.PCMFormat.SampleRate, Channels: plan.PCMFormat.Channels,
		SampleWidthBits: plan.PCMFormat.SampleWidthBits, SampleWidthBit: plan.PCMFormat.SampleWidthBit,
		ByteOrder: plan.PCMFormat.ByteOrder, Encoding: plan.PCMFormat.Encoding,
	}
	request.Timeline = make([]roomreplay.TimelineEvent, 0, len(plan.Timeline))
	for _, event := range plan.Timeline {
		request.Timeline = append(request.Timeline, roomreplay.TimelineEvent{
			Sequence: event.Sequence, OffsetMS: event.OffsetMS, OffsetNanos: event.OffsetNanos,
			Type: event.Type, ParticipantID: event.ParticipantID,
		})
	}
	for _, id := range request.TargetIDs {
		participant, ok := plan.Participant(id)
		if !ok {
			return roomreplay.BuildRequest{}, fmt.Errorf("replay participant %q is missing", id)
		}
		if replayParticipantSentPCMPath(participant) == "" {
			return roomreplay.BuildRequest{}, fmt.Errorf("replay participant %q sent PCM is missing", id)
		}
	}
	request.Participants = make([]roomreplay.Participant, 0, len(plan.Participants))
	for _, participant := range plan.Participants {
		sentPath := replayParticipantSentPCMPath(participant)
		if sentPath == "" {
			continue
		}
		request.Participants = append(request.Participants, roomreplay.Participant{
			ID: participant.ID, CapturePath: participant.CapturePath, SentPCMPath: sentPath,
		})
	}
	request.ReplayPlan = nil
	return request, nil
}

func replayParticipantSentPCMPath(participant roomreplay.RoomReplayParticipant) string {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == roomreplay.ArtifactRoleSentPCM {
			return artifact.AbsolutePath
		}
	}
	return ""
}

func (s *Service) ValidateOutput(plan RoomReplayPlan, destination string) error {
	raw := strings.TrimSpace(destination)
	if raw == "" {
		return errors.New("room replay output directory is required")
	}
	root, err := filepath.Abs(filepath.Clean(plan.BundlePath))
	if err != nil {
		return fmt.Errorf("resolve room replay bundle path: %w", err)
	}
	root, err = resolveRoomReplayOutputPath(root)
	if err != nil {
		return fmt.Errorf("resolve room replay bundle path %q: %w", plan.BundlePath, err)
	}
	output, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return fmt.Errorf("resolve room replay output path: %w", err)
	}
	output, err = resolveRoomReplayOutputPath(output)
	if err != nil {
		return fmt.Errorf("resolve room replay output path %q: %w", destination, err)
	}
	relative, err := filepath.Rel(root, output)
	if err != nil {
		return fmt.Errorf("compare room replay source and output paths: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("room replay output directory %q must be outside source bundle %q", destination, plan.BundlePath)
	}
	return nil
}

func resolveRoomReplayOutputPath(output string) (string, error) {
	existing, missing, err := nearestExistingRoomReplayPath(output)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if len(missing) > 0 {
		info, err := os.Stat(resolved)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("existing path component %q is not a directory", existing)
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func nearestExistingRoomReplayPath(output string) (string, []string, error) {
	missing := make([]string, 0, 1)
	for {
		_, err := os.Lstat(output)
		if err == nil {
			return output, missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(output)
		if parent == output {
			return "", nil, err
		}
		missing = append(missing, filepath.Base(output))
		output = parent
	}
}

func newRoomReplayBundleError(kind RoomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	return support.BundleError(kind, field, artifact, expected, actual, cause)
}

var _ roomreplay.Service = (*Service)(nil)
