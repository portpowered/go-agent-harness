package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	tracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

const probeSecret = "c112-traceprobe-secret"

var requiredTaps = []string{
	"microphone_pre_gate",
	"microphone_uploaded",
	"speaker_enqueued",
	"speaker_rendered",
}

func main() {
	output := flag.String("output", "", "directory in which to publish the boundary probe")
	caseName := flag.String("case", "", "evidence case label")
	flag.Parse()
	if err := run(*output, *caseName); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(output, caseName string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("trace probe output is required")
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return fmt.Errorf("create trace probe output: %w", err)
	}
	prepared, err := tracewire.NewService().Prepare(sessiontrace.Request{
		TraceAudio:      true,
		RecordDirectory: filepath.Join(output, "requested"),
		Clock:           clock.NewDeterministic(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Millisecond),
		Credentials:     []string{probeSecret},
	})
	if err != nil {
		return fmt.Errorf("prepare trace probe: %w", err)
	}
	binding := prepared.DeviceBinding()
	samples := []int16{1, 2, 3, 4, 5, 6}
	binding.PreGateSamplesObserver(16_000, samples)
	binding.UploadedSamplesObserver(24_000, samples)
	if err := binding.PlaybackSamplesObserver(context.Background(), 16_000, samples); err != nil {
		return fmt.Errorf("capture enqueued probe: %w", err)
	}
	binding.RenderedSamplesObserver(16_000, samples)
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind:    "provider_wire_send",
		Tick:    1,
		Payload: []byte(`{"type":"input_audio_buffer.append","api_key":"` + probeSecret + `"}`),
		Error:   probeSecret,
	})
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind:    "provider_wire_receive",
		Tick:    2,
		Payload: []byte(`{"type":"response.created"}`),
	})
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{
		Kind:  sessiontrace.SessionRuntimeObservationTerminal,
		Tick:  3,
		Clean: true,
	})
	bundle := filepath.Join(output, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		return fmt.Errorf("create trace probe bundle: %w", err)
	}
	if err := prepared.Finish(context.Background(), bundle, true); err != nil {
		return fmt.Errorf("publish trace probe: %w", err)
	}
	timeline := filepath.Join(bundle, "audio-trace", "timeline.jsonl")
	if err := validateTimeline(timeline); err != nil {
		return err
	}
	result, err := json.Marshal(map[string]any{
		"case":   caseName,
		"bundle": bundle,
		"taps":   requiredTaps,
	})
	if err != nil {
		return fmt.Errorf("encode trace probe result: %w", err)
	}
	fmt.Println(string(result))
	return nil
}

func validateTimeline(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open trace probe timeline: %w", err)
	}
	defer file.Close()
	seen := make(map[string]bool, len(requiredTaps))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event recording.Event
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("decode trace probe timeline: %w", err)
		}
		if event.Kind == "audio" {
			seen[event.Tap] = true
		}
		if strings.Contains(string(event.Payload), probeSecret) || strings.Contains(event.Error, probeSecret) {
			return errors.New("trace probe leaked its credential")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read trace probe timeline: %w", err)
	}
	for _, tap := range requiredTaps {
		if !seen[tap] {
			return fmt.Errorf("trace probe timeline missing %s", tap)
		}
	}
	return nil
}
