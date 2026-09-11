package session

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

func patchSessionLog(path string, images []imageEvidence, tools []toolObservation, credentials []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return wrapRecordingError(transcript.ErrRecordingWrite, "read session log", path, err, credentials)
	}
	byCall := imageEvidenceByCall(images)
	var fallback *imageEvidence
	if len(images) == 1 {
		fallback = &images[0]
	}
	output := make([]byte, 0, len(data)+len(images)*256)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		entry, err := decodeSessionLogEntry(line, path, credentials)
		if err != nil {
			return err
		}
		patchSessionLogToolEvents(&entry, tools, byCall, fallback)
		encoded, err := json.Marshal(entry)
		if err != nil {
			return wrapRecordingError(transcript.ErrRecordingWrite, "encode session log", path, err, credentials)
		}
		output = append(output, encoded...)
		output = append(output, '\n')
	}
	return atomicReplace(path, output, recordingFileMode)
}

func imageEvidenceByCall(images []imageEvidence) map[string]imageEvidence {
	byCall := make(map[string]imageEvidence, len(images))
	for _, image := range images {
		byCall[image.ToolCallID()] = image
	}
	return byCall
}

func decodeSessionLogEntry(line []byte, path string, credentials []string) (sessionLogEntry, error) {
	var entry sessionLogEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return sessionLogEntry{}, wrapRecordingError(transcript.ErrRecordingWrite, "decode session log", path, err, credentials)
	}
	return entry, nil
}

func patchSessionLogToolEvents(entry *sessionLogEntry, tools []toolObservation, byCall map[string]imageEvidence, fallback *imageEvidence) {
	callCursor, resultCursor := 0, 0
	for index := range entry.ToolEvents {
		event := &entry.ToolEvents[index]
		if callCursor < len(tools) && event.Type == "tool_call" {
			patchSessionLogToolCall(event, tools[callCursor].call)
			callCursor++
		}
		if resultCursor < len(tools) && event.Type == sessionLogToolResultType {
			patchSessionLogToolResult(event, tools[resultCursor])
			resultCursor++
		}
		patchSessionLogToolImage(event, byCall, fallback)
	}
}

func patchSessionLogToolCall(event *sessionLogTool, call messages.ToolCall) {
	event.ToolCallID = firstNonEmpty(event.ToolCallID, call.ID)
	event.ToolName = firstNonEmpty(event.ToolName, call.Name)
	event.Arguments = firstNonEmpty(event.Arguments, call.Arguments)
}

func patchSessionLogToolResult(event *sessionLogTool, observation toolObservation) {
	event.ToolCallID = firstNonEmpty(event.ToolCallID, observation.call.ID)
	event.ToolName = firstNonEmpty(event.ToolName, observation.call.Name)
	if observation.failed {
		event.Status = "failed"
	} else if event.Status == "" {
		event.Status = "completed"
	}
	if event.Content == "" && observation.result.Content != "" {
		event.Content = observation.result.Content
	}
}

func patchSessionLogToolImage(event *sessionLogTool, byCall map[string]imageEvidence, fallback *imageEvidence) {
	image, ok := byCall[event.ToolCallID]
	if !ok && fallback != nil && event.Type == sessionLogToolResultType {
		image, ok = *fallback, true
	}
	if ok && event.Type == sessionLogToolResultType {
		copy := image
		event.Image = &copy
	}
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (e imageEvidence) ToolCallID() string { return e.callID }
