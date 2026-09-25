//go:build live

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

type liveVoiceToolGroundingObservation struct {
	Provider                 string
	Model                    string
	Instructions             string
	AdvertisedTools          []string
	SessionUpdateCount       int
	SessionUpdateIndex       int
	FirstInputIndex          int
	ToolName                 string
	ToolCallID               string
	ToolArguments            string
	FunctionCallOutput       string
	ToolCallIndex            int
	ToolArgumentsIndex       int
	FunctionOutputIndex      int
	ResponseCreatesAfterTool int
	SpokenReply              string
	SpokenReplyIndex         int
	AudioBytesAfterTool      int
	TerminalStatus           string
	TerminalIndex            int
}

type voiceGroundingCall struct {
	index, argumentsIndex   int
	name, callID, arguments string
}

type voiceGroundingOutput struct {
	index          int
	callID, output string
}

// voiceGroundingIndexed is one indexed capture fact: a transcript, an audio
// byte count, or a terminal status.
type voiceGroundingIndexed struct {
	index int
	text  string
	bytes int
}

// voiceGroundingCollector accumulates the capture facts that
// inspectLiveVoiceToolGroundingCapture correlates after the scan.
type voiceGroundingCollector struct {
	observation           liveVoiceToolGroundingObservation
	calls                 []voiceGroundingCall
	outputs               []voiceGroundingOutput
	responseCreateIndices []int
	spoken, audio, done   []voiceGroundingIndexed
}

func inspectLiveVoiceToolGroundingCapture(capture gwtesting.SessionCapture, testCase liveVoiceToolGroundingCase) (liveVoiceToolGroundingObservation, error) {
	c := &voiceGroundingCollector{observation: liveVoiceToolGroundingObservation{
		Provider:            capture.Provider.Name,
		Model:               capture.Provider.Model,
		SessionUpdateIndex:  -1,
		FirstInputIndex:     -1,
		ToolCallIndex:       -1,
		ToolArgumentsIndex:  -1,
		FunctionOutputIndex: -1,
		SpokenReplyIndex:    -1,
		TerminalIndex:       -1,
	}}
	for index, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		if len(payload) == 0 {
			return c.observation, fmt.Errorf("record %d (%s) has an empty payload", index, record.Type)
		}
		var err error
		switch record.Direction {
		case gwtesting.DirectionClientToServer:
			err = c.observeClient(index, record.Type, payload)
		case gwtesting.DirectionServerToClient:
			err = c.observeServer(index, record.Type, payload)
		}
		if err != nil {
			return c.observation, err
		}
	}
	err := c.correlate(testCase)
	return c.observation, err
}

func (c *voiceGroundingCollector) observeClient(index int, recordType string, payload []byte) error {
	switch recordType {
	case rtEventSessionUpdate:
		c.observation.SessionUpdateCount++
		var event struct {
			Session struct {
				Instructions string `json:"instructions"`
				Tools        []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"session"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode session.update: %w", err)
		}
		if c.observation.SessionUpdateIndex < 0 {
			c.observation.SessionUpdateIndex = index
		}
		c.observation.Instructions = event.Session.Instructions
		c.observation.AdvertisedTools = c.observation.AdvertisedTools[:0]
		for _, tool := range event.Session.Tools {
			c.observation.AdvertisedTools = append(c.observation.AdvertisedTools, tool.Name)
		}
	case rtEventInputAudioAppend:
		if c.observation.FirstInputIndex < 0 {
			c.observation.FirstInputIndex = index
		}
	case rtEventConversationItemCreate:
		var event struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode conversation.item.create: %w", err)
		}
		if event.Item.Type == rtItemFunctionCallOutput {
			c.outputs = append(c.outputs, voiceGroundingOutput{index: index, callID: event.Item.CallID, output: event.Item.Output})
		}
	case rtEventResponseCreate:
		c.responseCreateIndices = append(c.responseCreateIndices, index)
	}
	return nil
}

func (c *voiceGroundingCollector) observeServer(index int, recordType string, payload []byte) error {
	switch recordType {
	case rtEventOutputItemAdded:
		var event struct {
			Item struct {
				Type   string `json:"type"`
				Name   string `json:"name"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.output_item.added: %w", err)
		}
		if event.Item.Type == rtItemFunctionCall {
			c.calls = append(c.calls, voiceGroundingCall{index: index, name: event.Item.Name, callID: event.Item.CallID})
		}
	case rtEventFunctionCallArgumentsDone:
		return c.observeArgumentsDone(index, payload)
	case rtEventOutputAudioTranscriptDone:
		var event struct {
			Transcript string `json:"transcript"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.output_audio_transcript.done: %w", err)
		}
		if strings.TrimSpace(event.Transcript) != "" {
			c.spoken = append(c.spoken, voiceGroundingIndexed{index: index, text: event.Transcript})
		}
	case rtEventOutputAudioDelta, rtEventLegacyAudioDelta:
		return c.observeAudioDelta(index, payload)
	case rtEventResponseDone:
		var event struct {
			Status   string `json:"status"`
			Response struct {
				Status string `json:"status"`
			} `json:"response"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode response.done: %w", err)
		}
		status := event.Response.Status
		if status == "" {
			status = event.Status
		}
		c.done = append(c.done, voiceGroundingIndexed{index: index, text: status})
	case rtEventError:
		return fmt.Errorf("provider emitted an error event")
	}
	return nil
}

func (c *voiceGroundingCollector) observeArgumentsDone(index int, payload []byte) error {
	var event struct {
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode response.function_call_arguments.done: %w", err)
	}
	if len(c.calls) == 0 {
		return fmt.Errorf("function-call arguments arrived before function call")
	}
	call := &c.calls[len(c.calls)-1]
	call.argumentsIndex = index
	call.arguments = event.Arguments
	if call.callID == "" {
		call.callID = event.CallID
	}
	if call.name == "" {
		call.name = event.Name
	}
	return nil
}

func (c *voiceGroundingCollector) observeAudioDelta(index int, payload []byte) error {
	var event struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("decode output audio delta: %w", err)
	}
	if event.Delta == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Delta)
	if err != nil {
		return fmt.Errorf("decode output audio delta: %w", err)
	}
	c.audio = append(c.audio, voiceGroundingIndexed{index: index, bytes: len(decoded)})
	return nil
}

// correlate checks the single call/result pair and derives the post-tool
// spoken reply, terminal status, and audio byte count.
func (c *voiceGroundingCollector) correlate(testCase liveVoiceToolGroundingCase) error {
	observation := &c.observation
	if observation.SessionUpdateCount != 1 {
		return fmt.Errorf("session.update count=%d, want exactly one", observation.SessionUpdateCount)
	}
	if observation.SessionUpdateIndex < 0 || observation.FirstInputIndex < 0 || observation.SessionUpdateIndex >= observation.FirstInputIndex {
		return fmt.Errorf("session.update index=%d must precede first input index=%d", observation.SessionUpdateIndex, observation.FirstInputIndex)
	}
	if len(c.calls) != 1 {
		return fmt.Errorf("function_call count=%d, want exactly one %s", len(c.calls), testCase.ExpectedTool)
	}
	if len(c.outputs) != 1 {
		return fmt.Errorf("function_call_output count=%d, want exactly one", len(c.outputs))
	}
	call, output := c.calls[0], c.outputs[0]
	observation.ToolName = call.name
	observation.ToolCallID = call.callID
	observation.ToolArguments = call.arguments
	observation.FunctionCallOutput = output.output
	observation.ToolCallIndex = call.index
	observation.ToolArgumentsIndex = call.argumentsIndex
	observation.FunctionOutputIndex = output.index
	if strings.TrimSpace(call.callID) == "" || output.callID != call.callID {
		return fmt.Errorf("function-call correlation invalid: call=(%q,%q), output_call_id=%q", call.name, call.callID, output.callID)
	}
	if call.argumentsIndex <= call.index || output.index <= call.argumentsIndex {
		return fmt.Errorf("call/result order invalid: call=%d arguments=%d output=%d", call.index, call.argumentsIndex, output.index)
	}
	for _, responseCreateIndex := range c.responseCreateIndices {
		if responseCreateIndex > output.index {
			observation.ResponseCreatesAfterTool++
		}
	}
	c.collectReplyAfter(output.index)
	return nil
}

func (c *voiceGroundingCollector) collectReplyAfter(outputIndex int) {
	observation := &c.observation
	for _, transcript := range c.spoken {
		if transcript.index <= outputIndex {
			continue
		}
		if observation.SpokenReplyIndex < 0 {
			observation.SpokenReplyIndex = transcript.index
		}
		if observation.SpokenReply != "" {
			observation.SpokenReply += " "
		}
		observation.SpokenReply += strings.TrimSpace(transcript.text)
	}
	for _, done := range c.done {
		if done.index <= observation.SpokenReplyIndex {
			continue
		}
		observation.TerminalIndex = done.index
		observation.TerminalStatus = done.text
		if observation.TerminalStatus == "" {
			observation.TerminalStatus = rtStatusCompleted
		}
		break
	}
	if observation.TerminalIndex < 0 {
		return
	}
	for _, delta := range c.audio {
		if delta.index > outputIndex && delta.index < observation.TerminalIndex {
			observation.AudioBytesAfterTool += delta.bytes
		}
	}
}
