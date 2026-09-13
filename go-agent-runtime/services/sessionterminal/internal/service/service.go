package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	failingEventConnect = "SESSION.CONNECT"
	failingEventRun     = "SESSION.RUN"
	userCancelled       = "user_cancelled"
)

type Service struct{}

func New() *Service { return &Service{} }

var _ sessionterminal.Service = (*Service)(nil)

func (s *Service) Finalize(request sessionterminal.Request) sessionterminal.Result {
	request = normalizeRequest(request)
	result := sessionterminal.Result{
		Error:      request.RunError,
		Accounting: accounting(request),
	}
	if request.UserCancelled {
		result.Records = append(result.Records, cancellationRecord(request))
	} else if failure := terminalFailure(request); failure != nil {
		result.Records = append(result.Records, failureRecord(request, failure))
	}
	result.Records = append(result.Records, metricsRecord(request))
	return result
}

func (*Service) CancellationOutputState(snapshot sessionterminal.OutputSnapshot) messages.TerminalOutputState {
	if snapshot.TurnsCompleted > 0 || snapshot.TotalOutputAudioBytes > 0 || snapshot.TotalOutputTextBytes > 0 || snapshot.ResponseOutputAudioBytes > 0 || snapshot.ResponseOutputTextBytes > 0 || snapshot.AssistantOutputObserved {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

func normalizeRequest(request sessionterminal.Request) sessionterminal.Request {
	if request.TurnsCompleted < 0 {
		request.TurnsCompleted = 0
	}
	request.Provider = bounded(request.Provider, sessionterminal.MaxDiagnosticValueBytes)
	request.Model = bounded(request.Model, sessionterminal.MaxDiagnosticValueBytes)
	request.Output.TurnsCompleted = request.TurnsCompleted
	request.Failure = cloneFailure(request.Failure)
	request.Lifecycle = normalizeLifecycle(request.Lifecycle)
	request.Metrics = cloneMetrics(request.Metrics)
	if request.Lifecycle.Scheduled.Completed < 0 {
		request.Lifecycle.Scheduled.Completed = 0
	}
	if request.Lifecycle.Scheduled.Dispatched < 0 {
		request.Lifecycle.Scheduled.Dispatched = 0
	}
	if request.Lifecycle.Scheduled.Inputs < 0 {
		request.Lifecycle.Scheduled.Inputs = 0
	}
	return request
}

func cloneFailure(failure *sessionterminal.FailureFacts) *sessionterminal.FailureFacts {
	if failure == nil {
		return nil
	}
	copy := *failure
	copy.Classification = bounded(copy.Classification, sessionterminal.MaxDiagnosticValueBytes)
	copy.TerminalReason = messages.TerminalReason(bounded(string(copy.TerminalReason), sessionterminal.MaxDiagnosticValueBytes))
	copy.Provenance = messages.TerminalProvenance(bounded(string(copy.Provenance), sessionterminal.MaxDiagnosticValueBytes))
	copy.OutputState = messages.TerminalOutputState(bounded(string(copy.OutputState), sessionterminal.MaxDiagnosticValueBytes))
	copy.ProviderErrorType = bounded(copy.ProviderErrorType, sessionterminal.MaxDiagnosticValueBytes)
	copy.ProviderErrorCode = bounded(copy.ProviderErrorCode, sessionterminal.MaxDiagnosticValueBytes)
	copy.FailingEvent = bounded(copy.FailingEvent, sessionterminal.MaxDiagnosticValueBytes)
	if copy.Classification == "" {
		copy.Classification = providers.ErrorClassUnknown
	}
	if copy.TerminalReason == "" {
		copy.TerminalReason = messages.TerminalReasonTerminalFailure
	}
	if copy.Provenance == "" {
		copy.Provenance = messages.TerminalProvenanceProvider
	}
	if copy.OutputState == "" {
		copy.OutputState = messages.TerminalOutputNone
	}
	if copy.FailingEvent == "" {
		copy.FailingEvent = failingEventRun
	}
	return &copy
}

func normalizeLifecycle(lifecycle sessionterminal.LifecycleSnapshot) sessionterminal.LifecycleSnapshot {
	lifecycle.UnresolvedToolResultCallIDs = normalizeIDs(lifecycle.UnresolvedToolResultCallIDs)
	lifecycle.PendingContinuationCallIDs = normalizeIDs(lifecycle.PendingContinuationCallIDs)
	lifecycle.PendingToolContinuationIDs = normalizeIDs(lifecycle.PendingToolContinuationIDs)
	lifecycle.PendingImageContinuationIDs = normalizeIDs(lifecycle.PendingImageContinuationIDs)
	lifecycle.FailureHints = normalizeStrings(lifecycle.FailureHints, sessionterminal.MaxDiagnosticItems, sessionterminal.MaxDiagnosticValueBytes)
	lifecycle.PendingContinuations.Statuses = normalizeMetadata(lifecycle.PendingContinuations.Statuses, sessionterminal.MaxContinuationDetailBytes)
	lifecycle.PendingContinuations.Codes = normalizeMetadata(lifecycle.PendingContinuations.Codes, sessionterminal.MaxContinuationDetailBytes)
	lifecycle.PendingContinuations.Details = normalizeMetadata(lifecycle.PendingContinuations.Details, sessionterminal.MaxContinuationDetailBytes)
	lifecycle.Scheduled.FailureStatus = bounded(lifecycle.Scheduled.FailureStatus, sessionterminal.MaxContinuationDetailBytes)
	lifecycle.Scheduled.FailureCode = bounded(lifecycle.Scheduled.FailureCode, sessionterminal.MaxContinuationDetailBytes)
	lifecycle.Scheduled.FailureDetails = bounded(lifecycle.Scheduled.FailureDetails, sessionterminal.MaxContinuationDetailBytes)
	return lifecycle
}

func normalizeIDs(values []string) []string {
	return normalizeStrings(values, sessionterminal.MaxDiagnosticItems, sessionterminal.MaxDiagnosticValueBytes)
}

func normalizeStrings(values []string, limit, maxBytes int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	seen := make(map[string]struct{}, len(values))
	ordered := make([]string, 0, len(values))
	for _, value := range values {
		value = bounded(strings.TrimSpace(value), maxBytes)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return ordered
}

func normalizeMetadata(values map[string]string, maxBytes int) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > sessionterminal.MaxDiagnosticItems {
		keys = keys[:sessionterminal.MaxDiagnosticItems]
	}
	copy := make(map[string]string, len(keys))
	for _, originalKey := range keys {
		key := bounded(strings.TrimSpace(originalKey), sessionterminal.MaxDiagnosticValueBytes)
		if key == "" {
			continue
		}
		copy[key] = bounded(values[originalKey], maxBytes)
	}
	return copy
}

func terminalFailure(request sessionterminal.Request) *sessionterminal.FailureFacts {
	unresolved := request.Lifecycle.UnresolvedToolResultCallIDs
	pendingAll := request.Lifecycle.PendingContinuationCallIDs
	pendingTool := request.Lifecycle.PendingToolContinuationIDs
	pendingImage := request.Lifecycle.PendingImageContinuationIDs
	scheduleIncomplete := request.Lifecycle.Scheduled.Incomplete
	if request.Failure == nil && len(unresolved) == 0 && len(pendingAll) == 0 && len(request.Lifecycle.PendingContinuations.Statuses) == 0 && len(request.Lifecycle.PendingContinuations.Codes) == 0 && len(request.Lifecycle.PendingContinuations.Details) == 0 && len(pendingTool) == 0 && len(pendingImage) == 0 && !scheduleIncomplete {
		if request.RunError == nil || errors.Is(request.RunError, context.Canceled) || errors.Is(request.RunError, context.DeadlineExceeded) {
			return nil
		}
	}
	if request.RoomBoundCancellation && request.Failure == nil && request.RoomCancellationOnly {
		return nil
	}
	if request.Failure != nil {
		return request.Failure
	}
	for _, hint := range request.Lifecycle.FailureHints {
		switch hint {
		case sessionterminal.FailureHintUnresolvedToolResults:
			if len(unresolved) > 0 {
				return lifecycleFailure("unresolved_tool_result", request)
			}
		case sessionterminal.FailureHintImageContinuationIncomplete:
			if len(pendingImage) > 0 {
				return lifecycleFailure("image_tool_continuation", request)
			}
		case sessionterminal.FailureHintToolContinuationIncomplete:
			if len(pendingTool) > 0 {
				return lifecycleFailure("tool_continuation", request)
			}
		case sessionterminal.FailureHintScheduledAudioIncomplete:
			if scheduleIncomplete {
				return lifecycleFailure("scheduled_audio_incomplete", request)
			}
		}
	}
	if recovered := factsFromSessionRunError(request.RunError); recovered != nil {
		return recovered
	}
	if request.RunError == nil && len(unresolved) == 0 && len(pendingAll) == 0 && len(pendingTool) == 0 && len(pendingImage) == 0 {
		return nil
	}
	if len(unresolved) > 0 {
		return lifecycleFailure("unresolved_tool_result", request)
	}
	if len(pendingTool) > 0 {
		return lifecycleFailure("tool_continuation", request)
	}
	if len(pendingImage) > 0 {
		return lifecycleFailure("image_tool_continuation", request)
	}
	if len(pendingAll) > 0 {
		return lifecycleFailure("tool_continuation", request)
	}
	classification := providers.ErrorClassification(request.RunError)
	if classification == "" {
		classification = providers.ErrorClassUnknown
	}
	failingEvent := failingEventRun
	if !request.Output.SawSessionOpen {
		failingEvent = failingEventConnect
	}
	return &sessionterminal.FailureFacts{
		Classification: classification,
		TerminalReason: messages.TerminalReasonTerminalFailure,
		Provenance:     messages.TerminalProvenanceCLI,
		OutputState:    derivedOutputState(request.Output),
		FailingEvent:   failingEvent,
	}
}

func lifecycleFailure(classification string, request sessionterminal.Request) *sessionterminal.FailureFacts {
	return &sessionterminal.FailureFacts{
		Classification: classification,
		TerminalReason: messages.TerminalReasonTerminalFailure,
		Provenance:     messages.TerminalProvenanceSession,
		OutputState:    derivedOutputState(request.Output),
		FailingEvent:   failingEventRun,
	}
}

func derivedOutputState(snapshot sessionterminal.OutputSnapshot) messages.TerminalOutputState {
	if !snapshot.SawSessionOpen {
		return messages.TerminalOutputNone
	}
	if snapshot.TurnsCompleted > 0 {
		return messages.TerminalOutputPartial
	}
	return messages.TerminalOutputNone
}

func factsFromSessionRunError(err error) *sessionterminal.FailureFacts {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr == nil || deltaErr.Value == nil {
		return nil
	}
	value := deltaErr.Value
	failure := &sessionterminal.FailureFacts{
		Classification:    value.Classification,
		TerminalReason:    value.TerminalReason,
		Provenance:        value.TerminalProvenance,
		OutputState:       value.OutputState,
		ProviderErrorType: value.ErrorType,
		ProviderErrorCode: value.Code,
		FailingEvent:      string(messages.StreamTypeError),
	}
	if failure.Classification == "" {
		failure.Classification = providers.ErrorClassUnknown
	}
	if failure.TerminalReason == "" {
		failure.TerminalReason = messages.TerminalReasonTerminalFailure
	}
	if failure.Provenance == "" {
		failure.Provenance = messages.TerminalProvenanceProvider
	}
	if failure.OutputState == "" {
		failure.OutputState = messages.TerminalOutputNone
	}
	return failure
}

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
	providerErrorCode := failure.ProviderErrorCode
	if providerErrorCode == "" {
		providerErrorCode = request.Lifecycle.Scheduled.FailureCode
	}
	if providerErrorCode == "" {
		for _, id := range metadataIDs(request.Lifecycle.PendingContinuations.Codes) {
			if code := request.Lifecycle.PendingContinuations.Codes[id]; code != "" {
				providerErrorCode = code
				break
			}
		}
	}
	if providerErrorCode != "" {
		fields[sessionterminal.FieldProviderErrorCode] = providerErrorCode
	}
	appendIDs(fields, sessionterminal.FieldUnresolvedToolResultCount, sessionterminal.FieldUnresolvedToolCallIDs, request.Lifecycle.UnresolvedToolResultCallIDs)
	appendIDs(fields, sessionterminal.FieldPendingToolContinuationCount, sessionterminal.FieldPendingToolContinuationIDs, request.Lifecycle.PendingToolContinuationIDs)
	appendIDs(fields, sessionterminal.FieldPendingImageContinuationCount, sessionterminal.FieldPendingImageContinuationIDs, request.Lifecycle.PendingImageContinuationIDs)
	appendScheduled(fields, request.Lifecycle.Scheduled)
	if formatted := formatMetadata(request.Lifecycle.PendingContinuations.Statuses); formatted != "" {
		fields[sessionterminal.FieldPendingContinuationStatuses] = formatted
	}
	if formatted := formatMetadata(request.Lifecycle.PendingContinuations.Codes); formatted != "" {
		fields[sessionterminal.FieldPendingContinuationCodes] = formatted
	}
	if formatted := formatMetadata(request.Lifecycle.PendingContinuations.Details); formatted != "" {
		fields[sessionterminal.FieldPendingContinuationDetails] = formatted
	}
	return sessionterminal.Record{Event: sessionterminal.EventFailure, Fields: fields}
}

func cancellationRecord(request sessionterminal.Request) sessionterminal.Record {
	completed := request.Lifecycle.Scheduled.Completed
	cancelledScheduled := request.Lifecycle.Scheduled.Inputs - completed
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
	if request.Lifecycle.Scheduled.Inputs > 0 {
		appendScheduled(fields, request.Lifecycle.Scheduled)
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

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
