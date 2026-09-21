package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	probeAudioAppend    = "input_audio_buffer.append"
	probeResponseCancel = "response.cancel"
)

func (s *Service) AnalyzeProbe(ctx context.Context, request replay.CaptureProbeRequest) (replay.CaptureProbeObservation, error) {
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	if strings.TrimSpace(request.SourcePath) == "" {
		return replay.CaptureProbeObservation{}, fmt.Errorf("replay probe capture path is required")
	}

	path, err := s.ResolveCapturePath(ctx, request.SourcePath)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	if request.ValidateSource {
		if validationErrs := gatewaytesting.ValidateSessionCaptureFile(path); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return replay.CaptureProbeObservation{}, fmt.Errorf("session fixture validation failed before any probe observation: %s", strings.Join(messages, "; "))
		}
	}
	capture, err := gatewaytesting.LoadSessionCapture(path)
	if err != nil {
		return replay.CaptureProbeObservation{}, fmt.Errorf("load replay session fixture: %w", err)
	}
	if request.AudioSamples != nil {
		capture, err = injectProbeAudio(capture, request)
		if err != nil {
			return replay.CaptureProbeObservation{}, err
		}
	}
	if err := replayContextError(ctx); err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	report, err := gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(report, capture), nil
}

func (s *Service) AnalyzeProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	capture, err := decodeProbeDocument(ctx, name, document)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	report, err := gatewaytesting.RunSessionReplayProbeFromCapture(ctx, capture)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(report, capture), nil
}

func (s *Service) InspectProbeDocument(ctx context.Context, name string, document []byte) (replay.CaptureProbeObservation, error) {
	capture, err := decodeProbeDocument(ctx, name, document)
	if err != nil {
		return replay.CaptureProbeObservation{}, err
	}
	return observeProbeCapture(gatewaytesting.SessionReplayProbeReport{}, capture), nil
}

func decodeProbeDocument(ctx context.Context, name string, document []byte) (gatewaytesting.SessionCapture, error) {
	if err := replayContextError(ctx); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	if strings.TrimSpace(name) == "" {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay probe document name is required")
	}
	var capture gatewaytesting.SessionCapture
	if err := json.Unmarshal(document, &capture); err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("decode provider capture: %w", err)
	}
	if validationErrs := gatewaytesting.ValidateSessionCapture(name, capture); len(validationErrs) > 0 {
		messages := make([]string, 0, len(validationErrs))
		for _, validationErr := range validationErrs {
			messages = append(messages, validationErr.Error())
		}
		return gatewaytesting.SessionCapture{}, fmt.Errorf("validate provider capture: %s", strings.Join(messages, "; "))
	}
	return capture, nil
}

func (s *Service) WriteCaptureDocument(ctx context.Context, request replay.CaptureDocumentRequest, output io.Writer) error {
	if err := replayContextError(ctx); err != nil {
		return err
	}
	if output == nil {
		return fmt.Errorf("capture document output is required")
	}
	var capture gatewaytesting.SessionCapture
	switch {
	case strings.TrimSpace(request.SourcePath) != "" && strings.TrimSpace(request.SyntheticSessionID) == "":
		path, err := s.ResolveCapturePath(ctx, request.SourcePath)
		if err != nil {
			return err
		}
		if validationErrs := gatewaytesting.ValidateSessionCaptureFile(path); len(validationErrs) > 0 {
			messages := make([]string, 0, len(validationErrs))
			for _, validationErr := range validationErrs {
				messages = append(messages, validationErr.Error())
			}
			return fmt.Errorf("session fixture validation failed before capture export: %s", strings.Join(messages, "; "))
		}
		capture, err = gatewaytesting.LoadSessionCapture(path)
		if err != nil {
			return fmt.Errorf("load capture for export: %w", err)
		}
	case strings.TrimSpace(request.SourcePath) == "" && strings.TrimSpace(request.SyntheticSessionID) != "":
		capture = gatewaytesting.SessionCapture{
			Version:  gatewaytesting.SessionCaptureVersion,
			Provider: gatewaytesting.SessionProviderMetadata{Name: "probe", Model: "fixture"},
			Session: gatewaytesting.SessionMetadata{
				ID:                request.SyntheticSessionID,
				FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
			},
			Records: []gatewaytesting.CapturedSessionEvent{},
		}
	default:
		return fmt.Errorf("select exactly one source capture or synthetic session ID")
	}
	return json.NewEncoder(output).Encode(capture)
}

func injectProbeAudio(capture gatewaytesting.SessionCapture, request replay.CaptureProbeRequest) (gatewaytesting.SessionCapture, error) {
	if request.SampleRateHz <= 0 || request.ExpectedSampleRateHz <= 0 || request.SampleRateHz != request.ExpectedSampleRateHz {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q sample rate = %d, want %d", request.CorpusID, request.SampleRateHz, request.ExpectedSampleRateHz)
	}
	if len(request.AudioSamples) == 0 {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q contains no PCM16 samples", request.CorpusID)
	}
	frames := probePCMFrames(request.AudioSamples)
	appendSlots := 0
	hasCancel := false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		if record.Type == probeAudioAppend {
			appendSlots++
		}
		if isProbeResponseCancel(record.Type) {
			hasCancel = true
		}
	}
	if appendSlots == 0 {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture has no input_audio_buffer.append slot for corpus %q", request.CorpusID)
	}
	if hasCancel && len(frames) < appendSlots {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q has %d frames but replay fixture reserves %d append slots before response.cancel", request.CorpusID, len(frames), appendSlots)
	}

	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(capture.Records)+len(frames)-appendSlots)
	frameIndex := 0
	firstAppend := true
	suffixInserted := false
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == probeAudioAppend {
			if hasCancel {
				if suffixInserted {
					continue
				}
				appendRecord, frameErr := probeAudioAppendRecord(record, frames[frameIndex])
				if frameErr != nil {
					return gatewaytesting.SessionCapture{}, frameErr
				}
				records = append(records, appendRecord)
				frameIndex++
			} else if firstAppend {
				for _, frame := range frames {
					appendRecord, frameErr := probeAudioAppendRecord(record, frame)
					if frameErr != nil {
						return gatewaytesting.SessionCapture{}, frameErr
					}
					records = append(records, appendRecord)
				}
				frameIndex = len(frames)
			}
			firstAppend = false
			continue
		}
		records = append(records, record)
		if hasCancel && !suffixInserted && record.Direction == gatewaytesting.DirectionClientToServer && isProbeResponseCancel(record.Type) {
			for frameIndex < len(frames) {
				appendRecord, frameErr := probeAudioAppendRecord(record, frames[frameIndex])
				if frameErr != nil {
					return gatewaytesting.SessionCapture{}, frameErr
				}
				records = append(records, appendRecord)
				frameIndex++
			}
			suffixInserted = true
		}
	}
	if frameIndex != len(frames) {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture did not receive all %q PCM frames: injected %d of %d", request.CorpusID, frameIndex, len(frames))
	}
	for index := range records {
		records[index].Sequence = index + 1
	}
	injected := capture
	injected.Records = records
	if err := validateProbeAudio(injected, frames); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	sealed, err := gatewaytesting.SealSessionCapture(injected)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("seal injected replay capture: %w", err)
	}
	return sealed, nil
}

func probePCMFrames(samples []int16) [][]byte {
	frames := make([][]byte, 0, (len(samples)+audio.FrameSize-1)/audio.FrameSize)
	for start := 0; start < len(samples); start += audio.FrameSize {
		frame := make([]int16, audio.FrameSize)
		copy(frame, samples[start:])
		frames = append(frames, codec.EncodePCM16(frame))
	}
	return frames
}

func probeAudioAppendRecord(template gatewaytesting.CapturedSessionEvent, pcm []byte) (gatewaytesting.CapturedSessionEvent, error) {
	payload, err := json.Marshal(struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}{Type: probeAudioAppend, Audio: codec.EncodeBase64(pcm)})
	if err != nil {
		return gatewaytesting.CapturedSessionEvent{}, fmt.Errorf("encode replay audio append: %w", err)
	}
	template.PayloadType = gatewaytesting.SessionPayloadTypeWebSocketMessage
	template.Payload = payload
	template.Data = nil
	template.Type = probeAudioAppend
	return template, nil
}

func validateProbeAudio(capture gatewaytesting.SessionCapture, frames [][]byte) error {
	actual := make([]byte, 0, len(frames)*audio.FrameSize*2)
	appendCount := 0
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != probeAudioAppend {
			continue
		}
		appendCount++
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(probeRecordPayload(record), &event); err != nil {
			return fmt.Errorf("decode injected input_audio_buffer.append payload: %w", err)
		}
		if event.Type != probeAudioAppend || event.Audio == "" {
			return fmt.Errorf("injected input_audio_buffer.append payload is missing its audio field")
		}
		pcm, err := codec.DecodeBase64(event.Audio)
		if err != nil {
			return fmt.Errorf("decode injected input audio: %w", err)
		}
		actual = append(actual, pcm...)
	}
	expected := make([]byte, 0, len(frames)*audio.FrameSize*2)
	for _, frame := range frames {
		expected = append(expected, frame...)
	}
	if appendCount != len(frames) {
		return fmt.Errorf("injected replay has %d append payloads for %d PCM frames", appendCount, len(frames))
	}
	if !bytesEqual(actual, expected) {
		return fmt.Errorf("injected replay append payloads do not equal the resolved corpus PCM")
	}
	return nil
}

func observeProbeCapture(report gatewaytesting.SessionReplayProbeReport, capture gatewaytesting.SessionCapture) replay.CaptureProbeObservation {
	observation := replay.CaptureProbeObservation{
		Provider:           capture.Provider.Name,
		Model:              capture.Provider.Model,
		FixtureProvenance:  capture.Session.FixtureProvenance,
		InboundFrames:      report.InboundFrames,
		OutboundTicks:      report.OutboundTicks,
		EndsWithDisconnect: capture.EndsWithDisconnect,
		TerminalReason:     capture.Session.FixtureProvenance,
		Transcript:         probeTranscript(capture),
	}
	if report.EndsWithDisconnect {
		observation.TerminalReason = "disconnect"
	}
	if classification := probeErrorClassification(capture); classification != "" {
		observation.TerminalReason = "error:" + classification
	}
	observation.TerminalMetadataReason, observation.TerminalProvenance, observation.OutputState = probeTerminalTriple(capture)
	outboundTick := 0
	for _, record := range capture.Records {
		if record.Direction == gatewaytesting.DirectionClientToServer {
			outboundTick++
			if !observation.HasInterruptTick && record.Type == probeAudioAppend {
				observation.HasInterruptTick = true
				observation.InterruptTick = outboundTick
			}
			if !observation.HasResponseCancel && isProbeResponseCancel(record.Type) {
				observation.HasResponseCancel = true
				observation.ResponseCancelTick = outboundTick
			}
		}
		var payload struct {
			CallID string `json:"call_id"`
			Item   struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		_ = json.Unmarshal(probeRecordPayload(record), &payload)
		switch {
		case record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "response.function_call_arguments.done" && payload.CallID != "":
			observation.ToolCalls = append(observation.ToolCalls, payload.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "conversation.item.create" && payload.Item.Type == "function_call_output" && payload.Item.CallID != "":
			observation.ToolResultsDelivered = append(observation.ToolResultsDelivered, payload.Item.CallID)
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "tool.result.discarded" && payload.CallID != "":
			observation.ToolResultsDiscarded = append(observation.ToolResultsDiscarded, payload.CallID)
		}
	}
	observeProbeBargeIn(capture, &observation)
	observation.BufferDisposition = probeBufferDisposition(capture)
	observeProbeAudioBounds(capture, &observation)
	return observation
}

func observeProbeBargeIn(capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	inFlight, cancelled := false, false
	for _, record := range capture.Records {
		switch {
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "input_audio_buffer.commit":
			observation.UserTurnsCommitted++
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == "conversation.item.create":
			var payload struct {
				Item struct {
					Type string `json:"type"`
				} `json:"item"`
			}
			if json.Unmarshal(probeRecordPayload(record), &payload) == nil && payload.Item.Type == "message" {
				observation.UserTurnsCommitted++
			}
		case record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "response.created":
			observation.ResponsesCreated++
			inFlight, cancelled = true, false
		case record.Direction == gatewaytesting.DirectionServerToClient && isProbeTranscriptDelta(record.Type):
			if !inFlight || cancelled {
				observation.PostCancelDeltas++
			}
		case record.Direction == gatewaytesting.DirectionClientToServer && record.Type == probeResponseCancel:
			observation.ResponseCancels++
			if !inFlight || cancelled {
				observation.SpuriousCancels++
			} else {
				observation.ResponsesCancelled++
				cancelled = true
			}
		case record.Direction == gatewaytesting.DirectionServerToClient && record.Type == "response.done":
			if inFlight && !cancelled {
				observation.AssistantTurnsDelivered++
			}
			inFlight, cancelled = false, false
		}
	}
	observation.InFlightAtEnd = inFlight
}

func observeProbeAudioBounds(capture gatewaytesting.SessionCapture, observation *replay.CaptureProbeObservation) {
	for index, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		position := record.Sequence
		if position <= 0 {
			position = index + 1
		}
		switch record.Type {
		case "response.output_audio.delta", "response.audio.delta":
			observation.AssistantAudioStarted = true
			if observation.AssistantAudioStartEvent == 0 {
				observation.AssistantAudioStartEvent = position
			}
		case "response.output_audio.done", "response.audio.done":
			if observation.AssistantAudioStarted {
				observation.AssistantAudioStopped = true
				observation.AssistantAudioStopEvent = position
			}
		}
	}
}

func probeTranscript(capture gatewaytesting.SessionCapture) string {
	var builder strings.Builder
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta":
		default:
			continue
		}
		var payload struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(probeRecordPayload(record), &payload) == nil {
			builder.WriteString(payload.Delta)
		}
	}
	return builder.String()
}

func probeTerminalTriple(capture gatewaytesting.SessionCapture) (reason, provenance, outputState string) {
	hasOutput, hasCompletion := false, false
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "response.text.delta", "response.output_text.delta", "response.audio_transcript.delta", "response.audio.delta", "response.output_audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			if json.Unmarshal(probeRecordPayload(record), &payload) == nil && payload.Delta != "" {
				hasOutput = true
			}
		case "response.done":
			hasCompletion = true
		}
	}
	if classification := probeErrorClassification(capture); classification != "" {
		return "error:" + classification, "provider", probeOutputState(hasOutput)
	}
	if capture.EndsWithDisconnect {
		return "disconnect", "provider", probeOutputState(hasOutput)
	}
	if hasCompletion {
		return "complete", "provider", "complete"
	}
	return "", "", ""
}

func probeOutputState(hasOutput bool) string {
	if hasOutput {
		return "partial"
	}
	return "none"
}

func probeBufferDisposition(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		switch record.Type {
		case "input_audio_buffer.committed":
			return "committed"
		case "input_audio_buffer.discarded":
			return "discarded"
		}
	}
	return ""
}

func probeErrorClassification(capture gatewaytesting.SessionCapture) string {
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionServerToClient {
			continue
		}
		if strings.HasPrefix(record.Type, "malformed.") {
			return providers.ErrorClassInvalidRequest
		}
		if record.Type != "error" {
			continue
		}
		var payload struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(probeRecordPayload(record), &payload) != nil {
			continue
		}
		if classification := providers.SessionErrorClassification(payload.Error.Type, payload.Error.Code); classification != "" {
			return classification
		}
	}
	return ""
}

func isProbeResponseCancel(eventType string) bool {
	return eventType == "response.cancel" || eventType == "RESPONSE.CANCEL"
}

func isProbeTranscriptDelta(eventType string) bool {
	switch eventType {
	case "response.text.delta", "response.audio_transcript.delta", "response.output_text.delta", "response.output_audio_transcript.delta":
		return true
	default:
		return false
	}
}

func probeRecordPayload(record gatewaytesting.CapturedSessionEvent) []byte {
	if len(record.Payload) != 0 {
		return record.Payload
	}
	return record.Data
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
