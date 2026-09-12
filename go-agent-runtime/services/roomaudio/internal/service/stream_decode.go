package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"strconv"
)

func validateRoomReplayAudioStreamTimeline(stream RoomReplayAudioStream, plan RoomReplayPlan, field string) error {
	roomDuration := plan.EndedAt.Sub(plan.ClockBase)
	if stream.StreamID == "" || stream.ParticipantID == "" {
		return roomReplayAudioMismatch(field+".identity", stream.Artifact.Path, "non-empty stream and participant identity", stream.StreamID+"/"+stream.ParticipantID, nil)
	}
	if stream.TimelineStart < 0 || stream.TimelineEnd <= stream.TimelineStart {
		return roomReplayAudioTimeline(field+".timeline", stream.Artifact.Path, "positive interval within room", fmt.Sprintf("%s..%s", stream.TimelineStart, stream.TimelineEnd))
	}
	if stream.TimelineEnd > roomDuration {
		return roomReplayAudioTimeline(field+".timeline", stream.Artifact.Path, "timeline end within declared room duration", stream.TimelineEnd.String())
	}
	sampleDuration := roomReplaySampleDuration(len(stream.Samples), stream.SampleRate)
	if stream.TimelineStart+sampleDuration > roomDuration {
		return roomReplayAudioTimeline(field+".samples", stream.Artifact.Path, "sample payload within declared room duration", (stream.TimelineStart + sampleDuration).String())
	}
	if stream.TimelineStart+sampleDuration > stream.TimelineEnd {
		return roomReplayAudioTimeline(field+".samples", stream.Artifact.Path, "sample payload within stream timeline", (stream.TimelineStart + sampleDuration).String())
	}
	previous := 0
	for index, boundary := range stream.ChunkBoundaries {
		if boundary.SampleIndex <= previous || boundary.SampleIndex > len(stream.Samples) {
			return roomReplayAudioTimeline(fmt.Sprintf("%s.chunk_boundaries[%d]", field, index), stream.Artifact.Path, fmt.Sprintf("sample index in 1..%d and increasing", len(stream.Samples)), strconv.Itoa(boundary.SampleIndex))
		}
		previous = boundary.SampleIndex
	}
	for index, annotation := range stream.ExpectedSpeech {
		if annotation.Start < stream.TimelineStart || annotation.End > stream.TimelineEnd || annotation.End <= annotation.Start {
			return roomReplayAudioTimeline(fmt.Sprintf("%s.expected_speech[%d]", field, index), stream.Artifact.Path, "annotation within stream timeline", fmt.Sprintf("%s..%s", annotation.Start, annotation.End))
		}
	}
	return nil
}

func loadRoomReplayJSONL(artifact RoomReplayArtifact, field string) ([]json.RawMessage, error) {
	data, err := readRoomReplayArtifact(artifact, maxRoomReplayArtifactBytes, field)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lines := make([]json.RawMessage, 0)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("%s.line[%d]", field, lineNumber), artifact.Path, "valid JSON object", "invalid JSON", nil)
		}
		object, err := roomReplayObject(line)
		if err != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("%s.line[%d]", field, lineNumber), artifact.Path, "JSON object", "non-object", err)
		}
		_ = object
		lines = append(lines, append(json.RawMessage(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, roomReplayAudioMismatch(field, artifact.Path, "readable JSONL", err.Error(), err)
	}
	if len(lines) == 0 {
		return nil, roomReplayAudioIncomplete(field, artifact.Path, "at least one JSONL record", "empty", ErrRoomReplayBundleIncomplete)
	}
	return lines, nil
}

func loadRoomReplayWAVStream(plan RoomReplayPlan, artifact RoomReplayArtifact, streamID, participantID, role string) (RoomReplayAudioStream, error) {
	data, err := readRoomReplayArtifact(artifact, maxRoomReplayArtifactBytes, "artifact."+role)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	wav, err := decodeRoomReplayWAV(data, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	if err := validateRoomReplayWAVFormat(wav, plan.PCMFormat, artifact.Path); err != nil {
		return RoomReplayAudioStream{}, err
	}
	if len(wav.PCM) == 0 {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "non-empty WAV PCM payload", "empty", ErrRoomReplayBundleIncomplete)
	}
	samples, err := decodeMonoPCM16(wav.PCM, wav.Channels, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	stream := RoomReplayAudioStream{
		PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: wav.SampleRate, Samples: samples}},
		Role:             role,
		PCM:              append([]byte(nil), wav.PCM...),
		SampleCount:      len(samples),
		Artifact:         artifact,
	}
	stream.TimelineStart = 0
	stream.TimelineEnd = roomReplaySampleDuration(len(samples), wav.SampleRate)
	return stream, nil
}

func loadRoomReplayPCMStream(plan RoomReplayPlan, artifact RoomReplayArtifact, streamID, participantID, role string) (RoomReplayAudioStream, error) {
	data, err := readRoomReplayArtifact(artifact, maxRoomReplayArtifactBytes, "artifact."+role)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	if len(data) == 0 {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "non-empty PCM payload", "empty", ErrRoomReplayBundleIncomplete)
	}
	pcm := data
	rate := plan.PCMFormat.SampleRate
	channels := plan.PCMFormat.Channels
	if bytes.HasPrefix(data, []byte("RIFF")) {
		wav, err := decodeRoomReplayWAV(data, artifact.Path)
		if err != nil {
			return RoomReplayAudioStream{}, err
		}
		if err := validateRoomReplayWAVFormat(wav, plan.PCMFormat, artifact.Path); err != nil {
			return RoomReplayAudioStream{}, err
		}
		pcm = wav.PCM
		rate = wav.SampleRate
		channels = wav.Channels
	}
	if len(pcm)%(2*channels) != 0 {
		return RoomReplayAudioStream{}, roomReplayAudioMismatch("artifact."+role, artifact.Path, "PCM16 frame-aligned payload", fmt.Sprintf("%d bytes", len(pcm)), nil)
	}
	samples, err := decodeMonoPCM16(pcm, channels, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	stream := RoomReplayAudioStream{
		PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: rate, Samples: samples}},
		Role:             role,
		PCM:              append([]byte(nil), pcm...),
		SampleCount:      len(samples),
		Artifact:         artifact,
	}
	stream.TimelineStart = 0
	stream.TimelineEnd = roomReplaySampleDuration(len(samples), rate)
	return stream, nil
}

func decodeMonoPCM16(data []byte, channels int, artifact string) ([]int16, error) {
	if channels != 1 {
		return nil, roomReplayAudioMismatch("pcm_format.channels", artifact, "1 channel for audio analysis", strconv.Itoa(channels), nil)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%2 != 0 {
		return nil, roomReplayAudioMismatch("pcm_format.sample_width_bits", artifact, "even PCM16 byte count", strconv.Itoa(len(data)), nil)
	}
	// A room artifact is an aggregate file, so its bound is owned by the room
	// bundle reader rather than the per-provider payload default.
	samples, err := codec.DecodePCM16WithLimit(data, len(data))
	if err != nil {
		return nil, roomReplayAudioMismatch("pcm_format.sample_width_bits", artifact, "even PCM16 byte count", strconv.Itoa(len(data)), err)
	}
	return samples, nil
}

func applyRoomReplayStreamMetadata(stream *RoomReplayAudioStream, metadata roomReplayAudioStreamMetadata) {
	if stream == nil {
		return
	}
	if metadata.StreamID != "" {
		stream.StreamID = metadata.StreamID
	}
	if metadata.HasStart {
		stream.TimelineStart = metadata.TimelineStart
	}
	if metadata.HasEnd {
		stream.TimelineEnd = metadata.TimelineEnd
	} else if metadata.HasDuration {
		stream.TimelineEnd = stream.TimelineStart + metadata.Duration
	}
	if len(metadata.ChunkBoundaries) > 0 {
		stream.ChunkBoundaries = append(stream.ChunkBoundaries[:0], metadata.ChunkBoundaries...)
	}
	if len(metadata.ExpectedSpeech) > 0 {
		stream.ExpectedSpeech = append(stream.ExpectedSpeech[:0], metadata.ExpectedSpeech...)
	}
}
