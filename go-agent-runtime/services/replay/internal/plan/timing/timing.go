package timing

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const defaultOutputSampleRateHz = 24000

const millisecondsPerSecond = 1000

type Report = replay.CaptureTimingReport
type ResponseTiming = replay.CaptureResponseTiming
type ToolTiming = replay.CaptureToolTiming
type Summary = replay.CaptureTimingSummary
type DurationSummary = replay.CaptureDurationSummary

const ReportSchemaVersion = replay.CaptureTimingReportSchemaVersion

type wireEvent struct {
	ResponseID string          `json:"response_id"`
	Delta      string          `json:"delta"`
	CallID     string          `json:"call_id"`
	Name       string          `json:"name"`
	Item       wireItem        `json:"item"`
	Response   json.RawMessage `json:"response"`
}

type wireItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
}

type responseState struct {
	timing         ResponseTiming
	firstAudioMS   int64
	lastAudioMS    int64
	audioBytes     int64
	firstOutputSet bool
	firstAudioSet  bool
}

func AnalyzeCapture(capture gwtesting.SessionCapture) (Report, error) {
	report := initialReport(capture)
	collector := newTimingCollector()
	for _, record := range capture.Records {
		if err := collector.observe(record); err != nil {
			return Report{}, err
		}
	}
	collector.finish(&report)
	return report, nil
}

type timingCollector struct {
	responses            map[string]*responseState
	responseOrder        []string
	commits              []int64
	tools                []ToolTiming
	toolIndex            map[string]int
	continuationRequests []int64
}

func initialReport(capture gwtesting.SessionCapture) Report {
	report := Report{
		SchemaVersion: ReportSchemaVersion,
		Provider:      capture.Provider.Name,
		Model:         capture.Provider.Model,
		SampleRateHz:  outputSampleRate(capture),
	}
	if len(capture.Records) > 0 {
		report.DurationMS = capture.Records[len(capture.Records)-1].TimestampMs
	}
	return report
}

func newTimingCollector() *timingCollector {
	return &timingCollector{
		responses: make(map[string]*responseState),
		tools:     make([]ToolTiming, 0),
		toolIndex: make(map[string]int),
	}
}

func (collector *timingCollector) observe(record gwtesting.CapturedSessionEvent) error {
	event, err := decodeWireEvent(record)
	if err != nil {
		return err
	}
	switch record.Type {
	case "input_audio_buffer.committed":
		collector.commits = append(collector.commits, record.TimestampMs)
	case "response.create":
		collector.observeRequest(record)
	case "response.created":
		collector.observeCreated(record, event)
	case "response.output_audio.delta":
		return collector.observeAudioDelta(record, event)
	case "response.output_audio.done":
		collector.observeAudioDone(record, event)
	case "response.function_call_arguments.done":
		collector.observeToolCall(record, event)
	case "conversation.item.create":
		collector.observeToolResult(record, event)
	case "response.done":
		collector.observeDone(record, event)
	}
	return nil
}

func decodeWireEvent(record gwtesting.CapturedSessionEvent) (wireEvent, error) {
	var event wireEvent
	if len(record.Payload) == 0 {
		return event, nil
	}
	if err := json.Unmarshal(record.Payload, &event); err != nil {
		return wireEvent{}, fmt.Errorf("decode record %d (%s): %w", record.Sequence, record.Type, err)
	}
	return event, nil
}

func (collector *timingCollector) observeRequest(record gwtesting.CapturedSessionEvent) {
	if record.Direction == gwtesting.DirectionClientToServer {
		collector.continuationRequests = append(collector.continuationRequests, record.TimestampMs)
	}
}

func (collector *timingCollector) observeCreated(record gwtesting.CapturedSessionEvent, event wireEvent) {
	id := responseID(event)
	if id == "" {
		return
	}
	if _, exists := collector.responses[id]; exists {
		return
	}
	collector.responses[id] = &responseState{timing: ResponseTiming{ResponseID: id, CreatedMS: record.TimestampMs}}
	collector.responseOrder = append(collector.responseOrder, id)
}

func (collector *timingCollector) observeAudioDelta(record gwtesting.CapturedSessionEvent, event wireEvent) error {
	state := collector.responses[event.ResponseID]
	if state == nil {
		return nil
	}
	setFirstOutput(state, record.TimestampMs)
	if !state.firstAudioSet {
		state.firstAudioSet = true
		state.firstAudioMS = record.TimestampMs
		state.timing.FirstAudioMS = int64Pointer(record.TimestampMs)
	}
	state.lastAudioMS = record.TimestampMs
	decoded, err := codec.DecodeBase64(event.Delta)
	if err != nil {
		return fmt.Errorf("decode audio delta at record %d: %w", record.Sequence, err)
	}
	state.audioBytes += int64(len(decoded))
	return nil
}

func (collector *timingCollector) observeAudioDone(record gwtesting.CapturedSessionEvent, event wireEvent) {
	if state := collector.responses[event.ResponseID]; state != nil {
		state.timing.AudioDoneMS = int64Pointer(record.TimestampMs)
	}
}

func (collector *timingCollector) observeToolCall(record gwtesting.CapturedSessionEvent, event wireEvent) {
	if state := collector.responses[event.ResponseID]; state != nil {
		setFirstOutput(state, record.TimestampMs)
	}
	if event.CallID == "" {
		return
	}
	collector.toolIndex[event.CallID] = len(collector.tools)
	collector.tools = append(collector.tools, ToolTiming{CallID: event.CallID, Name: event.Name, ResponseID: event.ResponseID, CallReadyMS: record.TimestampMs})
}

func (collector *timingCollector) observeToolResult(record gwtesting.CapturedSessionEvent, event wireEvent) {
	if record.Direction != gwtesting.DirectionClientToServer || event.Item.Type != "function_call_output" {
		return
	}
	index, ok := collector.toolIndex[event.Item.CallID]
	if !ok || collector.tools[index].ResultSentMS != nil {
		return
	}
	collector.tools[index].ResultSentMS = int64Pointer(record.TimestampMs)
	collector.tools[index].ExecutionMS = int64Pointer(record.TimestampMs - collector.tools[index].CallReadyMS)
}

func (collector *timingCollector) observeDone(record gwtesting.CapturedSessionEvent, event wireEvent) {
	if state := collector.responses[responseID(event)]; state != nil {
		state.timing.DoneMS = int64Pointer(record.TimestampMs)
	}
}

func (collector *timingCollector) finish(report *Report) {
	for _, id := range collector.responseOrder {
		state := collector.responses[id]
		setAudioTiming(state, report.SampleRateHz)
		report.Responses = append(report.Responses, state.timing)
	}
	linkToolContinuations(collector.tools, collector.continuationRequests, report.Responses)
	report.Tools = collector.tools
	assignTurns(report.Responses, collector.commits)
	computePlaybackTimeline(report.Responses)
	report.Summary = summarize(*report, collector.commits)
}

func setAudioTiming(state *responseState, sampleRate int) {
	if state.audioBytes == 0 {
		return
	}
	state.timing.AudioDurationMS = float64(state.audioBytes) * millisecondsPerSecond / float64(2*sampleRate)
	state.timing.AudioDeliverySpanMS = state.lastAudioMS - state.firstAudioMS
	denominator := math.Max(1, float64(state.timing.AudioDeliverySpanMS))
	state.timing.AudioBurstRatio = state.timing.AudioDurationMS / denominator
}

func responseID(event wireEvent) string {
	if event.ResponseID != "" {
		return event.ResponseID
	}
	if len(event.Response) == 0 {
		return ""
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Response, &response); err != nil {
		return ""
	}
	return response.ID
}

func outputSampleRate(capture gwtesting.SessionCapture) int {
	for index := len(capture.Records) - 1; index >= 0; index-- {
		if capture.Records[index].Type != "response.done" {
			continue
		}
		var payload struct {
			Response struct {
				Audio struct {
					Output struct {
						Format struct {
							Rate int `json:"rate"`
						} `json:"format"`
					} `json:"output"`
				} `json:"audio"`
			} `json:"response"`
		}
		if json.Unmarshal(capture.Records[index].Payload, &payload) == nil && payload.Response.Audio.Output.Format.Rate > 0 {
			return payload.Response.Audio.Output.Format.Rate
		}
	}
	return defaultOutputSampleRateHz
}

func setFirstOutput(state *responseState, timestamp int64) {
	if state == nil || state.firstOutputSet {
		return
	}
	state.firstOutputSet = true
	state.timing.FirstOutputMS = int64Pointer(timestamp)
}

func linkToolContinuations(tools []ToolTiming, requests []int64, responses []ResponseTiming) {
	for index := range tools {
		tool := &tools[index]
		if tool.ResultSentMS == nil {
			continue
		}
		requestMS, ok := firstAtOrAfter(requests, *tool.ResultSentMS)
		if !ok {
			continue
		}
		linkContinuationResponse(tool, requestMS, responses)
	}
}

func linkContinuationResponse(tool *ToolTiming, requestMS int64, responses []ResponseTiming) {
	tool.ContinuationRequestedMS = int64Pointer(requestMS)
	tool.ResultToRequestMS = int64Pointer(requestMS - *tool.ResultSentMS)
	response := firstContinuationResponse(responses, requestMS)
	if response == nil {
		return
	}
	tool.ContinuationResponseID = response.ResponseID
	tool.ContinuationCreatedMS = int64Pointer(response.CreatedMS)
	tool.RequestToCreatedMS = int64Pointer(response.CreatedMS - requestMS)
	linkContinuationOutput(tool, response)
	linkContinuationAudio(tool, response)
}

func firstContinuationResponse(responses []ResponseTiming, requestMS int64) *ResponseTiming {
	for index := range responses {
		if responses[index].CreatedMS >= requestMS {
			return &responses[index]
		}
	}
	return nil
}

func linkContinuationOutput(tool *ToolTiming, response *ResponseTiming) {
	if response.FirstOutputMS == nil {
		return
	}
	tool.ContinuationFirstOutputMS = int64Pointer(*response.FirstOutputMS)
	tool.CreatedToFirstOutputMS = int64Pointer(*response.FirstOutputMS - response.CreatedMS)
	tool.ResultToFirstOutputMS = int64Pointer(*response.FirstOutputMS - *tool.ResultSentMS)
}

func linkContinuationAudio(tool *ToolTiming, response *ResponseTiming) {
	if response.FirstAudioMS == nil {
		return
	}
	tool.ContinuationFirstAudioMS = int64Pointer(*response.FirstAudioMS)
	tool.ResultToFirstAudioMS = int64Pointer(*response.FirstAudioMS - *tool.ResultSentMS)
}

func computePlaybackTimeline(responses []ResponseTiming) {
	var previousEnd int64
	havePrevious := false
	for index := range responses {
		response := &responses[index]
		if response.FirstAudioMS == nil || response.AudioDurationMS <= 0 {
			continue
		}
		if index == 0 || response.TurnIndex != responses[index-1].TurnIndex {
			havePrevious = false
		}
		start := *response.FirstAudioMS
		if havePrevious && previousEnd > start {
			response.EstimatedQueueDelayMS = previousEnd - start
			start = previousEnd
		} else if havePrevious {
			response.EstimatedAudibleGapMS = start - previousEnd
		}
		end := start + int64(math.Round(response.AudioDurationMS))
		response.EstimatedPlaybackStart = int64Pointer(start)
		response.EstimatedPlaybackEnd = int64Pointer(end)
		previousEnd = end
		havePrevious = true
	}
}

func assignTurns(responses []ResponseTiming, commits []int64) {
	for responseIndex := range responses {
		for commitIndex := len(commits) - 1; commitIndex >= 0; commitIndex-- {
			if responses[responseIndex].CreatedMS >= commits[commitIndex] {
				responses[responseIndex].TurnIndex = commitIndex + 1
				break
			}
		}
	}
}

func firstAtOrAfter(values []int64, minimum int64) (int64, bool) {
	for _, value := range values {
		if value >= minimum {
			return value, true
		}
	}
	return 0, false
}

func int64Pointer(value int64) *int64 { return &value }
