package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func selectedAudio(fields map[string]json.RawMessage) (string, error) {
	audio, audioPresent, err := optionalString(fields, "audio")
	if err != nil {
		return "", err
	}
	synthetic, syntheticPresent, err := optionalString(fields, "synthetic_audio")
	if err != nil {
		return "", err
	}
	if !audioPresent && !syntheticPresent {
		return "", errors.New("missing audio or synthetic_audio")
	}
	if audio == "" {
		return synthetic, nil
	}
	return audio, nil
}

func streamDirection(fields map[string]json.RawMessage, record metricsreplay.Record, transcript bool) (metricsreplay.Direction, error) {
	role, present, err := optionalString(fields, "role")
	if err != nil {
		return "", err
	}
	if present {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "user":
			return metricsreplay.DirectionInput, nil
		case "assistant", "tool", "":
			return metricsreplay.DirectionOutput, nil
		default:
			return "", fmt.Errorf("unsupported stream role %q", role)
		}
	}
	if transcript {
		return metricsreplay.DirectionInput, nil
	}
	if record.Direction == metricsreplay.WireDirectionClientToServer {
		return metricsreplay.DirectionInput, nil
	}
	return metricsreplay.DirectionOutput, nil
}

func requireDirection(record metricsreplay.Record, want metricsreplay.WireDirection) error {
	if record.Direction != want {
		return fmt.Errorf("expected %q, got %q", want, record.Direction)
	}
	return nil
}

func addBytes(sums map[seriesKey]int64, key seriesKey, bytesCount int) error {
	if bytesCount < 0 || int64(bytesCount) < 0 {
		return errors.New("byte count overflow")
	}
	value := int64(bytesCount)
	if sums[key] > math.MaxInt64-value {
		return errors.New("series byte total overflow")
	}
	sums[key] += value
	return nil
}

func decodedBase64Len(encoded string) int {
	if encoded == "" {
		return 0
	}
	decoded, err := codec.DecodeBase64(encoded)
	if err != nil {
		return len(encoded)
	}
	return len(decoded)
}

func malformedRecord(index int, record metricsreplay.Record, reason string, cause error) error {
	label := fmt.Sprintf("record %d", index+1)
	if record.Sequence > 0 {
		label = fmt.Sprintf("record %d (sequence %d)", index+1, record.Sequence)
	}
	if record.Type != "" {
		label += " " + record.Type
	}
	message := fmt.Sprintf("%s: %s", label, reason)
	if cause == nil {
		return fmt.Errorf("%w: %s", metricsreplay.ErrMalformedFixture, message)
	}
	return errors.Join(metricsreplay.ErrMalformedFixture, fmt.Errorf("%s: %w", message, cause))
}
