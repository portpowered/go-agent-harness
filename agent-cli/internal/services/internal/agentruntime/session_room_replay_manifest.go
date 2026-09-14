package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Deprecated: bundle admission and manifest decoding live in the runtime
// roomreplaybundle service. These small JSON helpers remain only for the
// separately owned audio projection and for unchanged CLI test fixtures.
type roomReplayJSONObject map[string]json.RawMessage

const (
	roomReplayArtifactRoleWAV         = "wav"
	roomReplayArtifactRoleDiagnostics = "diagnostics"
	roomReplayArtifactRoleDeltas      = "deltas"
	roomReplayArtifactRoleSentPCM     = "sent_pcm"
	roomReplayArtifactRoleReceivedPCM = "received_pcm"
	roomReplayArtifactRoleEvents      = "events"
	roomReplayArtifactRoleCapture     = "capture"
)

var roomReplayRequiredParticipantArtifactRoles = []string{
	roomReplayArtifactRoleWAV,
	roomReplayArtifactRoleDiagnostics,
	roomReplayArtifactRoleDeltas,
	roomReplayArtifactRoleSentPCM,
	roomReplayArtifactRoleReceivedPCM,
	roomReplayArtifactRoleEvents,
}

func errOrDefault(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func roomReplayObject(raw json.RawMessage) (roomReplayJSONObject, error) {
	var object roomReplayJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	return object, nil
}

func roomReplayRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayStringField(object roomReplayJSONObject, names ...string) (string, bool, error) {
	raw, ok := roomReplayRawField(object, names...)
	if !ok {
		return "", false, nil
	}
	value, ok := decodeRoomReplayString(raw)
	if !ok {
		return "", true, errors.New("expected string")
	}
	return strings.TrimSpace(value), true, nil
}

func roomReplayIntField(object roomReplayJSONObject, name string) (int, bool, error) {
	raw, ok := roomReplayRawField(object, name)
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.Atoi(number.String())
	if err != nil {
		return 0, true, err
	}
	return value, true, nil
}

func firstRoomReplayIntField(primary, fallback roomReplayJSONObject, names ...string) (int, bool, error) {
	for _, object := range []roomReplayJSONObject{primary, fallback} {
		if object == nil {
			continue
		}
		for _, name := range names {
			if value, present, err := roomReplayIntField(object, name); present {
				return value, true, err
			}
		}
	}
	return 0, false, errors.New("missing integer")
}

func firstRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	for _, object := range []roomReplayJSONObject{primary, fallback} {
		if object == nil {
			continue
		}
		for _, name := range names {
			if value, present, err := roomReplayStringField(object, name); present {
				return value, true, err
			}
		}
	}
	return "", false, errors.New("missing string")
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, owner string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Owner == owner {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func normalizeRoomReplayArtifactRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_", "/", "_").Replace(normalized)
	switch {
	case strings.Contains(normalized, "room_mix") || normalized == "mix":
		return "mix"
	case normalized == "wav" || strings.Contains(normalized, "legacy_wav") || strings.HasSuffix(normalized, "_wav") || strings.HasSuffix(normalized, "_audio"):
		return roomReplayArtifactRoleWAV
	case strings.Contains(normalized, "diagnostic"):
		return roomReplayArtifactRoleDiagnostics
	case strings.Contains(normalized, "delta"):
		return roomReplayArtifactRoleDeltas
	case strings.Contains(normalized, "sent") && (strings.Contains(normalized, "pcm") || normalized == "sent"):
		return roomReplayArtifactRoleSentPCM
	case strings.Contains(normalized, "received") && (strings.Contains(normalized, "pcm") || normalized == "received"):
		return roomReplayArtifactRoleReceivedPCM
	case strings.Contains(normalized, "event"):
		return roomReplayArtifactRoleEvents
	case strings.Contains(normalized, "capture") || strings.Contains(normalized, "replay") || strings.Contains(normalized, "session"):
		return roomReplayArtifactRoleCapture
	case strings.Contains(normalized, "timeline"):
		return "timeline"
	case strings.Contains(normalized, "mix"):
		return "mix"
	default:
		return normalized
	}
}

// Deprecated: only the CLI round-trip compatibility test needs to parse the
// legacy PCM field spellings. Admission validates the same contract privately.
func parseRoomReplayPCMFormat(object roomReplayJSONObject) (RoomReplayPCMFormat, error) {
	format := RoomReplayPCMFormat{}
	container := object
	for _, key := range []string{"pcm_format", "pcm", "audio_format"} {
		if raw, ok := roomReplayRawField(object, key); ok {
			if nested, err := roomReplayObject(raw); err == nil {
				container = nested
				break
			}
		}
	}
	var err error
	format.SampleRate, _, err = firstRoomReplayIntField(container, object, "sample_rate_hz", "sample_rate", "sampleRate")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_rate_hz", "", "positive sample rate", "invalid or missing", err)
	}
	format.Channels, _, err = firstRoomReplayIntField(container, object, "channels", "channel_count")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.channels", "", "positive channel count", "invalid or missing", err)
	}
	format.SampleWidthBits, _, err = firstRoomReplayIntField(container, object, "sample_width_bits", "sample_width", "bits_per_sample")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.sample_width_bits", "", "16", "invalid or missing", err)
	}
	format.SampleWidthBit = format.SampleWidthBits
	format.ByteOrder, _, err = firstRoomReplayStringField(container, object, "byte_order", "endianness")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.byte_order", "", "little", "invalid or missing", err)
	}
	format.Encoding, _, err = firstRoomReplayStringField(container, object, "encoding", "sample_encoding", "format")
	if err != nil {
		return RoomReplayPCMFormat{}, newRoomReplayBundleError(RoomReplayBundleIncomplete, "pcm_format.encoding", "", "signed_pcm16", "invalid or missing", err)
	}
	return format, nil
}

// Deprecated: this is a compatibility assertion for the audio projection;
// runtime bundle admission owns the authoritative validation path.
func validateRoomReplayPCMFormat(format RoomReplayPCMFormat) error {
	sampleWidth := format.SampleWidthBits
	if sampleWidth == 0 {
		sampleWidth = format.SampleWidthBit
	}
	if format.SampleRate <= 0 || format.Channels <= 0 || sampleWidth != 16 || !strings.EqualFold(format.ByteOrder, "little") {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format", "", "signed little-endian PCM16 with positive rate/channels", fmt.Sprintf("rate=%d channels=%d width=%d byte_order=%q", format.SampleRate, format.Channels, sampleWidth, format.ByteOrder), ErrInvalidRoomReplayBundle)
	}
	encoding := strings.ToLower(strings.TrimSpace(format.Encoding))
	if encoding != "signed_pcm16" && encoding != "pcm_s16le" && encoding != "pcm16" && encoding != "signed 16-bit pcm" {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "pcm_format.encoding", "", "signed_pcm16", format.Encoding, ErrInvalidRoomReplayBundle)
	}
	return nil
}
