package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// audioTurn is the small compatibility projection needed because the live
// evidence service deliberately stores one bounded PCM stream per direction,
// while the historical CLI bundle emitted one immutable artifact per audio
// delta. The raw transcript remains authoritative; this projection only adds
// the exact segment names and byte totals to session-log.jsonl.
type audioTurn struct {
	inputBytes     uint64
	outputBytes    uint64
	inputOffset    uint64
	outputOffset   uint64
	inputSegments  []string
	outputSegments []string
	committed      bool
	complete       bool
	toolMessage    bool
}

type audioProjection struct {
	current       audioTurn
	closed        []audioTurn
	inputBytes    uint64
	outputBytes   uint64
	inputOrdinal  int
	outputOrdinal int
}

func (p *audioProjection) observe(message messages.StreamMessage, direction recordingDirection) {
	if p == nil {
		return
	}
	p.observeAudioDelta(message, direction)
	if direction == recordingDirectionClient {
		p.observeClientBoundary(message)
		return
	}
	p.observeAgentBoundary(message)
}

func (p *audioProjection) observeAudioDelta(message messages.StreamMessage, direction recordingDirection) {
	audio, ok := message.Value.(*messages.AudioDeltaValue)
	if !ok || audio == nil || len(audio.Content) == 0 {
		return
	}
	contentBytes := uint64(len(audio.Content))
	if direction == recordingDirectionClient {
		if p.current.inputBytes == 0 {
			p.current.inputOffset = p.inputBytes
		}
		p.current.inputBytes += contentBytes
		p.inputBytes += contentBytes
		p.current.inputSegments = append(p.current.inputSegments, filepath.ToSlash(filepath.Join("audio", formatAudioName("in", p.inputOrdinal))))
		p.inputOrdinal++
		return
	}
	if p.current.outputBytes == 0 {
		p.current.outputOffset = p.outputBytes
	}
	p.current.outputBytes += contentBytes
	p.outputBytes += contentBytes
	p.current.outputSegments = append(p.current.outputSegments, filepath.ToSlash(filepath.Join("audio", formatAudioName("out", p.outputOrdinal))))
	p.outputOrdinal++
}

func (p *audioProjection) observeClientBoundary(message messages.StreamMessage) {
	if message.Type == messages.StreamTypeMessageEnd {
		p.current.committed = true
	}
}

func (p *audioProjection) observeAgentBoundary(message messages.StreamMessage) {
	if message.Type == messages.StreamTypeToolCallStart || message.Type == messages.StreamTypeToolCallDelta || message.Type == messages.StreamTypeToolCallEnd {
		p.current.toolMessage = true
		return
	}
	if message.Type != messages.StreamTypeMessageEnd {
		return
	}
	if p.current.toolMessage {
		p.current.toolMessage = false
		return
	}
	p.current.complete = true
	p.closeCurrent()
}

func (p *audioProjection) closeCurrent() {
	if p == nil || (p.current.inputBytes == 0 && p.current.outputBytes == 0) {
		return
	}
	turn := p.current
	turn.inputSegments = append([]string(nil), turn.inputSegments...)
	turn.outputSegments = append([]string(nil), turn.outputSegments...)
	p.closed = append(p.closed, turn)
	p.current = audioTurn{}
}

func (p *audioProjection) snapshot() []audioTurn {
	if p == nil {
		return nil
	}
	copyOf := make([]audioTurn, 0, len(p.closed)+1)
	for _, turn := range p.closed {
		copyOf = append(copyOf, cloneAudioTurn(turn))
	}
	if p.current.inputBytes > 0 || p.current.outputBytes > 0 {
		copyOf = append(copyOf, cloneAudioTurn(p.current))
	}
	return copyOf
}

func cloneAudioTurn(turn audioTurn) audioTurn {
	turn.inputSegments = append([]string(nil), turn.inputSegments...)
	turn.outputSegments = append([]string(nil), turn.outputSegments...)
	return turn
}

type recordingDirection uint8

const (
	recordingDirectionClient recordingDirection = iota + 1
	recordingDirectionAgent
)

func formatAudioName(direction string, index int) string {
	return fmt.Sprintf("%s-%03d.pcm", direction, index)
}

func patchSessionLogAudio(path string, turns []audioTurn, credentials []string) error {
	if len(turns) == 0 {
		return nil
	}
	data, entries, err := readAudioSessionLog(path, len(turns), credentials)
	if err != nil {
		return err
	}
	for index, turn := range turns {
		applyAudioTurn(&entries[index], index, turn)
	}
	return writeAudioSessionLog(path, data, entries, len(turns), credentials)
}

func readAudioSessionLog(path string, turnCount int, credentials []string) ([]byte, []sessionLogEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, wrapRecordingError(transcript.ErrRecordingWrite, "read session log", path, err, credentials)
	}
	entries := make([]sessionLogEntry, 0, turnCount)
	if err == nil {
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var entry sessionLogEntry
			if decodeErr := json.Unmarshal(line, &entry); decodeErr != nil {
				return nil, nil, wrapRecordingError(transcript.ErrRecordingWrite, "decode session log", path, decodeErr, credentials)
			}
			entries = append(entries, entry)
		}
	}
	for len(entries) < turnCount {
		entries = append(entries, sessionLogEntry{TurnIndex: len(entries) + 1})
	}
	return data, entries, nil
}

func applyAudioTurn(entry *sessionLogEntry, index int, turn audioTurn) {
	if entry.TurnIndex == 0 {
		entry.TurnIndex = index + 1
	}
	if turn.inputBytes > 0 {
		entry.Input.AudioOffsetBytes = turn.inputOffset
		entry.Input.AudioBytes = turn.inputBytes
		entry.Input.AudioSegments = append([]string(nil), turn.inputSegments...)
	}
	if turn.outputBytes > 0 {
		entry.Response.AudioOffsetBytes = turn.outputOffset
		entry.Response.AudioBytes = turn.outputBytes
		entry.Response.AudioSegments = append([]string(nil), turn.outputSegments...)
	}
	entry.Input.Committed = entry.Input.Committed || turn.committed
	entry.Response.Complete = entry.Response.Complete || turn.complete
}

func writeAudioSessionLog(path string, data []byte, entries []sessionLogEntry, turnCount int, credentials []string) error {
	output := make([]byte, 0, len(data)+turnCount*128)
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "encode session log", path, err, credentials)
		}
		output = append(output, encoded...)
		output = append(output, '\n')
	}
	if err := atomicReplace(path, output, recordingFileMode); err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "write session log", path, err, credentials)
	}
	return nil
}
