package timing

import (
	"math"
	"slices"
)

func summarize(report Report, commits []int64) Summary {
	responseSamples := collectResponseSamples(report.Responses)
	toolSamples, unfinished := collectToolSamples(report.Tools)
	inputLatencies := collectInputLatencies(report.Responses, commits)
	summary := Summary{
		ResponseCount:              len(report.Responses),
		ToolCallCount:              len(report.Tools),
		AudioResponseCount:         responseSamples.audioResponses,
		UnfinishedToolCallCount:    unfinished,
		MaxAudioBurstRatio:         responseSamples.maxAudioBurst,
		MaxEstimatedQueueDelayMS:   responseSamples.maxQueueDelay,
		InputToFirstOutputMS:       durationSummary(inputLatencies),
		ResponseToFirstOutputMS:    durationSummary(responseSamples.firstOutput),
		ToolExecutionMS:            durationSummary(toolSamples.execution),
		ToolResultToRequestMS:      durationSummary(toolSamples.resultToRequest),
		ToolRequestToCreatedMS:     durationSummary(toolSamples.requestToCreated),
		ToolCreatedToFirstOutputMS: durationSummary(toolSamples.createdToOutput),
		ToolResultToFirstOutputMS:  durationSummary(toolSamples.resultToOutput),
		ToolResultToFirstAudioMS:   durationSummary(toolSamples.resultToAudio),
		EstimatedAudibleGapMS:      durationSummary(responseSamples.audibleGaps),
	}
	return summary
}

type responseSamples struct {
	audioResponses int
	maxAudioBurst  float64
	maxQueueDelay  int64
	firstOutput    []int64
	audibleGaps    []int64
}

func collectResponseSamples(responses []ResponseTiming) responseSamples {
	samples := responseSamples{}
	for _, response := range responses {
		if response.FirstAudioMS != nil {
			samples.audioResponses++
		}
		if response.FirstOutputMS != nil {
			samples.firstOutput = append(samples.firstOutput, *response.FirstOutputMS-response.CreatedMS)
		}
		if response.AudioBurstRatio > samples.maxAudioBurst {
			samples.maxAudioBurst = response.AudioBurstRatio
		}
		if response.EstimatedQueueDelayMS > samples.maxQueueDelay {
			samples.maxQueueDelay = response.EstimatedQueueDelayMS
		}
		if response.EstimatedAudibleGapMS > 0 {
			samples.audibleGaps = append(samples.audibleGaps, response.EstimatedAudibleGapMS)
		}
	}
	return samples
}

func collectInputLatencies(responses []ResponseTiming, commits []int64) []int64 {
	latencies := make([]int64, 0)
	for commitIndex, commit := range commits {
		nextCommit := nextCommitTime(commits, commitIndex)
		for _, response := range responses {
			if isResponseToCommit(response, commit, nextCommit) {
				latencies = append(latencies, *response.FirstOutputMS-commit)
				break
			}
		}
	}
	return latencies
}

func nextCommitTime(commits []int64, index int) int64 {
	if index+1 < len(commits) {
		return commits[index+1]
	}
	return math.MaxInt64
}

func isResponseToCommit(response ResponseTiming, commit, nextCommit int64) bool {
	return response.CreatedMS >= commit && response.CreatedMS < nextCommit && response.FirstOutputMS != nil
}

type toolSamples struct {
	execution        []int64
	resultToRequest  []int64
	requestToCreated []int64
	createdToOutput  []int64
	resultToOutput   []int64
	resultToAudio    []int64
}

func collectToolSamples(tools []ToolTiming) (toolSamples, int) {
	samples := toolSamples{}
	unfinished := 0
	for _, tool := range tools {
		if tool.ExecutionMS == nil {
			unfinished++
		} else {
			samples.execution = append(samples.execution, *tool.ExecutionMS)
		}
		appendToolSamples(&samples, tool)
	}
	return samples, unfinished
}

func appendToolSamples(samples *toolSamples, tool ToolTiming) {
	if tool.ResultToFirstOutputMS != nil {
		samples.resultToOutput = append(samples.resultToOutput, *tool.ResultToFirstOutputMS)
	}
	if tool.ResultToFirstAudioMS != nil {
		samples.resultToAudio = append(samples.resultToAudio, *tool.ResultToFirstAudioMS)
	}
	if tool.ResultToRequestMS != nil {
		samples.resultToRequest = append(samples.resultToRequest, *tool.ResultToRequestMS)
	}
	if tool.RequestToCreatedMS != nil {
		samples.requestToCreated = append(samples.requestToCreated, *tool.RequestToCreatedMS)
	}
	if tool.CreatedToFirstOutputMS != nil {
		samples.createdToOutput = append(samples.createdToOutput, *tool.CreatedToFirstOutputMS)
	}
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
