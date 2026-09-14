package agentruntime

import (
	"os"
	"reflect"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

// RoomEvidenceManifestPath remains a source-compatible name for the retained
// replay admission files. The manifest contract is owned by roomevidence.
const RoomEvidenceManifestPath = roomevidence.ManifestPath

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func sortedRoomIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	for index := 1; index < len(result); index++ {
		value := result[index]
		position := index
		for position > 0 && result[position-1] > value {
			result[position] = result[position-1]
			position--
		}
		result[position] = value
	}
	return result
}

func roomFormatForOptions(opts RoomRunOptions) room.PCM16Format {
	format := roomMixerConfig(opts).Format
	if format == (room.PCM16Format{}) {
		return room.DefaultPCM16Format()
	}
	return format
}

func roomCredentialSecrets(manifest room.Manifest, options room.ValidationOptions) []string {
	lookup := options.LookupCredential
	if lookup == nil {
		lookup = os.LookupEnv
	}
	secrets := make([]string, 0, len(manifest.Participants))
	for _, participant := range manifest.Participants {
		if value, ok := lookup(participant.APIKeyEnv); ok && value != "" {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

func roomMixerConfig(opts RoomRunOptions) room.PCM16MixerConfig {
	config := opts.MixerConfig
	if opts.PCMFormat != (room.PCM16Format{}) {
		config.Format = opts.PCMFormat
	} else if opts.FrameSamples > 0 {
		format := room.DefaultPCM16Format()
		format.FrameDuration = time.Duration(opts.FrameSamples) * time.Second / time.Duration(format.SampleRate)
		config.Format = format
	}
	return config
}

func cloneRoomRecordingStatus(status *transcript.RecordingStatus) *transcript.RecordingStatus {
	if status == nil {
		return nil
	}
	clone := *status
	return &clone
}

func cloneRoomStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

// roomReplayParticipantArtifact is a decision-free lookup retained for the
// replay scheduler while its owner moves to the public room replay contract.
func roomReplayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}
