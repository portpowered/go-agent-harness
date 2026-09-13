package sessionterminal_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestFinalizeProviderErrorPreservesTypedCauseAndFields(t *testing.T) {
	cause := &providers.ProviderError{Provider: "openai", StatusCode: 401, Err: providers.ErrAuthentication}
	streamValue := &messages.ErrorValue{
		Classification:     providers.ErrorClassAuthentication,
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceProvider,
		OutputState:        messages.TerminalOutputPartial,
		ErrorType:          "invalid_request_error",
		Code:               "invalid_api_key",
		Err:                cause,
	}
	result := terminalwire.NewService().Finalize(sessionterminal.Request{
		RunError:       &engine.StreamDeltaError{Value: streamValue},
		Provider:       "openai",
		Model:          "gpt-realtime",
		TurnsCompleted: 2,
		Output:         sessionterminal.OutputSnapshot{SawSessionOpen: true, TurnsCompleted: 2},
	})

	if !errors.Is(result.Error, cause) || !errors.Is(result.Error, providers.ErrAuthentication) {
		t.Fatalf("typed error identity lost: %v", result.Error)
	}
	record := findRecord(t, result, sessionterminal.EventFailure)
	if got := record.Fields[sessionterminal.FieldClassification]; got != providers.ErrorClassAuthentication {
		t.Fatalf("classification = %q", got)
	}
	if got := record.Fields[sessionterminal.FieldProviderErrorCode]; got != "invalid_api_key" {
		t.Fatalf("provider code = %q", got)
	}
	if got := record.Fields[sessionterminal.FieldOutputState]; got != string(messages.TerminalOutputPartial) {
		t.Fatalf("output state = %q", got)
	}
}

func TestFinalizeLifecycleHintsOrderMetadataAndErrorIdentity(t *testing.T) {
	cause := errors.New("provider stopped after tool result")
	result := terminalwire.NewService().Finalize(sessionterminal.Request{
		RunError:       cause,
		Provider:       "provider",
		Model:          "model",
		TurnsCompleted: 1,
		Output:         sessionterminal.OutputSnapshot{SawSessionOpen: true, TurnsCompleted: 1},
		Lifecycle: sessionterminal.LifecycleSnapshot{
			PendingToolContinuationIDs:  []string{" z", "a", "a"},
			PendingContinuationCallIDs:  []string{"z", "a", "a"},
			PendingImageContinuationIDs: []string{},
			PendingContinuations: sessionterminal.ContinuationSnapshot{
				Statuses: map[string]string{"z": "failed", "a": "incomplete"},
				Codes:    map[string]string{"z": "z-code", "a": "a-code"},
				Details:  map[string]string{"z": "z-detail", "a": "a-detail"},
			},
			FailureHints: []string{sessionterminal.FailureHintToolContinuationIncomplete},
		},
	})
	if !errors.Is(result.Error, cause) {
		t.Fatalf("cause was not retained: %v", result.Error)
	}
	record := findRecord(t, result, sessionterminal.EventFailure)
	if got := record.Fields[sessionterminal.FieldPendingToolContinuationIDs]; got != "a, z" {
		t.Fatalf("continuation IDs = %q", got)
	}
	if got := record.Fields[sessionterminal.FieldPendingContinuationStatuses]; got != "a=incomplete, z=failed" {
		t.Fatalf("statuses = %q", got)
	}
	if got := record.Fields[sessionterminal.FieldPendingContinuationCodes]; got != "a=a-code, z=z-code" {
		t.Fatalf("codes = %q", got)
	}
	if got := record.Fields[sessionterminal.FieldPendingContinuationDetails]; got != "a=a-detail, z=z-detail" {
		t.Fatalf("details = %q", got)
	}
}

func TestFinalizeCancellationAndRoomBoundPrecedence(t *testing.T) {
	service := terminalwire.NewService()
	cancelled := service.Finalize(sessionterminal.Request{
		UserCancelled:  true,
		Provider:       "provider",
		Model:          "model",
		TurnsCompleted: 1,
		Output:         sessionterminal.OutputSnapshot{TurnsCompleted: 1},
		Lifecycle: sessionterminal.LifecycleSnapshot{
			UnresolvedToolResultCallIDs: []string{"call-2", "call-1"},
			PendingContinuationCallIDs:  []string{"continuation-1"},
			Scheduled:                   sessionterminal.ScheduledSnapshot{Inputs: 3, Dispatched: 2, Completed: 1},
		},
	})
	terminal := findRecord(t, cancelled, sessionterminal.EventTerminal)
	if _, ok := findRecordMaybe(cancelled, sessionterminal.EventFailure); ok {
		t.Fatal("user cancellation was promoted to failure")
	}
	if got := terminal.Fields[sessionterminal.FieldOutputState]; got != string(messages.TerminalOutputPartial) {
		t.Fatalf("cancellation output state = %q", got)
	}
	if got := terminal.Fields[sessionterminal.FieldCancelledToolResultCallIDs]; got != "call-1, call-2" {
		t.Fatalf("cancelled tool IDs = %q", got)
	}
	if got := terminal.Fields[sessionterminal.FieldCancelledToolContinuationCallIDs]; got != "continuation-1" {
		t.Fatalf("cancelled continuation IDs = %q", got)
	}

	roomBound := service.Finalize(sessionterminal.Request{
		RunError:              context.Canceled,
		RoomBoundCancellation: true,
		RoomCancellationOnly:  true,
		Lifecycle: sessionterminal.LifecycleSnapshot{
			UnresolvedToolResultCallIDs: []string{"still-pending"},
		},
	})
	if _, ok := findRecordMaybe(roomBound, sessionterminal.EventFailure); ok {
		t.Fatal("room-owned cancellation emitted session_failure")
	}
}

func TestFinalizeOutputStateAndAccountingAreIndependentAndDeepCopied(t *testing.T) {
	service := terminalwire.NewService()
	metricsSnapshot := metrics.Snapshot{
		HistogramBounds: []int64{1, 2},
		Series: []metrics.SeriesSnapshot{{
			Direction:  metrics.DirectionOutput,
			Modality:   metrics.ModalityText,
			TotalBytes: 7,
			Histogram: metrics.HistogramSnapshot{
				Bounds: []int64{1, 2}, BucketCounts: []uint64{3, 4}, SampleCount: 7,
			},
		}},
	}
	request := sessionterminal.Request{
		Provider:       "provider",
		Model:          "model",
		TurnsCompleted: 2,
		Output:         sessionterminal.OutputSnapshot{TurnsCompleted: 2, TotalOutputTextBytes: 4},
		Bytes:          sessionterminal.ByteSnapshot{InputAudioBytes: 8, OutputTextBytes: 4},
		Usage:          sessionterminal.TokenSnapshot{PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24, Seen: true},
		Metrics:        metricsSnapshot,
	}
	result := service.Finalize(request)
	if got := service.CancellationOutputState(sessionterminal.OutputSnapshot{}); got != messages.TerminalOutputNone {
		t.Fatalf("empty output state = %q", got)
	}
	if got := service.CancellationOutputState(request.Output); got != messages.TerminalOutputPartial {
		t.Fatalf("partial output state = %q", got)
	}
	if result.Accounting == nil || result.Accounting.PromptTokens != 11 || result.Accounting.Metrics.Series[0].TotalBytes != 7 {
		t.Fatalf("accounting = %#v", result.Accounting)
	}
	request.Metrics.HistogramBounds[0] = 99
	request.Metrics.Series[0].Histogram.BucketCounts[0] = 99
	if result.Accounting.Metrics.HistogramBounds[0] != 1 || result.Accounting.Metrics.Series[0].Histogram.BucketCounts[0] != 3 {
		t.Fatal("accounting aliases request metrics")
	}
	result.Accounting.Metrics.Series[0].Histogram.Bounds[0] = 77
	if request.Metrics.Series[0].Histogram.Bounds[0] != 1 {
		t.Fatal("accounting mutation leaked into request")
	}
	if !reflect.DeepEqual(result.Accounting.Metrics.Series[0].Histogram.BucketCounts, []uint64{3, 4}) {
		t.Fatalf("bucket counts changed: %#v", result.Accounting.Metrics.Series[0].Histogram.BucketCounts)
	}
}

func findRecord(t *testing.T, result sessionterminal.Result, event string) sessionterminal.Record {
	t.Helper()
	record, ok := findRecordMaybe(result, event)
	if !ok {
		t.Fatalf("missing %s in %#v", event, result.Records)
	}
	return record
}

func findRecordMaybe(result sessionterminal.Result, event string) (sessionterminal.Record, bool) {
	for _, record := range result.Records {
		if record.Event == event {
			return record, true
		}
	}
	return sessionterminal.Record{}, false
}
