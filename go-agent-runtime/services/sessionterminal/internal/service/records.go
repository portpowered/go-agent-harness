package service

import (
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func failureRecord(request sessionterminal.Request, failure *sessionterminal.FailureFacts) sessionterminal.Record {
	fields := map[string]string{
		sessionterminal.FieldClassification:     failure.Classification,
		sessionterminal.FieldTerminalReason:     string(failure.TerminalReason),
		sessionterminal.FieldTerminalProvenance: string(failure.Provenance),
		sessionterminal.FieldOutputState:        string(failure.OutputState),
		sessionterminal.FieldProvider:           request.Provider,
		sessionterminal.FieldModel:              request.Model,
		sessionterminal.FieldTurnsCompleted:     strconv.Itoa(request.TurnsCompleted),
		sessionterminal.FieldFailingEvent:       failure.FailingEvent,
	}
	if failure.ProviderErrorType != "" {
		fields[sessionterminal.FieldProviderErrorType] = failure.ProviderErrorType
	}
	if providerErrorCode := failureCode(request, failure); providerErrorCode != "" {
		fields[sessionterminal.FieldProviderErrorCode] = providerErrorCode
	}
	appendIDs(fields, sessionterminal.FieldUnresolvedToolResultCount, sessionterminal.FieldUnresolvedToolCallIDs, request.Lifecycle.UnresolvedToolResultCallIDs)
	appendIDs(fields, sessionterminal.FieldPendingToolContinuationCount, sessionterminal.FieldPendingToolContinuationIDs, request.Lifecycle.PendingToolContinuationIDs)
	appendIDs(fields, sessionterminal.FieldPendingImageContinuationCount, sessionterminal.FieldPendingImageContinuationIDs, request.Lifecycle.PendingImageContinuationIDs)
	appendScheduled(fields, request.Lifecycle.Scheduled)
	appendMetadata(fields, request.Lifecycle.PendingContinuations)
	return sessionterminal.Record{Event: sessionterminal.EventFailure, Fields: fields}
}

func failureCode(request sessionterminal.Request, failure *sessionterminal.FailureFacts) string {
	if failure.ProviderErrorCode != "" {
		return failure.ProviderErrorCode
	}
	if request.Lifecycle.Scheduled.FailureCode != "" {
		return request.Lifecycle.Scheduled.FailureCode
	}
	for _, id := range metadataIDs(request.Lifecycle.PendingContinuations.Codes) {
		if code := request.Lifecycle.PendingContinuations.Codes[id]; code != "" {
			return code
		}
	}
	return ""
}

func appendMetadata(fields map[string]string, metadata sessionterminal.ContinuationSnapshot) {
	for _, item := range []struct {
		key    string
		values map[string]string
	}{
		{sessionterminal.FieldPendingContinuationStatuses, metadata.Statuses},
		{sessionterminal.FieldPendingContinuationCodes, metadata.Codes},
		{sessionterminal.FieldPendingContinuationDetails, metadata.Details},
	} {
		if formatted := formatMetadata(item.values); formatted != "" {
			fields[item.key] = formatted
		}
	}
}

func cancellationRecord(request sessionterminal.Request) sessionterminal.Record {
	scheduled := request.Lifecycle.Scheduled
	cancelledScheduled := scheduled.Inputs - scheduled.Completed
	if cancelledScheduled < 0 {
		cancelledScheduled = 0
	}
	fields := map[string]string{
		sessionterminal.FieldClassification:     userCancelled,
		sessionterminal.FieldTerminalReason:     string(messages.TerminalReasonCancellation),
		sessionterminal.FieldTerminalProvenance: string(messages.TerminalProvenanceCLI),
		sessionterminal.FieldOutputState:        string((&Service{}).CancellationOutputState(request.Output)),
		sessionterminal.FieldProvider:           request.Provider,
		sessionterminal.FieldModel:              request.Model,
		sessionterminal.FieldTurnsCompleted:     strconv.Itoa(request.TurnsCompleted),
		sessionterminal.FieldCancelledBy:        "user",
	}
	if scheduled.Inputs > 0 {
		appendScheduled(fields, scheduled)
		fields[sessionterminal.FieldCancelledScheduledInputCount] = strconv.Itoa(cancelledScheduled)
	}
	if ids := request.Lifecycle.UnresolvedToolResultCallIDs; len(ids) > 0 {
		fields[sessionterminal.FieldCancelledToolResultCount] = strconv.Itoa(len(ids))
		fields[sessionterminal.FieldCancelledToolResultCallIDs] = strings.Join(ids, ", ")
	}
	if ids := request.Lifecycle.PendingContinuationCallIDs; len(ids) > 0 {
		fields[sessionterminal.FieldCancelledToolContinuationCount] = strconv.Itoa(len(ids))
		fields[sessionterminal.FieldCancelledToolContinuationCallIDs] = strings.Join(ids, ", ")
	}
	return sessionterminal.Record{Event: sessionterminal.EventTerminal, Fields: fields}
}

func appendIDs(fields map[string]string, countKey, idsKey string, ids []string) {
	if len(ids) == 0 {
		return
	}
	fields[countKey] = strconv.Itoa(len(ids))
	fields[idsKey] = strings.Join(ids, ", ")
}

func appendScheduled(fields map[string]string, scheduled sessionterminal.ScheduledSnapshot) {
	if scheduled.Inputs == 0 {
		return
	}
	fields[sessionterminal.FieldScheduledInputCount] = strconv.Itoa(scheduled.Inputs)
	fields[sessionterminal.FieldDispatchedInputCount] = strconv.Itoa(scheduled.Dispatched)
	fields[sessionterminal.FieldCompletedTurnCount] = strconv.Itoa(scheduled.Completed)
}

func formatMetadata(values map[string]string) string {
	ids := metadataIDs(values)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+"="+values[id])
	}
	return strings.Join(parts, ", ")
}

func metadataIDs(values map[string]string) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func metricsRecord(request sessionterminal.Request) sessionterminal.Record {
	fields := map[string]string{
		sessionterminal.FieldProvider:       request.Provider,
		sessionterminal.FieldModel:          request.Model,
		sessionterminal.FieldTurnsCompleted: strconv.Itoa(request.TurnsCompleted),
		"input_audio_bytes":                 strconv.FormatUint(request.Bytes.InputAudioBytes, 10),
		"input_text_bytes":                  strconv.FormatUint(request.Bytes.InputTextBytes, 10),
		"output_audio_bytes":                strconv.FormatUint(request.Bytes.OutputAudioBytes, 10),
		"output_text_bytes":                 strconv.FormatUint(request.Bytes.OutputTextBytes, 10),
		"output_tool_bytes":                 strconv.FormatUint(request.Bytes.OutputToolBytes, 10),
	}
	if request.Usage.Seen {
		fields["provider_prompt_tokens"] = strconv.FormatUint(request.Usage.PromptTokens, 10)
		fields["provider_completion_tokens"] = strconv.FormatUint(request.Usage.CompletionTokens, 10)
		fields["provider_total_tokens"] = strconv.FormatUint(request.Usage.TotalTokens, 10)
		fields["provider_reasoning_tokens"] = strconv.FormatUint(request.Usage.ReasoningTokens, 10)
	}
	appendScheduled(fields, request.Lifecycle.Scheduled)
	return sessionterminal.Record{Event: sessionterminal.EventMetrics, Fields: fields}
}

func accounting(request sessionterminal.Request) *sessionterminal.FinalAccounting {
	return &sessionterminal.FinalAccounting{
		PromptTokens:     request.Usage.PromptTokens,
		CompletionTokens: request.Usage.CompletionTokens,
		TotalTokens:      request.Usage.TotalTokens,
		ReasoningTokens:  request.Usage.ReasoningTokens,
		UsageSemantics:   sessionterminal.TokenUsageIncremental,
		Metrics:          request.Metrics,
	}
}

func cloneMetrics(snapshot metrics.Snapshot) metrics.Snapshot {
	clone := metrics.Snapshot{HistogramBounds: append([]int64(nil), snapshot.HistogramBounds...)}
	if len(clone.HistogramBounds) > sessionterminal.MaxMetricBuckets {
		clone.HistogramBounds = clone.HistogramBounds[:sessionterminal.MaxMetricBuckets]
	}
	seriesCount := len(snapshot.Series)
	if seriesCount > sessionterminal.MaxMetricSeries {
		seriesCount = sessionterminal.MaxMetricSeries
	}
	clone.Series = make([]metrics.SeriesSnapshot, seriesCount)
	for index := 0; index < seriesCount; index++ {
		series := snapshot.Series[index]
		series.Histogram.Bounds = append([]int64(nil), series.Histogram.Bounds...)
		if len(series.Histogram.Bounds) > sessionterminal.MaxMetricBuckets {
			series.Histogram.Bounds = series.Histogram.Bounds[:sessionterminal.MaxMetricBuckets]
		}
		series.Histogram.BucketCounts = append([]uint64(nil), series.Histogram.BucketCounts...)
		if len(series.Histogram.BucketCounts) > sessionterminal.MaxMetricBuckets {
			series.Histogram.BucketCounts = series.Histogram.BucketCounts[:sessionterminal.MaxMetricBuckets]
		}
		clone.Series[index] = series
	}
	return clone
}
