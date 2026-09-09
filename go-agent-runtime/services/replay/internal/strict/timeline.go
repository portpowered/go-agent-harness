package strict

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

const timelineScanBuffer = 4096

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", replay.ErrBundleIncomplete)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func resolveTraceDirectory(bundlePath string) (string, error) {
	if info, err := os.Stat(filepath.Join(bundlePath, "timeline.jsonl")); err == nil && !info.IsDir() {
		return bundlePath, nil
	}
	tracePath := filepath.Join(bundlePath, "audio-trace")
	if info, err := os.Stat(filepath.Join(tracePath, "timeline.jsonl")); err == nil && !info.IsDir() {
		return tracePath, nil
	}
	return "", fmt.Errorf("%w: missing timeline.jsonl in %s or %s", replay.ErrBundleIncomplete, bundlePath, tracePath)
}

func readTimeline(directory string) (events []recording.Event, err error) {
	file, err := os.Open(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("%w: open timeline: %w", replay.ErrBundleIncomplete, err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, timelineScanBuffer), 2*recording.MaxRuntimePayloadBytes)
	for scan.Scan() {
		var event recording.Event
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("%w: decode timeline: %w", replay.ErrBundleIncomplete, err)
		}
		if event.Sequence != uint64(len(events)+1) || event.ElapsedNS < 0 {
			return nil, fmt.Errorf("%w: timeline sequence or elapsed time is invalid", replay.ErrBundleIncomplete)
		}
		events = append(events, event)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("%w: read timeline: %w", replay.ErrBundleIncomplete, err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: timeline has no events", replay.ErrBundleIncomplete)
	}
	return events, nil
}

func timelineOrigin(events []recording.Event) (time.Time, error) {
	if len(events) == 0 || events[0].Timestamp == "" {
		return time.Time{}, fmt.Errorf("%w: timeline has no recording origin", replay.ErrBundleIncomplete)
	}
	origin, err := time.Parse(time.RFC3339Nano, events[0].Timestamp)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid recording origin: %w", replay.ErrBundleIncomplete, err)
	}
	return origin, nil
}
