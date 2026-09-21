package service

import (
	roomevidence "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

func (s *Service) Analyze(bundle roomevidence.Bundle) (roomevidence.Analysis, error) {
	result, err := roomanalysis.AnalyzePCM16Room(roomAnalysisInput(bundle), bundle.Tolerances.RoomConfig)
	return roomevidence.Analysis{Result: result}, err
}

func roomAnalysisInput(bundle roomevidence.Bundle) roomanalysis.PCM16RoomInput {
	input := roomanalysis.PCM16RoomInput{
		Overlaps: append([]roomanalysis.PCM16OverlapInterval(nil), bundle.Overlaps...),
		BargeIns: append([]roomanalysis.PCM16BargeInAnnotation(nil), bundle.BargeIns...),
		Loudness: append([]roomanalysis.PCM16LoudnessInterval(nil), bundle.Loudness...),
	}
	for _, participant := range bundle.Participants {
		input.Streams = append(input.Streams,
			cloneTimedStream(participant.WAV.PCM16TimedStream),
			cloneTimedStream(participant.Sent.PCM16TimedStream),
			cloneTimedStream(participant.Received.PCM16TimedStream),
		)
	}
	if bundle.RoomMix.StreamID != "" {
		input.Streams = append(input.Streams, cloneTimedStream(bundle.RoomMix.PCM16TimedStream))
	}
	return input
}
