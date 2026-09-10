package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

type result struct {
	Accepted       bool     `json:"accepted"`
	ErrIncomplete  bool     `json:"err_incomplete"`
	Error          string   `json:"error,omitempty"`
	ReplayExposed  bool     `json:"replay_exposed"`
	EventKinds     []string `json:"event_kinds,omitempty"`
	AudioFrames    int      `json:"audio_frames,omitempty"`
	TotalSamples   int      `json:"total_samples,omitempty"`
	PCMBytes       int      `json:"pcm_bytes,omitempty"`
	PCMSHA256      string   `json:"pcm_sha256,omitempty"`
	EOF            bool     `json:"eof"`
	ClockElapsedNS int64    `json:"clock_elapsed_ns,omitempty"`
}

func main() {
	if len(os.Args) != 2 {
		writeResult(result{Error: "usage: replay-consumer <trace-directory>"})
		os.Exit(2)
	}

	replay, err := recording.OpenReplay(os.Args[1])
	if err != nil {
		writeResult(result{
			ErrIncomplete: errors.Is(err, recording.ErrIncomplete),
			Error:         err.Error(),
			ReplayExposed: false,
		})
		os.Exit(1)
	}

	digest := sha256.New()
	observed := result{Accepted: true, ReplayExposed: true}
	for {
		event, frame, nextErr := replay.Next()
		if errors.Is(nextErr, io.EOF) {
			observed.EOF = true
			break
		}
		if nextErr != nil {
			writeResult(result{Error: nextErr.Error(), ReplayExposed: true})
			os.Exit(1)
		}
		observed.EventKinds = append(observed.EventKinds, event.Kind)
		observed.ClockElapsedNS = replay.Clock.Elapsed().Nanoseconds()
		if event.Kind != "audio" {
			if frame != nil {
				writeResult(result{Error: fmt.Sprintf("control event %q returned a frame", event.Kind), ReplayExposed: true})
				os.Exit(1)
			}
			continue
		}
		if frame == nil {
			writeResult(result{Error: "audio event returned no frame", ReplayExposed: true})
			os.Exit(1)
		}
		observed.AudioFrames++
		observed.TotalSamples += len(frame.Samples)
		observed.PCMBytes += len(frame.Samples) * 2
		var encoded [2]byte
		for _, sample := range frame.Samples {
			binary.LittleEndian.PutUint16(encoded[:], uint16(sample))
			_, _ = digest.Write(encoded[:])
		}
	}
	observed.PCMSHA256 = hex.EncodeToString(digest.Sum(nil))
	writeResult(observed)
}

func writeResult(value result) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
