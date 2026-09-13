package agentsession

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const c156MutationValue = "mutated"

func TestC156SessionMaxDurationContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		duration time.Duration
	}{
		{name: "zero", duration: 0},
		{name: "positive", duration: time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSessionMaxDuration(test.duration); err != nil {
				t.Fatalf("ValidateSessionMaxDuration(%s) = %v, want nil", test.duration, err)
			}
		})
	}

	err := ValidateSessionMaxDuration(-time.Second)
	var durationErr *SessionMaxDurationError
	if !errors.As(err, &durationErr) {
		t.Fatalf("negative duration error = %T, want *SessionMaxDurationError", err)
	}
	if !errors.Is(err, ErrInvalidSessionMaxDuration) {
		t.Fatalf("negative duration error = %v, want stable sentinel", err)
	}
	if durationErr.Duration != -time.Second {
		t.Fatalf("duration error value = %s, want -1s", durationErr.Duration)
	}
	if got, want := durationErr.Error(), "--max-duration must be non-negative, got -1s"; got != want {
		t.Fatalf("duration error text = %q, want %q", got, want)
	}
	if !errors.Is(durationErr, ErrInvalidSessionMaxDuration) {
		t.Fatalf("duration error = %v, want stable sentinel", durationErr)
	}

	var nilDurationErr *SessionMaxDurationError
	if got := nilDurationErr.Error(); got != ErrInvalidSessionMaxDuration.Error() {
		t.Fatalf("nil duration error text = %q, want sentinel text", got)
	}
	if !errors.Is(nilDurationErr, ErrInvalidSessionMaxDuration) {
		t.Fatalf("nil duration error = %v, want stable sentinel", nilDurationErr)
	}
}

func TestC156AudioInTurnBargeContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		enabled   bool
		turns     int
		wantErr   bool
		wantTurns int
	}{
		{name: "disabled with no turns", enabled: false, turns: 0},
		{name: "disabled with negative turns", enabled: false, turns: -1},
		{name: "enabled with two turns", enabled: true, turns: 2},
		{name: "enabled with extra turns", enabled: true, turns: 3},
		{name: "enabled with negative turns", enabled: true, turns: -1, wantErr: true, wantTurns: 0},
		{name: "enabled with zero turns", enabled: true, turns: 0, wantErr: true, wantTurns: 0},
		{name: "enabled with one turn", enabled: true, turns: 1, wantErr: true, wantTurns: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSessionAudioInTurnBarge(test.enabled, test.turns)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("ValidateSessionAudioInTurnBarge(%t, %d) = %v, want nil", test.enabled, test.turns, err)
				}
				return
			}

			assertC156BargeError(t, err, test.wantTurns)
		})
	}

	var nilBargeErr *SessionAudioInTurnBargeError
	if got := nilBargeErr.Error(); got != ErrSessionAudioInTurnBargeRequiresSequence.Error() {
		t.Fatalf("nil barge error text = %q, want sentinel text", got)
	}
	if !errors.Is(nilBargeErr, ErrSessionAudioInTurnBargeRequiresSequence) {
		t.Fatalf("nil barge error = %v, want stable sentinel", nilBargeErr)
	}
}

func assertC156BargeError(t *testing.T, err error, wantTurns int) {
	t.Helper()

	var bargeErr *SessionAudioInTurnBargeError
	if !errors.As(err, &bargeErr) {
		t.Fatalf("barge error = %T, want *SessionAudioInTurnBargeError", err)
	}
	if !errors.Is(err, ErrSessionAudioInTurnBargeRequiresSequence) {
		t.Fatalf("barge error = %v, want stable sentinel", err)
	}
	if bargeErr.TurnCount != wantTurns {
		t.Fatalf("barge error turn count = %d, want %d", bargeErr.TurnCount, wantTurns)
	}
	if !strings.Contains(bargeErr.Error(), "got "+strconv.Itoa(wantTurns)) {
		t.Fatalf("barge error text = %q, want reported turn count %d", bargeErr.Error(), wantTurns)
	}
	if !errors.Is(bargeErr.Unwrap(), ErrSessionAudioInTurnBargeRequiresSequence) {
		t.Fatalf("barge unwrap = %v, want stable sentinel", bargeErr.Unwrap())
	}
}

func TestC156ReasoningEffortContract(t *testing.T) {
	for _, effort := range []string{"", " ", "minimal", "low", "medium", "high", "xhigh", " medium "} {
		if err := ValidateOpenAIRealtimeReasoningEffort(effort); err != nil {
			t.Fatalf("ValidateOpenAIRealtimeReasoningEffort(%q) = %v, want nil", effort, err)
		}
	}
	if err := ValidateOpenAIRealtimeReasoningEffort("maximum"); err == nil {
		t.Fatal("invalid reasoning effort was accepted")
	} else if !strings.Contains(err.Error(), "--reasoning-effort must be one of") || !strings.Contains(err.Error(), `got "maximum"`) {
		t.Fatalf("invalid reasoning effort error = %q, want actionable value", err)
	}
}

func TestC156DiagnosticContractConstants(t *testing.T) {
	for _, test := range []struct {
		name string
		got  string
		want string
	}{
		{name: "failure event", got: SessionDiagnosticEventFailure, want: "session_failure"},
		{name: "terminal event", got: SessionDiagnosticEventTerminal, want: "session_terminal"},
		{name: "turn event", got: SessionDiagnosticEventTurn, want: "session_turn_completed"},
		{name: "tool call event", got: SessionDiagnosticEventToolCall, want: "session_tool_call_unexecutable"},
		{name: "metrics event", got: SessionDiagnosticEventMetrics, want: "session_metrics"},
		{name: "room bound event", got: SessionDiagnosticEventRoomBound, want: "room_bound_shutdown"},
		{name: "unresolved count", got: SessionDiagnosticFieldUnresolvedToolResultCount, want: "unresolved_tool_result_count"},
		{name: "unresolved IDs", got: SessionDiagnosticFieldUnresolvedToolCallIDs, want: "unresolved_tool_call_ids"},
		{name: "pending image count", got: SessionDiagnosticFieldPendingImageContinuationCount, want: "pending_image_continuation_count"},
		{name: "pending image IDs", got: SessionDiagnosticFieldPendingImageContinuationIDs, want: "pending_image_continuation_call_ids"},
		{name: "pending tool count", got: SessionDiagnosticFieldPendingToolContinuationCount, want: "pending_tool_continuation_count"},
		{name: "pending tool IDs", got: SessionDiagnosticFieldPendingToolContinuationIDs, want: "pending_tool_continuation_call_ids"},
		{name: "scheduled input count", got: SessionDiagnosticFieldScheduledInputCount, want: "scheduled_input_count"},
		{name: "dispatched input count", got: SessionDiagnosticFieldDispatchedInputCount, want: "dispatched_input_count"},
		{name: "completed turn count", got: SessionDiagnosticFieldCompletedTurnCount, want: "completed_turn_count"},
		{name: "pending statuses", got: SessionDiagnosticFieldPendingContinuationStatuses, want: "pending_continuation_statuses"},
		{name: "pending codes", got: SessionDiagnosticFieldPendingContinuationCodes, want: "pending_continuation_codes"},
		{name: "pending details", got: SessionDiagnosticFieldPendingContinuationDetails, want: "pending_continuation_details"},
		{name: "cancelled by", got: SessionDiagnosticFieldCancelledBy, want: "cancelled_by"},
		{name: "cancelled scheduled count", got: SessionDiagnosticFieldCancelledScheduledInputCount, want: "cancelled_scheduled_input_count"},
		{name: "cancelled tool count", got: SessionDiagnosticFieldCancelledToolResultCount, want: "cancelled_tool_result_count"},
		{name: "cancelled tool IDs", got: SessionDiagnosticFieldCancelledToolResultCallIDs, want: "cancelled_tool_result_call_ids"},
		{name: "cancelled continuation count", got: SessionDiagnosticFieldCancelledToolContinuationCount, want: "cancelled_tool_continuation_count"},
		{name: "cancelled continuation IDs", got: SessionDiagnosticFieldCancelledToolContinuationCallIDs, want: "cancelled_tool_continuation_call_ids"},
		{name: "playback overflow event", got: SessionDiagnosticEventPlaybackOverflow, want: "session_playback_overflow"},
		{name: "playback device", got: SessionDiagnosticFieldPlaybackDeviceID, want: "device_id"},
		{name: "playback sample rate", got: SessionDiagnosticFieldPlaybackSampleRate, want: "sample_rate"},
		{name: "playback channels", got: SessionDiagnosticFieldPlaybackChannels, want: "channels"},
		{name: "playback latency", got: SessionDiagnosticFieldPlaybackLatencyTargetMillis, want: "latency_target_ms"},
		{name: "playback capacity", got: SessionDiagnosticFieldPlaybackCapacitySamples, want: "capacity_samples"},
		{name: "playback queued", got: SessionDiagnosticFieldPlaybackQueuedSamples, want: "queued_samples"},
		{name: "playback peak queued", got: SessionDiagnosticFieldPlaybackPeakQueuedSamples, want: "peak_queued_samples"},
		{name: "playback dropped", got: SessionDiagnosticFieldPlaybackDroppedSamples, want: "dropped_samples"},
		{name: "playback overflow count", got: SessionDiagnosticFieldPlaybackOverflowEvents, want: "overflow_events"},
		{name: "playback participant", got: SessionDiagnosticFieldPlaybackParticipantID, want: "participant_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("diagnostic contract value = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestC156RequestAndAudioInputPreserveCallerMetadata(t *testing.T) {
	reader := bytes.NewReader([]byte{0x01, 0x02, 0x03})
	request := Request{
		Provider:         "provider-name",
		ProviderProvided: true,
		Prompt:           "",
		PromptProvided:   true,
		TextSeed:         TextSeed{Value: "", Present: true},
		AudioInput: AudioInput{
			Path:               "input.raw",
			Stdin:              reader,
			SourceSampleRate:   24000,
			CloseStdinOnCancel: true,
			MaxDuration:        3 * time.Second,
			Present:            true,
			DevicePresent:      false,
		},
		AudioTurns:               []string{"turn-1", "turn-2"},
		AudioInterrupts:          []string{"interrupt-1"},
		AudioInterruptTool:       "lookup",
		AudioInputDevice:         "default-input",
		AudioOutputDevice:        "default-output",
		AudioInputDevicePresent:  true,
		AudioOutputDevicePresent: true,
	}

	if request.AudioInput.Stdin != reader {
		t.Fatal("request audio input did not preserve the caller's reader")
	}
	if got, want := request.AudioInput.Path, "input.raw"; got != want {
		t.Fatalf("audio input path = %q, want %q", got, want)
	}
	if got, want := request.AudioInput.SourceSampleRate, 24000; got != want {
		t.Fatalf("audio input sample rate = %d, want %d", got, want)
	}
	if !request.AudioInput.CloseStdinOnCancel || !request.AudioInput.Present || request.AudioInput.DevicePresent {
		t.Fatalf("audio input presence/close metadata = %#v, want caller values", request.AudioInput)
	}
	if !request.PromptProvided || request.Prompt != "" || !request.TextSeed.Present || request.TextSeed.Value != "" {
		t.Fatalf("explicit empty prompt metadata was not preserved: %#v", request)
	}
	if !reflect.DeepEqual(request.AudioTurns, []string{"turn-1", "turn-2"}) || !reflect.DeepEqual(request.AudioInterrupts, []string{"interrupt-1"}) {
		t.Fatalf("audio turn metadata was not preserved: turns=%v interrupts=%v", request.AudioTurns, request.AudioInterrupts)
	}
	if request.AudioInterruptTool != "lookup" || request.AudioInputDevice != "default-input" || request.AudioOutputDevice != "default-output" || !request.AudioInputDevicePresent || !request.AudioOutputDevicePresent {
		t.Fatalf("request device/tool metadata was not preserved: %#v", request)
	}
}

func TestC156CancellationIntentIsNilSafeAndMonotonic(t *testing.T) {
	var nilIntent *SessionCancellationIntent
	nilIntent.MarkSIGINT()
	if nilIntent.SIGINTReceived() {
		t.Fatal("nil cancellation intent reported SIGINT")
	}

	intent := NewSessionCancellationIntent()
	if intent.SIGINTReceived() {
		t.Fatal("new cancellation intent reported SIGINT")
	}

	const readers = 8
	const observations = 10_000
	start := make(chan struct{})
	var ready sync.WaitGroup
	var readersDone sync.WaitGroup
	ready.Add(readers)
	readersDone.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer readersDone.Done()
			<-start
			ready.Done()
			for j := 0; j < observations; j++ {
				_ = intent.SIGINTReceived()
			}
		}()
	}
	close(start)
	ready.Wait()
	for i := 0; i < observations; i++ {
		intent.MarkSIGINT()
	}
	readersDone.Wait()

	if !intent.SIGINTReceived() {
		t.Fatal("cancellation intent did not retain SIGINT")
	}
	intent.MarkSIGINT()
	if !intent.SIGINTReceived() {
		t.Fatal("cancellation intent reset after repeated SIGINT")
	}
}

func TestC156DiagnosticCallbacksPreservePublicRecords(t *testing.T) {
	var nilSessionDiagnostic SessionDiagnosticFunc
	nilSessionDiagnostic.RecordSessionDiagnostic(SessionDiagnosticRecord{Event: SessionDiagnosticEventFailure})

	fields := map[string]string{
		SessionDiagnosticFieldCancelledBy:        "sigint",
		SessionDiagnosticFieldCompletedTurnCount: "2",
	}
	var sessionRecords []SessionDiagnosticRecord
	sessionDiagnostic := SessionDiagnosticFunc(func(record SessionDiagnosticRecord) {
		sessionRecords = append(sessionRecords, record)
	})
	record := SessionDiagnosticRecord{Event: SessionDiagnosticEventTerminal, Fields: fields}
	sessionDiagnostic.RecordSessionDiagnostic(record)
	if len(sessionRecords) != 1 {
		t.Fatalf("session diagnostic records = %d, want exactly one", len(sessionRecords))
	}
	if got := sessionRecords[0]; got.Event != record.Event || !reflect.DeepEqual(got.Fields, record.Fields) {
		t.Fatalf("session diagnostic record = %#v, want %#v", got, record)
	}

	var nilToolDiagnostic SessionToolDiagnosticFunc
	nilToolDiagnostic.RecordSessionToolDiagnostic(SessionToolDiagnostic{ToolCallID: "ignored"})

	originalErr := errors.New("tool result rejected")
	var toolRecords []SessionToolDiagnostic
	toolDiagnostic := SessionToolDiagnosticFunc(func(diagnostic SessionToolDiagnostic) {
		toolRecords = append(toolRecords, diagnostic)
	})
	diagnostic := SessionToolDiagnostic{
		ToolCallID: "call-42",
		ToolName:   "lookup",
		Source:     "provider",
		ErrorCode:  "buffer_full",
		Error:      originalErr,
	}
	toolDiagnostic.RecordSessionToolDiagnostic(diagnostic)
	if len(toolRecords) != 1 {
		t.Fatalf("tool diagnostic records = %d, want exactly one", len(toolRecords))
	}
	if got := toolRecords[0]; got.ToolCallID != diagnostic.ToolCallID || got.ToolName != diagnostic.ToolName || got.Source != diagnostic.Source || got.ErrorCode != diagnostic.ErrorCode || !errors.Is(got.Error, originalErr) {
		t.Fatalf("tool diagnostic record = %#v, want %#v", got, diagnostic)
	}
}

func TestC156UnresolvedToolResultsPreserveDeterministicOutcome(t *testing.T) {
	statuses := map[string]messages.SessionSendStatus{
		"call-z":        messages.SessionSendCancelled,
		"call-a":        "",
		"call-unlisted": messages.SessionSendClosed,
	}
	err := NewSessionUnresolvedToolResultsError(
		[]string{" call-z ", "", "call-a", "call-z", "call-a"},
		statuses,
	)
	if !errors.Is(err, ErrSessionUnresolvedToolResults) {
		t.Fatalf("unresolved tool error = %v, want stable sentinel", err)
	}
	var unresolvedErr *SessionUnresolvedToolResultsError
	if !errors.As(err, &unresolvedErr) {
		t.Fatalf("unresolved tool error = %T, want *SessionUnresolvedToolResultsError", err)
	}
	wantIDs := []string{"call-a", "call-z"}
	if got := unresolvedErr.UnresolvedCallIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unresolved call IDs = %v, want %v", got, wantIDs)
	}
	copyOfIDs := unresolvedErr.UnresolvedCallIDs()
	copyOfIDs[0] = c156MutationValue
	if got := unresolvedErr.UnresolvedCallIDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("unresolved call IDs changed after caller mutation: %v", got)
	}
	if len(unresolvedErr.SendStatuses) != 2 || unresolvedErr.SendStatuses["call-z"] != messages.SessionSendCancelled || unresolvedErr.SendStatuses["call-a"] != "" {
		t.Fatalf("retained send statuses = %#v, want only ordered IDs", unresolvedErr.SendStatuses)
	}
	if got, want := unresolvedErr.Error(), "tool results were not delivered for 2 unresolved call(s): call-a, call-z (send outcomes: call-z=cancelled)"; got != want {
		t.Fatalf("unresolved tool error text = %q, want %q", got, want)
	}
	if !errors.Is(unresolvedErr, ErrSessionUnresolvedToolResults) {
		t.Fatalf("unresolved tool error = %v, want stable sentinel", unresolvedErr)
	}

	if got := NewSessionUnresolvedToolResultsError(nil, nil).Error(); got != ErrSessionUnresolvedToolResults.Error() {
		t.Fatalf("empty unresolved tool error text = %q, want sentinel text", got)
	}
	var nilUnresolvedErr *SessionUnresolvedToolResultsError
	if got := nilUnresolvedErr.Error(); got != ErrSessionUnresolvedToolResults.Error() {
		t.Fatalf("nil unresolved tool error text = %q, want sentinel text", got)
	}
	if got := nilUnresolvedErr.UnresolvedCallIDs(); got != nil {
		t.Fatalf("nil unresolved call IDs = %v, want nil", got)
	}
}

func TestC156VoiceContractIsOrderedAndCopyIsolated(t *testing.T) {
	wantVoices := []string{"alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse"}
	voices := SupportedOpenAIRealtimeVoices()
	if !reflect.DeepEqual(voices, wantVoices) {
		t.Fatalf("supported voices = %v, want %v", voices, wantVoices)
	}
	voices[0] = c156MutationValue
	if got := SupportedOpenAIRealtimeVoices(); !reflect.DeepEqual(got, wantVoices) {
		t.Fatalf("supported voice registry changed after caller mutation: %v", got)
	}

	for _, voice := range append([]string{""}, wantVoices...) {
		if err := ValidateOpenAIRealtimeVoice(voice); err != nil {
			t.Fatalf("ValidateOpenAIRealtimeVoice(%q) = %v, want nil", voice, err)
		}
	}
	for _, voice := range []string{"Alloy", " alloy ", "not-a-voice"} {
		assertC156InvalidVoice(t, voice, wantVoices)
	}

	var nilVoiceErr *InvalidOpenAIRealtimeVoiceError
	if got := nilVoiceErr.Error(); got != ErrInvalidOpenAIRealtimeVoice.Error() {
		t.Fatalf("nil voice error text = %q, want sentinel text", got)
	}
	if got := nilVoiceErr.Unwrap(); got != nil {
		t.Fatalf("nil voice unwrap = %v, want nil", got)
	}
}

func assertC156InvalidVoice(t *testing.T, voice string, wantVoices []string) {
	t.Helper()

	err := ValidateOpenAIRealtimeVoice(voice)
	var voiceErr *InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &voiceErr) {
		t.Fatalf("invalid voice error = %T, want *InvalidOpenAIRealtimeVoiceError", err)
	}
	if !errors.Is(err, ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("invalid voice error = %v, want stable sentinel", err)
	}
	if voiceErr.Voice != voice {
		t.Fatalf("invalid voice value = %q, want %q", voiceErr.Voice, voice)
	}
	if !reflect.DeepEqual(voiceErr.SupportedVoices, wantVoices) {
		t.Fatalf("invalid voice supported values = %v, want %v", voiceErr.SupportedVoices, wantVoices)
	}
	wantMessage := `invalid OpenAI Realtime voice "` + voice + `"; supported voices: ` + strings.Join(wantVoices, ", ")
	if got := voiceErr.Error(); got != wantMessage {
		t.Fatalf("invalid voice error text = %q, want %q", got, wantMessage)
	}
	voiceErr.SupportedVoices[0] = c156MutationValue
	if got := SupportedOpenAIRealtimeVoices(); !reflect.DeepEqual(got, wantVoices) {
		t.Fatalf("voice registry changed through typed error: %v", got)
	}
	if !errors.Is(voiceErr.Unwrap(), ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("invalid voice unwrap = %v, want stable sentinel", voiceErr.Unwrap())
	}
}
