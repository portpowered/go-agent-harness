package agentruntime

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"encoding/json"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type roomReplayAudioStreamMetadata struct {
	StreamID        string
	TimelineStart   time.Duration
	TimelineEnd     time.Duration
	HasStart        bool
	HasEnd          bool
	Duration        time.Duration
	HasDuration     bool
	ChunkBoundaries []audio.ChunkBoundary
	ExpectedSpeech  []audio.SpeechAnnotation
}

func loadRoomReplayAudioParticipant(plan RoomReplayPlan, participant RoomReplayParticipant, object roomReplayJSONObject) (RoomReplayAudioParticipant, error) {
	wavArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleWAV)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.wav", "", "validated WAV artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	deltaArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleDeltas)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.deltas", "", "validated delta artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	sentArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleSentPCM)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.sent_pcm", "", "validated sent artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	receivedArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleReceivedPCM)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.received_pcm", "", "validated received artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	eventsArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleEvents)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.events", "", "validated events artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	diagnosticsArtifact, ok := roomReplayParticipantArtifact(participant, roomReplayArtifactRoleDiagnostics)
	if !ok {
		return RoomReplayAudioParticipant{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts.diagnostics", "", "validated diagnostics artifact", "missing", ErrRoomReplayBundleIncomplete)
	}

	events, err := loadRoomReplayJSONL(eventsArtifact, "participants["+participant.ID+"].events")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	diagnostics, err := loadRoomReplayJSONL(diagnosticsArtifact, "participants["+participant.ID+"].diagnostics")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}

	metadata := make(map[string]roomReplayAudioStreamMetadata)
	for _, role := range []string{"wav", "sent", "received"} {
		metadata[role] = parseRoomReplayStreamMetadata(object, role)
	}
	mergeRoomReplaySidecarMetadata(metadata, events)
	mergeRoomReplaySidecarMetadata(metadata, diagnostics)

	defaultWAVID := participant.ID + ":output"
	wavMetadata := metadata["wav"]
	if wavMetadata.StreamID == "" {
		wavMetadata.StreamID = defaultWAVID
	}
	defaultSentID := participant.ID + ":sent"
	sentMetadata := metadata["sent"]
	if sentMetadata.StreamID == "" {
		sentMetadata.StreamID = defaultSentID
	}
	defaultReceivedID := participant.ID + ":received"
	receivedMetadata := metadata["received"]
	if receivedMetadata.StreamID == "" {
		receivedMetadata.StreamID = defaultReceivedID
	}

	deltas, err := loadRoomReplayAudioDeltas(deltaArtifact, participant.ID, wavMetadata.StreamID, plan)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	wav, err := loadRoomReplayWAVStream(plan, wavArtifact, wavMetadata.StreamID, participant.ID, "wav")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	wav.Role = "wav"
	wav.DeltaArtifact = deltaArtifact
	wav.Deltas = deltas
	for index, delta := range deltas {
		endSample := 0
		for previous := 0; previous <= index; previous++ {
			endSample += len(deltas[previous].PCM) / 2
		}
		wav.ChunkBoundaries = append(wav.ChunkBoundaries, audio.ChunkBoundary{ID: delta.ID, SampleIndex: endSample})
	}
	if !wavMetadata.HasStart {
		for _, delta := range deltas {
			if delta.HasOffset {
				wavMetadata.TimelineStart = delta.Offset
				wavMetadata.HasStart = true
				break
			}
		}
	}
	if !wavMetadata.HasEnd {
		for index := len(deltas) - 1; index >= 0; index-- {
			if deltas[index].HasOffset {
				wavMetadata.TimelineEnd = deltas[index].Offset + roomReplaySampleDuration(len(deltas[index].PCM)/2, plan.PCMFormat.SampleRate)
				wavMetadata.HasEnd = true
				break
			}
		}
	}
	if !wavMetadata.HasEnd {
		wavMetadata.TimelineEnd = wavMetadata.TimelineStart + roomReplaySampleDuration(len(wav.Samples), plan.PCMFormat.SampleRate)
		wavMetadata.HasEnd = true
	}
	applyRoomReplayStreamMetadata(&wav, wavMetadata)
	if len(wav.ChunkBoundaries) == 0 {
		for index, delta := range deltas {
			endSample := 0
			for previous := 0; previous <= index; previous++ {
				endSample += len(deltas[previous].PCM) / 2
			}
			wav.ChunkBoundaries = append(wav.ChunkBoundaries, audio.ChunkBoundary{ID: delta.ID, SampleIndex: endSample})
		}
	}
	if err := validateRoomReplayDeltaStream(wav, plan, participant.ID); err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	if err := reconstructRoomReplayDeltaStream(wav, participant.ID); err != nil {
		return RoomReplayAudioParticipant{}, err
	}

	sent, err := loadRoomReplayPCMStream(plan, sentArtifact, sentMetadata.StreamID, participant.ID, "sent")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	applyRoomReplayStreamMetadata(&sent, sentMetadata)
	if err := validateRoomReplayAudioStreamTimeline(sent, plan, "participants["+participant.ID+"].sent"); err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	received, err := loadRoomReplayPCMStream(plan, receivedArtifact, receivedMetadata.StreamID, participant.ID, "received")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	applyRoomReplayStreamMetadata(&received, receivedMetadata)
	if err := validateRoomReplayAudioStreamTimeline(received, plan, "participants["+participant.ID+"].received"); err != nil {
		return RoomReplayAudioParticipant{}, err
	}

	return RoomReplayAudioParticipant{ID: participant.ID, WAV: wav, Sent: sent, Received: received, Events: events, Diagnostics: diagnostics}, nil
}

func roomReplayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func loadRoomReplayJSONL(artifact RoomReplayArtifact, field string) ([]json.RawMessage, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, artifact.Path, "readable JSONL", err.Error(), err)
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
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "readable WAV", err.Error(), err)
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
		PCM16TimedStream: audio.PCM16TimedStream{PCM16Input: audio.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: wav.SampleRate, Samples: samples}},
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
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "readable PCM", err.Error(), err)
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
		PCM16TimedStream: audio.PCM16TimedStream{PCM16Input: audio.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: rate, Samples: samples}},
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

func validateRoomReplayDeltaStream(stream RoomReplayAudioStream, plan RoomReplayPlan, participantID string) error {
	if len(stream.Deltas) == 0 {
		return &RoomReplayDeltaReconstructionError{ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: "missing", DeltaIndex: 0, ByteOffset: 0, ExpectedLength: len(stream.PCM), ActualLength: 0, ExpectedSampleCount: stream.SampleCount, ActualSampleCount: 0, Cause: ErrRoomReplayDeltaReconstruction}
	}
	previousOffset := time.Duration(-1)
	for _, delta := range stream.Deltas {
		if delta.HasOffset {
			if delta.Offset < 0 || delta.Offset > plan.EndedAt.Sub(plan.ClockBase) {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "offset within declared room duration", delta.Offset.String())
			}
			if previousOffset >= 0 && delta.Offset < previousOffset {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "monotonic delta timestamps", delta.Offset.String())
			}
			previousOffset = delta.Offset
		}
	}
	return validateRoomReplayAudioStreamTimeline(stream, plan, "participants["+participantID+"].wav")
}

// RoomReplayDeltaReconstructionError points to the first divergent byte,
// including the delta line responsible for that byte. A missing or extra
// suffix uses ByteOffset at the end of the common prefix.
type RoomReplayDeltaReconstructionError struct {
	ParticipantID       string
	StreamID            string
	DeltaID             string
	DeltaIndex          int
	ByteOffset          int
	ExpectedByte        int
	ActualByte          int
	ExpectedLength      int
	ActualLength        int
	ExpectedSampleCount int
	ActualSampleCount   int
	Cause               error
}

func (e *RoomReplayDeltaReconstructionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	expectedByte := "<missing>"
	if e.ExpectedByte >= 0 {
		expectedByte = fmt.Sprintf("0x%02x", e.ExpectedByte)
	}
	actualByte := "<missing>"
	if e.ActualByte >= 0 {
		actualByte = fmt.Sprintf("0x%02x", e.ActualByte)
	}
	return fmt.Sprintf("%s: participant %q stream %q delta %q (index %d) first divergent byte %d: expected %s, actual %s; expected %d bytes/%d samples, reconstructed %d bytes/%d samples", ErrRoomReplayDeltaReconstruction, e.ParticipantID, e.StreamID, e.DeltaID, e.DeltaIndex, e.ByteOffset, expectedByte, actualByte, e.ExpectedLength, e.ExpectedSampleCount, e.ActualLength, e.ActualSampleCount)
}

func (e *RoomReplayDeltaReconstructionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(ErrRoomReplayDeltaReconstruction, ErrInvalidRoomReplayBundle, e.Cause)
}

func reconstructRoomReplayDeltaStream(stream RoomReplayAudioStream, participantID string) error {
	position := 0
	actualLength := 0
	for index, delta := range stream.Deltas {
		if position < len(stream.PCM) {
			shared := len(delta.PCM)
			if remaining := len(stream.PCM) - position; shared > remaining {
				shared = remaining
			}
			for offset := 0; offset < shared; offset++ {
				if delta.PCM[offset] != stream.PCM[position+offset] {
					return &RoomReplayDeltaReconstructionError{
						ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: position + offset,
						ExpectedByte: int(stream.PCM[position+offset]), ActualByte: int(delta.PCM[offset]),
						ExpectedLength: len(stream.PCM), ActualLength: actualLength + len(delta.PCM),
						ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (actualLength + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
					}
				}
			}
		}
		if position+len(delta.PCM) > len(stream.PCM) {
			return &RoomReplayDeltaReconstructionError{
				ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: len(stream.PCM),
				ExpectedByte: -1, ActualByte: int(delta.PCM[len(stream.PCM)-position]), ExpectedLength: len(stream.PCM), ActualLength: actualLength + len(delta.PCM),
				ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (actualLength + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
			}
		}
		position += len(delta.PCM)
		actualLength = position
	}
	if actualLength != len(stream.PCM) {
		deltaID := "missing"
		if len(stream.Deltas) > 0 {
			deltaID = "after-" + stream.Deltas[len(stream.Deltas)-1].ID
		}
		return &RoomReplayDeltaReconstructionError{
			ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: deltaID, DeltaIndex: len(stream.Deltas), ByteOffset: actualLength,
			ExpectedByte: int(stream.PCM[actualLength]), ActualByte: -1, ExpectedLength: len(stream.PCM), ActualLength: actualLength,
			ExpectedSampleCount: stream.SampleCount, ActualSampleCount: actualLength / 2, Cause: ErrRoomReplayDeltaReconstruction,
		}
	}
	return nil
}

func loadRoomReplayAudioDeltas(artifact RoomReplayArtifact, participantID, streamID string, plan RoomReplayPlan) ([]RoomReplayAudioDelta, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return nil, roomReplayAudioIncomplete("participants["+participantID+"].deltas", artifact.Path, "readable delta JSONL", err.Error(), err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	deltas := make([]RoomReplayAudioDelta, 0)
	seenDeltaIDs := make(map[string]int)
	var previousOffset time.Duration
	hasPreviousOffset := false
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		object, err := roomReplayObject(line)
		if err != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d]", participantID, lineNumber), artifact.Path, "JSON object", "invalid", err)
		}
		kind, _, kindErr := firstRoomReplayStringField(object, nil, "type", "event_type", "kind")
		if kindErr != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].type", participantID, lineNumber), artifact.Path, "string event type", "invalid", kindErr)
		}
		payload, found, payloadErr := roomReplayAudioPayload(object, kind)
		if payloadErr != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d]", participantID, lineNumber), artifact.Path, "base64 PCM16 audio payload", "invalid", payloadErr)
		}
		if !found {
			if isRoomReplayAudioDeltaKind(kind) {
				return nil, roomReplayAudioIncomplete(fmt.Sprintf("participants[%s].deltas.line[%d].content", participantID, lineNumber), artifact.Path, "audio payload", "missing", ErrRoomReplayBundleIncomplete)
			}
			continue
		}
		if len(payload) == 0 {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].content", participantID, lineNumber), artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), nil)
		}
		if err := codec.ValidatePCM16(payload, codec.MaxPCM16Bytes); err != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].content", participantID, lineNumber), artifact.Path, "non-empty even PCM16 byte payload", strconv.Itoa(len(payload)), err)
		}

		sequence, _, hasSequence, sequenceErr := roomReplayFirstIntField(object, "sequence", "delta_index", "chunk_index", "index", "global_index", "actor_provided_index")
		if sequenceErr != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].sequence", participantID, lineNumber), artifact.Path, "integer sequence", "invalid", sequenceErr)
		}
		if hasSequence {
			if sequence < 0 {
				return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].sequence", participantID, lineNumber), artifact.Path, "non-negative sequence", strconv.FormatInt(sequence, 10), nil)
			}
		}

		offset, hasOffset, offsetErr := roomReplayAudioOffset(object)
		if offsetErr != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, lineNumber), artifact.Path, "non-negative millisecond offset", "invalid", offsetErr)
		}
		if hasOffset {
			if offset < 0 || offset > plan.EndedAt.Sub(plan.ClockBase) {
				return nil, roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, lineNumber), artifact.Path, "offset within declared room duration", offset.String())
			}
			if hasPreviousOffset && offset < previousOffset {
				return nil, roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, lineNumber), artifact.Path, "monotonic delta timestamps", offset.String())
			}
			previousOffset, hasPreviousOffset = offset, true
		}
		declaredStreamID, streamPresent, streamErr := firstRoomReplayStringField(object, nil, "stream_id", "audio_stream_id", "stream_identity")
		if streamErr != nil && streamPresent {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].stream_id", participantID, lineNumber), artifact.Path, "string stream identity", "invalid", streamErr)
		}
		if streamPresent {
			declaredStreamID = strings.TrimSpace(declaredStreamID)
			if declaredStreamID == "" || declaredStreamID != streamID {
				return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].stream_id", participantID, lineNumber), artifact.Path, streamID, declaredStreamID, nil)
			}
		}
		declaredParticipantID, participantPresent, participantErr := firstRoomReplayStringField(object, nil, "participant_id")
		if participantErr != nil && participantPresent {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].participant_id", participantID, lineNumber), artifact.Path, "string participant identity", "invalid", participantErr)
		}
		if participantPresent && strings.TrimSpace(declaredParticipantID) != participantID {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].participant_id", participantID, lineNumber), artifact.Path, participantID, strings.TrimSpace(declaredParticipantID), nil)
		}
		id, _, idErr := firstRoomReplayStringField(object, nil, "delta_id", "chunk_id", "id")
		if idErr != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].id", participantID, lineNumber), artifact.Path, "string delta identity", "invalid", idErr)
		}
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("delta-%d", len(deltas))
		}
		id = strings.TrimSpace(id)
		if previousLine, exists := seenDeltaIDs[id]; exists {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("participants[%s].deltas.line[%d].id", participantID, lineNumber), artifact.Path, fmt.Sprintf("unique delta identity (first seen on line %d)", previousLine), id, nil)
		}
		seenDeltaIDs[id] = lineNumber
		turnID, _, _ := firstRoomReplayStringField(object, nil, "turn_id", "turn", "response_id")
		deltas = append(deltas, RoomReplayAudioDelta{ID: id, Sequence: sequence, HasSequence: hasSequence, Offset: offset, HasOffset: hasOffset, TurnID: strings.TrimSpace(turnID), LineNumber: lineNumber, PCM: append([]byte(nil), payload...)})
	}
	if err := scanner.Err(); err != nil {
		return nil, roomReplayAudioMismatch("participants["+participantID+"].deltas", artifact.Path, "readable delta JSONL", err.Error(), err)
	}
	if len(deltas) == 0 {
		return nil, roomReplayAudioIncomplete("participants["+participantID+"].deltas", artifact.Path, "at least one audio delta", "none", ErrRoomReplayBundleIncomplete)
	}
	return deltas, nil
}

func roomReplayAudioPayload(object roomReplayJSONObject, kind string) ([]byte, bool, error) {
	audioKind := isRoomReplayAudioDeltaKind(kind)
	for _, key := range []string{"pcm_base64", "audio_base64", "delta_base64", "pcm", "audio"} {
		if raw, ok := object[key]; ok {
			payload, found, err := decodeRoomReplayAudioRaw(raw)
			if found || err != nil {
				return payload, true, err
			}
		}
	}
	if audioKind || strings.TrimSpace(kind) == "" {
		for _, key := range []string{"delta", "data"} {
			if raw, ok := object[key]; ok {
				payload, found, err := decodeRoomReplayAudioRaw(raw)
				if found || err != nil {
					return payload, true, err
				}
			}
		}
	}
	if audioKind {
		if raw, ok := object["content"]; ok {
			payload, found, err := decodeRoomReplayAudioRaw(raw)
			return payload, found, err
		}
	}
	for _, key := range []string{"value", "payload", "audio_delta"} {
		if raw, ok := object[key]; ok {
			nested, err := roomReplayObject(raw)
			if err != nil {
				continue
			}
			nestedKind := kind
			if value, present, _ := firstRoomReplayStringField(nested, nil, "type", "event_type", "kind"); present {
				nestedKind = value
			}
			payload, found, payloadErr := roomReplayAudioPayload(nested, nestedKind)
			if found || payloadErr != nil {
				return payload, found, payloadErr
			}
		}
	}
	return nil, false, nil
}

func decodeRoomReplayAudioRaw(raw json.RawMessage) ([]byte, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		if encoded == "" {
			return []byte{}, true, nil
		}
		decoded, err := codec.DecodeLegacyBase64(encoded)
		if err != nil {
			return nil, true, err
		}
		return decoded, true, nil
	}
	var numbers []int
	if json.Unmarshal(raw, &numbers) == nil {
		payload := make([]byte, len(numbers))
		for index, value := range numbers {
			if value < 0 || value > 255 {
				return nil, true, fmt.Errorf("audio byte %d is outside 0..255", value)
			}
			payload[index] = byte(value)
		}
		return payload, true, nil
	}
	if object, err := roomReplayObject(raw); err == nil {
		for _, key := range []string{"base64", "data", "content", "pcm", "delta"} {
			if nested, ok := object[key]; ok {
				return decodeRoomReplayAudioRaw(nested)
			}
		}
	}
	return nil, true, errors.New("audio payload must be base64 string or byte array")
}

func isRoomReplayAudioDeltaKind(kind string) bool {
	normalized := strings.ToLower(strings.NewReplacer(".", "", "_", "", "-", "", " ", "").Replace(strings.TrimSpace(kind)))
	return strings.Contains(normalized, "audio") && (strings.Contains(normalized, "delta") || strings.Contains(normalized, "chunk")) || normalized == "deltaaudio" || normalized == "pcmdelta"
}

func roomReplayFirstIntField(object roomReplayJSONObject, names ...string) (int64, string, bool, error) {
	for _, name := range names {
		value, present, err := roomReplayInt64Field(object, name)
		if present {
			return value, name, true, err
		}
	}
	return 0, "", false, nil
}

func roomReplayInt64Field(object roomReplayJSONObject, name string) (int64, bool, error) {
	raw, ok := object[name]
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, true, err
}

func roomReplayAudioOffset(object roomReplayJSONObject) (time.Duration, bool, error) {
	for _, name := range []string{"monotonic_offset_ms", "offset_ms", "timestamp_ms", "offset"} {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, true)
			return value, true, err
		}
	}
	return 0, false, nil
}

func parseRoomReplayStreamMetadata(object roomReplayJSONObject, role string) roomReplayAudioStreamMetadata {
	metadata := roomReplayAudioStreamMetadata{}
	if object == nil {
		return metadata
	}
	var candidates []json.RawMessage
	for _, key := range []string{"streams", "audio_streams", "stream_metadata"} {
		if raw, ok := object[key]; ok {
			if nested, err := roomReplayObject(raw); err == nil {
				for _, alias := range roomReplayStreamRoleAliases(role) {
					if value, exists := nested[alias]; exists {
						candidates = append(candidates, value)
					}
				}
			}
		}
	}
	for _, key := range roomReplayStreamRoleAliases(role) {
		if raw, ok := object[key]; ok {
			candidates = append(candidates, raw)
		}
	}
	if raw, ok := object["artifacts"]; ok {
		if nested, err := roomReplayObject(raw); err == nil {
			for _, key := range roomReplayStreamRoleAliases(role) {
				if value, exists := nested[key]; exists {
					candidates = append(candidates, value)
				}
			}
		}
	}
	for _, candidate := range candidates {
		parsed := parseRoomReplayStreamMetadataObject(candidate)
		mergeRoomReplayAudioStreamMetadata(&metadata, parsed)
	}
	return metadata
}

func roomReplayStreamRoleAliases(role string) []string {
	switch role {
	case "wav":
		return []string{"wav", "output", "audio", "output_stream", "wav_stream"}
	case "sent":
		return []string{"sent", "sent_pcm", "sent_stream", "uplink"}
	case "received":
		return []string{"received", "received_pcm", "received_stream", "downlink"}
	default:
		return []string{role}
	}
}

func parseRoomReplayStreamMetadataObject(raw json.RawMessage) roomReplayAudioStreamMetadata {
	metadata := roomReplayAudioStreamMetadata{}
	object, err := roomReplayObject(raw)
	if err != nil {
		return metadata
	}
	metadata.StreamID, _, _ = firstRoomReplayStringField(object, nil, "stream_id", "id", "identity")
	if value, present, err := roomReplayFirstDurationField(object, "timeline_start_ms", "start_ms", "start_offset_ms", "timeline_start", "start"); err == nil && present {
		metadata.TimelineStart, metadata.HasStart = value, true
	}
	if value, present, err := roomReplayFirstDurationField(object, "timeline_end_ms", "end_ms", "timeline_end", "end"); err == nil && present {
		metadata.TimelineEnd, metadata.HasEnd = value, true
	}
	if value, present, err := roomReplayFirstDurationField(object, "duration_ms", "duration"); err == nil && present {
		metadata.Duration, metadata.HasDuration = value, true
	}
	if raw, ok := roomReplayFirstRawField(object, "chunk_boundaries", "boundaries", "chunks"); ok {
		metadata.ChunkBoundaries = parseRoomReplayChunkBoundaries(raw)
	}
	if raw, ok := roomReplayFirstRawField(object, "expected_speech", "speech", "speech_regions"); ok {
		metadata.ExpectedSpeech = parseRoomReplaySpeechAnnotations(raw)
	}
	return metadata
}

func mergeRoomReplayAudioStreamMetadata(destination *roomReplayAudioStreamMetadata, source roomReplayAudioStreamMetadata) {
	if destination == nil {
		return
	}
	if destination.StreamID == "" {
		destination.StreamID = source.StreamID
	}
	if !destination.HasStart && source.HasStart {
		destination.TimelineStart, destination.HasStart = source.TimelineStart, true
	}
	if !destination.HasEnd && source.HasEnd {
		destination.TimelineEnd, destination.HasEnd = source.TimelineEnd, true
	}
	if !destination.HasDuration && source.HasDuration {
		destination.Duration, destination.HasDuration = source.Duration, true
	}
	if len(destination.ChunkBoundaries) == 0 {
		destination.ChunkBoundaries = append([]audio.ChunkBoundary(nil), source.ChunkBoundaries...)
	}
	if len(destination.ExpectedSpeech) == 0 {
		destination.ExpectedSpeech = append([]audio.SpeechAnnotation(nil), source.ExpectedSpeech...)
	}
}

func mergeRoomReplaySidecarMetadata(metadata map[string]roomReplayAudioStreamMetadata, lines []json.RawMessage) {
	for _, line := range lines {
		object, err := roomReplayObject(line)
		if err != nil {
			continue
		}
		role, _, _ := firstRoomReplayStringField(object, nil, "stream_role", "audio_role", "role")
		role = normalizeRoomReplayAudioRole(role)
		if role == "" {
			continue
		}
		parsed := parseRoomReplayStreamMetadataObject(line)
		if parsed.StreamID == "" {
			continue
		}
		current := metadata[role]
		mergeRoomReplayAudioStreamMetadata(&current, parsed)
		metadata[role] = current
	}
}

func normalizeRoomReplayAudioRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "output", "wav", "audio", "output_stream", "wav_stream":
		return "wav"
	case "sent", "sent_pcm", "sent_stream", "uplink":
		return "sent"
	case "received", "received_pcm", "received_stream", "downlink":
		return "received"
	default:
		return ""
	}
}

func roomReplayFirstRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayFirstDurationField(object roomReplayJSONObject, names ...string) (time.Duration, bool, error) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, strings.HasSuffix(name, "_ms") || name == "offset")
			return value, true, err
		}
	}
	return 0, false, nil
}

func roomReplayDurationValue(raw json.RawMessage, numericIsMilliseconds bool) (time.Duration, error) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0, errors.New("duration is empty")
		}
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed, nil
		}
		if numeric, err := strconv.ParseInt(value, 10, 64); err == nil && numericIsMilliseconds {
			return time.Duration(numeric) * time.Millisecond, nil
		}
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, err
	}
	if numericIsMilliseconds {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(value) * time.Millisecond, nil
	}
	return 0, fmt.Errorf("duration must be a string")
}

func parseRoomReplayChunkBoundaries(raw json.RawMessage) []audio.ChunkBoundary {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	boundaries := make([]audio.ChunkBoundary, 0, len(values))
	for index, value := range values {
		object, err := roomReplayObject(value)
		if err != nil {
			continue
		}
		position, _, _, err := roomReplayFirstIntField(object, "sample_index", "end_sample", "offset_samples", "sample")
		if err != nil {
			continue
		}
		id, _, _ := firstRoomReplayStringField(object, nil, "id", "chunk_id", "name")
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("chunk-%d", index)
		}
		if position > 0 && position <= int64(^uint(0)>>1) {
			boundaries = append(boundaries, audio.ChunkBoundary{ID: id, SampleIndex: int(position)})
		}
	}
	return boundaries
}

func parseRoomReplaySpeechAnnotations(raw json.RawMessage) []audio.SpeechAnnotation {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	annotations := make([]audio.SpeechAnnotation, 0, len(values))
	for _, value := range values {
		object, err := roomReplayObject(value)
		if err != nil {
			continue
		}
		start, startPresent, startErr := roomReplayFirstDurationField(object, "start_ms", "start_offset_ms", "start")
		end, endPresent, endErr := roomReplayFirstDurationField(object, "end_ms", "end_offset_ms", "end")
		if startErr != nil || endErr != nil || !startPresent || !endPresent || end <= start {
			continue
		}
		label, _, _ := firstRoomReplayStringField(object, nil, "label", "id", "name")
		annotations = append(annotations, audio.SpeechAnnotation{Label: label, Start: start, End: end})
	}
	return annotations
}
