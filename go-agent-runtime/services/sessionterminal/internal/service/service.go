package service

import (
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
	request.Lifecycle.Scheduled.Completed = nonNegative(request.Lifecycle.Scheduled.Completed)
	request.Lifecycle.Scheduled.Dispatched = nonNegative(request.Lifecycle.Scheduled.Dispatched)
	request.Lifecycle.Scheduled.Inputs = nonNegative(request.Lifecycle.Scheduled.Inputs)
	return request
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
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

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
