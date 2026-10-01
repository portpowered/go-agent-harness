package wire

import (
	"path/filepath"
	"runtime"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

// roomAudioFixturePath resolves a committed room-audio replay bundle. The
// fixtures and their generator are owned by the room replay service, which
// admits them as plans; room evidence owns their audio projection.
func roomAudioFixturePath(name string) string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "roomreplay", "wire", "testdata", "room-audio", name)
}

// loadGoldenRoomAudio admits a committed bundle and projects its audio through
// the public room evidence contract, exactly as a room replay host does.
func loadGoldenRoomAudio(bundle string) (roomevidence.Bundle, error) {
	service := newTestService()
	plan, err := service.LoadPlan(bundle)
	if err != nil {
		return roomevidence.Bundle{}, err
	}
	return service.Load(plan)
}

// goldenAnalysisInput detaches every resolved stream so a test can mutate one
// analysis input without touching the loaded bundle.
func goldenAnalysisInput(bundle roomevidence.Bundle) roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), bundle.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), bundle.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), bundle.Loudness...),
	}
	streams := make([]roomanalysis.PCM16TimedStream, 0, len(bundle.Participants)*3+1)
	for _, participant := range bundle.Participants {
		streams = append(streams, participant.WAV.PCM16TimedStream, participant.Sent.PCM16TimedStream, participant.Received.PCM16TimedStream)
	}
	if bundle.RoomMix.StreamID != "" {
		streams = append(streams, bundle.RoomMix.PCM16TimedStream)
	}
	for _, stream := range streams {
		stream.Samples = append([]int16(nil), stream.Samples...)
		stream.ExpectedSpeech = append(stream.ExpectedSpeech[:0:0], stream.ExpectedSpeech...)
		stream.ChunkBoundaries = append(stream.ChunkBoundaries[:0:0], stream.ChunkBoundaries...)
		input.Streams = append(input.Streams, stream)
	}
	return input
}
