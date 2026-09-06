package sessiontiming

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	ReportSchemaVersion       = 1
	defaultOutputSampleRateHz = 24000
)

type Report struct {
	SchemaVersion int              `json:"schema_version"`
	Provider      string           `json:"provider"`
	Model         string           `json:"model"`
	DurationMS    int64            `json:"duration_ms"`
	SampleRateHz  int              `json:"sample_rate_hz"`
	Responses     []ResponseTiming `json:"responses"`
	Tools         []ToolTiming     `json:"tools"`
	Summary       Summary          `json:"summary"`
}

type ResponseTiming struct {
	ResponseID             string  `json:"response_id"`
	TurnIndex              int     `json:"turn_index,omitempty"`
	CreatedMS              int64   `json:"created_ms"`
	FirstOutputMS          *int64  `json:"first_output_ms,omitempty"`
	FirstAudioMS           *int64  `json:"first_audio_ms,omitempty"`
	AudioDoneMS            *int64  `json:"audio_done_ms,omitempty"`
	DoneMS                 *int64  `json:"done_ms,omitempty"`
	AudioDurationMS        float64 `json:"audio_duration_ms"`
	AudioDeliverySpanMS    int64   `json:"audio_delivery_span_ms,omitempty"`
	AudioBurstRatio        float64 `json:"audio_burst_ratio,omitempty"`
	EstimatedPlaybackStart *int64  `json:"estimated_playback_start_ms,omitempty"`
	EstimatedPlaybackEnd   *int64  `json:"estimated_playback_end_ms,omitempty"`
	EstimatedAudibleGapMS  int64   `json:"estimated_audible_gap_ms,omitempty"`
	EstimatedQueueDelayMS  int64   `json:"estimated_queue_delay_ms,omitempty"`
}

type ToolTiming struct {
	CallID                    string `json:"call_id"`
	Name                      string `json:"name"`
	ResponseID                string `json:"response_id"`
	CallReadyMS               int64  `json:"call_ready_ms"`
	ResultSentMS              *int64 `json:"result_sent_ms,omitempty"`
	ExecutionMS               *int64 `json:"execution_ms,omitempty"`
	ContinuationRequestedMS   *int64 `json:"continuation_requested_ms,omitempty"`
	ContinuationResponseID    string `json:"continuation_response_id,omitempty"`
	ContinuationCreatedMS     *int64 `json:"continuation_created_ms,omitempty"`
	ContinuationFirstOutputMS *int64 `json:"continuation_first_output_ms,omitempty"`
	ContinuationFirstAudioMS  *int64 `json:"continuation_first_audio_ms,omitempty"`
	ResultToRequestMS         *int64 `json:"result_to_request_ms,omitempty"`
	RequestToCreatedMS        *int64 `json:"request_to_created_ms,omitempty"`
	CreatedToFirstOutputMS    *int64 `json:"created_to_first_output_ms,omitempty"`
	ResultToFirstOutputMS     *int64 `json:"result_to_first_output_ms,omitempty"`
	ResultToFirstAudioMS      *int64 `json:"result_to_first_audio_ms,omitempty"`
}

type Summary struct {
	ResponseCount              int             `json:"response_count"`
	AudioResponseCount         int             `json:"audio_response_count"`
	ToolCallCount              int             `json:"tool_call_count"`
	UnfinishedToolCallCount    int             `json:"unfinished_tool_call_count"`
	InputToFirstOutputMS       DurationSummary `json:"input_to_first_output_ms"`
	ResponseToFirstOutputMS    DurationSummary `json:"response_created_to_first_output_ms"`
	ToolExecutionMS            DurationSummary `json:"tool_execution_ms"`
	ToolResultToRequestMS      DurationSummary `json:"tool_result_to_request_ms"`
	ToolRequestToCreatedMS     DurationSummary `json:"tool_request_to_response_created_ms"`
	ToolCreatedToFirstOutputMS DurationSummary `json:"tool_response_created_to_first_output_ms"`
	ToolResultToFirstOutputMS  DurationSummary `json:"tool_result_to_first_output_ms"`
	ToolResultToFirstAudioMS   DurationSummary `json:"tool_result_to_first_audio_ms"`
	EstimatedAudibleGapMS      DurationSummary `json:"estimated_audible_gap_ms"`
	MaxAudioBurstRatio         float64         `json:"max_audio_burst_ratio"`
	MaxEstimatedQueueDelayMS   int64           `json:"max_estimated_queue_delay_ms"`
}

type DurationSummary struct {
	Count int   `json:"count"`
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
	MaxMS int64 `json:"max_ms"`
}

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
	report := Report{
		SchemaVersion: ReportSchemaVersion,
		Provider:      capture.Provider.Name,
		Model:         capture.Provider.Model,
		SampleRateHz:  outputSampleRate(capture),
	}
	if len(capture.Records) > 0 {
		report.DurationMS = capture.Records[len(capture.Records)-1].TimestampMs
	}

	responses := make(map[string]*responseState)
	responseOrder := make([]string, 0)
	commits := make([]int64, 0)
	tools := make([]ToolTiming, 0)
	toolIndex := make(map[string]int)
	continuationRequests := make([]int64, 0)

	for _, record := range capture.Records {
		var event wireEvent
		if len(record.Payload) > 0 {
			if err := json.Unmarshal(record.Payload, &event); err != nil {
				return Report{}, fmt.Errorf("decode record %d (%s): %w", record.Sequence, record.Type, err)
			}
		}
		switch record.Type {
		case "input_audio_buffer.committed":
			commits = append(commits, record.TimestampMs)
		case "response.create":
			if record.Direction == gwtesting.DirectionClientToServer {
				continuationRequests = append(continuationRequests, record.TimestampMs)
			}
		case "response.created":
			id := responseID(event)
			if id == "" {
				continue
			}
			if _, exists := responses[id]; !exists {
				responses[id] = &responseState{timing: ResponseTiming{ResponseID: id, CreatedMS: record.TimestampMs}}
				responseOrder = append(responseOrder, id)
			}
		case "response.output_audio.delta":
			state := responses[event.ResponseID]
			if state == nil {
				continue
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
				return Report{}, fmt.Errorf("decode audio delta at record %d: %w", record.Sequence, err)
			}
			state.audioBytes += int64(len(decoded))
		case "response.output_audio.done":
			if state := responses[event.ResponseID]; state != nil {
				state.timing.AudioDoneMS = int64Pointer(record.TimestampMs)
			}
		case "response.function_call_arguments.done":
			if state := responses[event.ResponseID]; state != nil {
				setFirstOutput(state, record.TimestampMs)
			}
			if event.CallID != "" {
				toolIndex[event.CallID] = len(tools)
				tools = append(tools, ToolTiming{CallID: event.CallID, Name: event.Name, ResponseID: event.ResponseID, CallReadyMS: record.TimestampMs})
			}
		case "conversation.item.create":
			if record.Direction != gwtesting.DirectionClientToServer || event.Item.Type != "function_call_output" {
				continue
			}
			if index, ok := toolIndex[event.Item.CallID]; ok && tools[index].ResultSentMS == nil {
				tools[index].ResultSentMS = int64Pointer(record.TimestampMs)
				delta := record.TimestampMs - tools[index].CallReadyMS
				tools[index].ExecutionMS = int64Pointer(delta)
			}
		case "response.done":
			id := responseID(event)
			if state := responses[id]; state != nil {
				state.timing.DoneMS = int64Pointer(record.TimestampMs)
			}
		}
	}

	for _, id := range responseOrder {
		state := responses[id]
		if state.audioBytes > 0 {
			state.timing.AudioDurationMS = float64(state.audioBytes) * 1000 / float64(2*report.SampleRateHz)
			state.timing.AudioDeliverySpanMS = state.lastAudioMS - state.firstAudioMS
			denominator := math.Max(1, float64(state.timing.AudioDeliverySpanMS))
			state.timing.AudioBurstRatio = state.timing.AudioDurationMS / denominator
		}
		report.Responses = append(report.Responses, state.timing)
	}

	linkToolContinuations(tools, continuationRequests, report.Responses)
	report.Tools = tools
	assignTurns(report.Responses, commits)
	computePlaybackTimeline(report.Responses)
	report.Summary = summarize(report, commits)
	return report, nil
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
	_ = json.Unmarshal(event.Response, &response)
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
		if tools[index].ResultSentMS == nil {
			continue
		}
		requestMS, ok := firstAtOrAfter(requests, *tools[index].ResultSentMS)
		if !ok {
			continue
		}
		tools[index].ContinuationRequestedMS = int64Pointer(requestMS)
		resultToRequest := requestMS - *tools[index].ResultSentMS
		tools[index].ResultToRequestMS = int64Pointer(resultToRequest)
		for responseIndex := range responses {
			response := &responses[responseIndex]
			if response.CreatedMS < requestMS {
				continue
			}
			tools[index].ContinuationResponseID = response.ResponseID
			tools[index].ContinuationCreatedMS = int64Pointer(response.CreatedMS)
			requestToCreated := response.CreatedMS - requestMS
			tools[index].RequestToCreatedMS = int64Pointer(requestToCreated)
			if response.FirstOutputMS != nil {
				tools[index].ContinuationFirstOutputMS = int64Pointer(*response.FirstOutputMS)
				createdToFirstOutput := *response.FirstOutputMS - response.CreatedMS
				tools[index].CreatedToFirstOutputMS = int64Pointer(createdToFirstOutput)
				delta := *response.FirstOutputMS - *tools[index].ResultSentMS
				tools[index].ResultToFirstOutputMS = int64Pointer(delta)
			}
			if response.FirstAudioMS != nil {
				tools[index].ContinuationFirstAudioMS = int64Pointer(*response.FirstAudioMS)
				delta := *response.FirstAudioMS - *tools[index].ResultSentMS
				tools[index].ResultToFirstAudioMS = int64Pointer(delta)
			}
			break
		}
	}
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

func summarize(report Report, commits []int64) Summary {
	summary := Summary{ResponseCount: len(report.Responses), ToolCallCount: len(report.Tools)}
	inputLatencies := make([]int64, 0)
	responseLatencies := make([]int64, 0)
	toolExecution := make([]int64, 0)
	toolResultToRequest := make([]int64, 0)
	toolRequestToCreated := make([]int64, 0)
	toolCreatedToFirstOutput := make([]int64, 0)
	toolContinuation := make([]int64, 0)
	toolAudioContinuation := make([]int64, 0)
	audibleGaps := make([]int64, 0)
	for _, response := range report.Responses {
		if response.FirstAudioMS != nil {
			summary.AudioResponseCount++
		}
		if response.FirstOutputMS != nil {
			responseLatencies = append(responseLatencies, *response.FirstOutputMS-response.CreatedMS)
		}
		if response.AudioBurstRatio > summary.MaxAudioBurstRatio {
			summary.MaxAudioBurstRatio = response.AudioBurstRatio
		}
		if response.EstimatedQueueDelayMS > summary.MaxEstimatedQueueDelayMS {
			summary.MaxEstimatedQueueDelayMS = response.EstimatedQueueDelayMS
		}
		if response.EstimatedAudibleGapMS > 0 {
			audibleGaps = append(audibleGaps, response.EstimatedAudibleGapMS)
		}
	}
	for commitIndex, commit := range commits {
		var nextCommit int64 = math.MaxInt64
		if commitIndex+1 < len(commits) {
			nextCommit = commits[commitIndex+1]
		}
		for _, response := range report.Responses {
			if response.CreatedMS >= commit && response.CreatedMS < nextCommit && response.FirstOutputMS != nil {
				inputLatencies = append(inputLatencies, *response.FirstOutputMS-commit)
				break
			}
		}
	}
	for _, tool := range report.Tools {
		if tool.ExecutionMS == nil {
			summary.UnfinishedToolCallCount++
		} else {
			toolExecution = append(toolExecution, *tool.ExecutionMS)
		}
		if tool.ResultToFirstOutputMS != nil {
			toolContinuation = append(toolContinuation, *tool.ResultToFirstOutputMS)
		}
		if tool.ResultToFirstAudioMS != nil {
			toolAudioContinuation = append(toolAudioContinuation, *tool.ResultToFirstAudioMS)
		}
		if tool.ResultToRequestMS != nil {
			toolResultToRequest = append(toolResultToRequest, *tool.ResultToRequestMS)
		}
		if tool.RequestToCreatedMS != nil {
			toolRequestToCreated = append(toolRequestToCreated, *tool.RequestToCreatedMS)
		}
		if tool.CreatedToFirstOutputMS != nil {
			toolCreatedToFirstOutput = append(toolCreatedToFirstOutput, *tool.CreatedToFirstOutputMS)
		}
	}
	summary.InputToFirstOutputMS = durationSummary(inputLatencies)
	summary.ResponseToFirstOutputMS = durationSummary(responseLatencies)
	summary.ToolExecutionMS = durationSummary(toolExecution)
	summary.ToolResultToRequestMS = durationSummary(toolResultToRequest)
	summary.ToolRequestToCreatedMS = durationSummary(toolRequestToCreated)
	summary.ToolCreatedToFirstOutputMS = durationSummary(toolCreatedToFirstOutput)
	summary.ToolResultToFirstOutputMS = durationSummary(toolContinuation)
	summary.ToolResultToFirstAudioMS = durationSummary(toolAudioContinuation)
	summary.EstimatedAudibleGapMS = durationSummary(audibleGaps)
	return summary
}

func durationSummary(values []int64) DurationSummary {
	if len(values) == 0 {
		return DurationSummary{}
	}
	values = append([]int64(nil), values...)
	slices.Sort(values)
	return DurationSummary{
		Count: len(values),
		P50MS: percentile(values, 0.50),
		P95MS: percentile(values, 0.95),
		MaxMS: values[len(values)-1],
	}
}

func percentile(sorted []int64, quantile float64) int64 {
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
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
