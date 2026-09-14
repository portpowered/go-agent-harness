package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/internal/admission"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Service is the private implementation behind the public room evidence
// contract. It is intentionally stateless; each Open call creates an
// independent recorder with no process-wide ownership or mutable globals.
type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) ValidateOutput(path string) error {
	destination := filepath.Clean(strings.TrimSpace(path))
	if strings.TrimSpace(path) == "" || destination == "." {
		return fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	return validateOutputTarget(destination)
}

func (s *Service) PrepareOutput(path string) (string, error) {
	destination := filepath.Clean(strings.TrimSpace(path))
	if strings.TrimSpace(path) == "" || destination == "." {
		return "", fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	if err := s.ValidateOutput(destination); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destination, evidenceDirectoryMode); err != nil {
		return "", fmt.Errorf("create room evidence output directory %q: %w", destination, err)
	}
	return destination, nil
}

func (s *Service) Open(options roomevidence.Options) (roomevidence.Recorder, error) {
	return newRecorder(options)
}

func (s *Service) LoadPlan(bundle string) (roomevidence.RoomReplayPlan, error) {
	return admission.New().Load(bundle)
}

func (s *Service) ValidateReplayOutput(plan roomevidence.RoomReplayPlan, destination string) error {
	raw := strings.TrimSpace(destination)
	if raw == "" {
		return fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	source, err := filepath.Abs(filepath.Clean(plan.BundlePath))
	if err != nil {
		return fmt.Errorf("resolve room replay bundle path: %w", err)
	}
	output, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return fmt.Errorf("resolve room replay output path: %w", err)
	}
	relative, err := filepath.Rel(source, output)
	if err != nil {
		return fmt.Errorf("compare room replay source and output paths: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("room replay output directory %q must be outside source bundle %q", destination, plan.BundlePath)
	}
	return validateOutputTarget(output)
}

func (s *Service) ValidateEvidenceOutput(destination string) error {
	raw := strings.TrimSpace(destination)
	if raw == "" || filepath.Clean(raw) == "." {
		return fmt.Errorf("%w: directory is required", roomevidence.ErrInvalidOutput)
	}
	return validateOutputTarget(filepath.Clean(raw))
}

func (s *Service) CreateFreshRunDirectory(configDir string) (string, error) {
	if strings.TrimSpace(configDir) == "" {
		return "", fmt.Errorf("%w: config directory is required", roomevidence.ErrInvalidOutput)
	}
	if err := os.MkdirAll(configDir, evidenceDirectoryMode); err != nil {
		return "", fmt.Errorf("create room config directory %q: %w", configDir, err)
	}
	directory, err := os.MkdirTemp(configDir, "room-run-")
	if err != nil {
		return "", fmt.Errorf("create fresh room run directory under %q: %w", configDir, err)
	}
	return filepath.Clean(directory), nil
}

// Load validates and resolves the audio projection of an already admitted
// room plan. The loader shares this service with recording so all bundle
// policy, formats, integrity and analysis projection have one authority.
func (s *Service) Load(plan RoomReplayPlan) (roomevidence.RoomReplayAudioBundle, error) {
	if err := validateRoomReplayPlan(plan); err != nil {
		return roomevidence.RoomReplayAudioBundle{}, err
	}
	manifest, profile, participantObjects, err := loadRoomReplayManifest(plan)
	if err != nil {
		return roomevidence.RoomReplayAudioBundle{}, err
	}
	admittedPlan := cloneRoomReplayPlan(plan)
	participants, streamOwners, err := loadRoomReplayParticipants(admittedPlan, participantObjects)
	if err != nil {
		return roomevidence.RoomReplayAudioBundle{}, err
	}
	roomMix, err := loadRoomReplayMix(admittedPlan, streamOwners)
	if err != nil {
		return roomevidence.RoomReplayAudioBundle{}, err
	}
	result := roomevidence.RoomReplayAudioBundle{
		Plan: admittedPlan, Format: admittedPlan.PCMFormat, Tolerances: profile,
		Participants: participants, RoomMix: roomMix,
	}
	annotations, overlaps, barges, loudness, err := parseRoomReplayAudioAnnotations(manifest, admittedPlan, participants, streamOwners)
	if err != nil {
		return roomevidence.RoomReplayAudioBundle{}, err
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

var _ roomevidence.Service = (*Service)(nil)

func Validate(service roomevidence.Service, plan RoomReplayPlan) error {
	if service == nil {
		return errors.New("room evidence service is required")
	}
	_, err := service.Load(plan)
	return err
}

func validateOutputTarget(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, evidenceDirectoryMode); err != nil {
		return fmt.Errorf("prepare room evidence output parent %q: %w", destination, err)
	}
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: target %q must be a non-symlink directory", roomevidence.ErrInvalidOutput, destination)
		}
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return fmt.Errorf("inspect room evidence output directory %q: %w", destination, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("%w: %q is not safe: it must be empty", roomevidence.ErrOutputNotEmpty, destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect room evidence output target %q: %w", destination, err)
	}
	probe, err := os.CreateTemp(parent, ".room-evidence-probe-")
	if err != nil {
		return fmt.Errorf("probe room evidence output target %q: %w", destination, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close room evidence output probe %q: %w", destination, closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove room evidence output probe %q: %w", destination, removeErr)
	}
	return nil
}

func (r *recorder) hashArtifactInto(integrity map[string]artifactIntegrity, relative string) {
	if strings.TrimSpace(relative) == "" {
		return
	}
	path := filepath.Join(r.destination, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	hash, err := hashFile(path)
	if err != nil {
		return
	}
	integrity[filepath.ToSlash(relative)] = artifactIntegrity{Size: info.Size(), SHA256: hash}
}

func hashFile(path string) (hash string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeManifestFile(path string, manifest roomManifest, secrets []string) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal room run manifest: %w", err)
	}
	data = append(redactJSON(data, secrets), '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".run-manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create room run manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := writeManifestTemporary(temporary, data); err != nil {
		return errors.Join(fmt.Errorf("write room run manifest temporary file: %w", err), removeManifestTemporary(temporaryPath))
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.Join(fmt.Errorf("replace room run manifest: %w", err), removeManifestTemporary(temporaryPath))
	}
	return nil
}

func writeManifestTemporary(file *os.File, data []byte) (err error) {
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close room run manifest temporary file: %w", closeErr))
		}
	}()
	if err := writeAll(file, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

func removeManifestTemporary(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove room run manifest temporary file: %w", err)
	}
	return nil
}

func (r *recorder) cleanupOpenFiles() {
	if r.timeline != nil {
		r.recordCleanupError("", roomevidence.TimelinePath, r.timeline.close())
		r.removeCleanupFile(roomevidence.TimelinePath)
	}
	for _, participant := range r.participants {
		if participant == nil {
			continue
		}
		r.recordCleanupError(participant.id, participant.artifacts.WAV, participant.wav.close())
		r.recordCleanupError(participant.id, participant.artifacts.Diagnostics, participant.diagnostics.close())
		r.recordCleanupError(participant.id, participant.artifacts.Deltas, participant.deltas.close())
		r.recordCleanupError(participant.id, participant.artifacts.Events, participant.events.close())
		r.recordCleanupError(participant.id, participant.artifacts.SentPCM, participant.sentPCM.close())
		r.recordCleanupError(participant.id, participant.artifacts.ReceivedPCM, participant.receivedPCM.close())
		for _, path := range []string{participant.artifacts.WAV, participant.artifacts.Diagnostics, participant.artifacts.Deltas, participant.artifacts.Events, participant.artifacts.SentPCM, participant.artifacts.ReceivedPCM} {
			r.removeCleanupFile(path)
		}
	}
}

func (r *recorder) recordCleanupError(participant, artifact string, err error) {
	if err != nil {
		r.recordError(participant, artifact, err)
	}
}

func (r *recorder) removeCleanupFile(relative string) {
	path := filepath.Join(r.destination, filepath.FromSlash(relative))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.recordError("", relative, err)
	}
}
