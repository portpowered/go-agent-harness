package wire

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// fixtureCaptureInspector derives capture facts from the recorded provider
// session itself, so audio-bundle admission still rejects a capture that is
// not a replayable realtime websocket trace.
type fixtureCaptureInspector struct{}

func (fixtureCaptureInspector) InspectCapture(ctx context.Context, path string) (replay.CaptureInspection, error) {
	if err := ctx.Err(); err != nil {
		return replay.CaptureInspection{}, err
	}
	loaded, err := gwtesting.LoadSessionCaptureForReplay(path)
	if err != nil {
		return replay.CaptureInspection{}, err
	}
	facts := replay.CaptureFacts{Version: loaded.Capture.Version, EventCount: len(loaded.Capture.Records)}
	realtime := len(loaded.Capture.Records) > 0
	for _, record := range loaded.Capture.Records {
		realtime = realtime && record.PayloadType == gwtesting.SessionPayloadTypeWebSocketMessage
		if record.Direction == gwtesting.DirectionClientToServer && record.Type == "input_audio_buffer.append" {
			facts.ClientAudioAppendCount++
		}
	}
	kind := replay.CaptureKindTurn
	if realtime {
		if _, err := gwtesting.NewReplayWebSocketDialerFromCapture(loaded.Capture); err != nil {
			return replay.CaptureInspection{}, err
		}
		kind, facts.RealtimeWebSocketReplayable = replay.CaptureKindRealtime, true
	}
	return replay.CaptureInspection{SourcePath: path, CapturePath: path, Kind: kind, Provider: loaded.Capture.Provider.Name, Model: loaded.Capture.Provider.Model, Facts: facts}, nil
}
