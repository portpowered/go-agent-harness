package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionobservation"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []sessionobservation.SessionRuntimeObservation
}

func (o *recordingObserver) ObserveSessionRuntime(event sessionobservation.SessionRuntimeObservation) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *recordingObserver) snapshot() []sessionobservation.SessionRuntimeObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]sessionobservation.SessionRuntimeObservation(nil), o.events...)
}

type optedInObserver struct{ recordingObserver }

func (optedInObserver) ObserveProviderBoundaries() bool { return true }
func (optedInObserver) RetainCommitPayload() bool       { return false }

func TestServicePreservesBoundariesIdentityAndPayloadCopies(t *testing.T) {
	source := platformclock.NewDeterministic(time.Unix(1700000000, 0).UTC(), time.Second)
	observer := &recordingObserver{}
	runtime := New(observer, source)

	input := []byte{1, 2, 3}
	runtime.AudioInput(input)
	input[0] = 9
	source.Advance()
	output := []byte{4, 5, 6}
	message := messages.StreamMessage{
		ResponseID:      "response-7",
		ResponsePurpose: messages.ResponsePurposeToolAcknowledgement,
		ActorStreamID:   "stream-2",
		LoopPassID:      12,
	}
	runtime.AudioOutputMessage(output, message)
	output[0] = 8
	runtime.ResponseCreate(message)

	events := observer.snapshot()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Kind != sessionobservation.SessionRuntimeObservationAudioInput || !reflect.DeepEqual(events[0].Payload, []byte{1, 2, 3}) {
		t.Fatalf("audio input = %#v", events[0])
	}
	audio := events[1]
	if audio.Kind != sessionobservation.SessionRuntimeObservationAudioOutput || audio.Tick != 1 || !audio.Timestamp.Equal(source.Now()) || !reflect.DeepEqual(audio.Payload, []byte{4, 5, 6}) {
		t.Fatalf("audio output = %#v", audio)
	}
	if audio.ResponseID != message.ResponseID || audio.StreamID != message.ActorStreamID || audio.LoopPassID != message.LoopPassID || audio.Epoch != 0 {
		t.Fatalf("audio identity = %#v", audio)
	}
	if events[2].Kind != sessionobservation.SessionRuntimeObservationResponseCreate || events[2].ResponsePurpose != message.ResponsePurpose {
		t.Fatalf("response create = %#v", events[2])
	}
}

func TestServiceCommitCaptureResetsAndHonorsProviderPreferences(t *testing.T) {
	observer := &recordingObserver{}
	runtime := New(observer, platformclock.Real{})
	runtime.ProviderAudioSent([]byte("first"))
	runtime.InputCommit()
	runtime.ProviderAudioSent([]byte("second"))
	runtime.InputCommit()
	runtime.ProviderAudioSent([]byte("provider"))
	runtime.ProviderInputCommit()

	events := observer.snapshot()
	if len(events) != 3 {
		t.Fatalf("commit events = %d, want 3", len(events))
	}
	for index, want := range []struct {
		payload string
		ordinal int
	}{{"first", 1}, {"second", 2}, {"provider", 0}} {
		if events[index].Kind != sessionobservation.SessionRuntimeObservationInputCommit || string(events[index].Payload) != want.payload || events[index].InputCommit != want.ordinal {
			t.Fatalf("commit %d = %#v, want payload=%q ordinal=%d", index, events[index], want.payload, want.ordinal)
		}
	}

	optedIn := &optedInObserver{}
	providerRuntime := New(optedIn, platformclock.Real{})
	if !providerRuntime.ProviderBoundaryObservationsEnabled() {
		t.Fatal("provider boundary preference was not retained")
	}
	providerRuntime.ProviderAudioSent([]byte("not-retained"))
	providerRuntime.ProviderInputCommit()
	providerEvents := optedIn.snapshot()
	if len(providerEvents) != 1 || len(providerEvents[0].Payload) != 0 || providerEvents[0].InputCommit != 0 {
		t.Fatalf("provider commit = %#v, want empty payload and zero ordinal", providerEvents)
	}
	providerRuntime.EnableProviderBoundaryObservations()
	if !providerRuntime.ProviderBoundaryObservationsEnabled() {
		t.Fatal("explicit provider boundary opt-in was not enabled")
	}
}

func TestServiceCommitCaptureIsBounded(t *testing.T) {
	observer := &recordingObserver{}
	runtime := New(observer, platformclock.Real{})
	runtime.ProviderAudioSent(make([]byte, maxCommitPayloadBytes+17))
	runtime.ProviderAudioSent([]byte("discarded-after-bound"))
	if got := len(runtime.inputPayload); got != maxCommitPayloadBytes {
		t.Fatalf("retained input bytes = %d, want cap %d", got, maxCommitPayloadBytes)
	}
	runtime.InputCommit()
	runtime.ProviderAudioSent([]byte("fresh"))
	runtime.InputCommit()
	events := observer.snapshot()
	if len(events) != 2 || len(events[0].Payload) != maxCommitPayloadBytes || string(events[1].Payload) != "fresh" {
		t.Fatalf("bounded/reset commits = first=%d second=%q", len(events[0].Payload), events[1].Payload)
	}
}

func TestServicePlaybackToolRedactionAndIdentity(t *testing.T) {
	observer := &recordingObserver{}
	runtime := New(observer, platformclock.Real{})
	runtime.AudioPlaybackReceipt(sessionobservation.PlaybackReceipt{CommandID: 9, Epoch: 4, AudioEndMS: 37, Applied: false, Err: errors.New("stale playback")})
	runtime.AudioPlaybackReceipt(sessionobservation.PlaybackReceipt{CommandID: 10, Epoch: 5, Applied: false, Err: context.DeadlineExceeded})
	call := messages.ToolCall{ID: "call-9", Name: "lookup", Arguments: `{"q":"x"}`}
	runtime.ObserveToolCall(call)
	runtime.ObserveToolResult(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "failed lookup"}, true)

	events := observer.snapshot()
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	var receipt map[string]any
	if err := json.Unmarshal(events[0].Payload, &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt["command_id"] != float64(9) || receipt["epoch"] != float64(4) || receipt["audio_end_ms"] != float64(37) || receipt["applied"] != false || events[0].Clean {
		t.Fatalf("rejected receipt = %#v", events[0])
	}
	if events[0].Error != "stale playback" || !strings.Contains(string(events[0].Payload), "stale playback") {
		t.Fatalf("receipt error = %#v", events[0])
	}
	if events[1].Error != "" || strings.Contains(string(events[1].Payload), "deadline") {
		t.Fatalf("deadline was not redacted = %#v", events[1])
	}
	if events[2].Kind != "tool_call" || events[3].Kind != "tool_result" || events[3].Clean || !strings.Contains(string(events[3].Payload), call.ID) || !strings.Contains(string(events[3].Payload), "failed lookup") {
		t.Fatalf("tool evidence = %#v", events[2:])
	}
}

func TestServiceTerminalIsExactlyOnceAndDeepCopiesAccounting(t *testing.T) {
	observer := &recordingObserver{}
	runtime := New(observer, platformclock.Real{})
	accounting := &sessionobservation.SessionFinalAccounting{
		PromptTokens:   14,
		TotalTokens:    22,
		UsageSemantics: sessionobservation.SessionTokenUsageIncremental,
		Metrics: metrics.Snapshot{
			HistogramBounds: []int64{10, 20},
			Series:          []metrics.SeriesSnapshot{{Histogram: metrics.HistogramSnapshot{Bounds: []int64{10, 20}, BucketCounts: []uint64{1, 2}}}},
		},
	}
	runtime.TerminalWithAccounting(3, context.Canceled, accounting)
	accounting.Metrics.HistogramBounds[0] = 99
	accounting.Metrics.Series[0].Histogram.BucketCounts[0] = 99
	runtime.TerminalWithAccounting(4, errors.New("late terminal"), nil)

	events := observer.snapshot()
	if len(events) != 1 {
		t.Fatalf("terminal events = %d, want exactly one", len(events))
	}
	terminal := events[0]
	if terminal.Kind != sessionobservation.SessionRuntimeObservationTerminal || terminal.Clean || terminal.Error != "" || terminal.TurnsCompleted != 3 {
		t.Fatalf("terminal = %#v", terminal)
	}
	if terminal.FinalAccounting == nil || terminal.FinalAccounting.Metrics.HistogramBounds[0] != 10 || terminal.FinalAccounting.Metrics.Series[0].Histogram.BucketCounts[0] != 1 {
		t.Fatalf("terminal accounting was aliased = %#v", terminal.FinalAccounting)
	}
}

func TestServiceUsesLocalSequenceWhenClockHasNoTick(t *testing.T) {
	observer := &recordingObserver{}
	runtime := New(observer, fixedClock{now: time.Unix(7, 0)})
	runtime.Observe("one", nil, 0, true, nil)
	runtime.Observe("two", nil, 0, true, nil)
	events := observer.snapshot()
	if events[0].Tick != 1 || events[1].Tick != 2 || !events[0].Timestamp.Equal(events[1].Timestamp) {
		t.Fatalf("local sequence = %#v", events)
	}
}

func TestServiceNilObserverIsInert(t *testing.T) {
	runtime := New(nil, nil)
	runtime.EnableProviderBoundaryObservations()
	runtime.Observe("ignored", []byte("ignored"), 1, false, errors.New("ignored"))
	runtime.AudioInput([]byte("ignored"))
	runtime.AudioOutputMessage([]byte("ignored"), messages.StreamMessage{})
	runtime.AudioPlaybackReceipt(sessionobservation.PlaybackReceipt{})
	runtime.InputCommit()
	runtime.ProviderInputCommit()
	runtime.ResponseCreate(messages.StreamMessage{})
	runtime.TurnCompleted(1)
	runtime.TerminalWithAccounting(1, nil, nil)
	runtime.ObserveToolCall(messages.ToolCall{})
	runtime.ObserveToolResult(messages.ToolCall{}, messages.ToolCallResponse{}, false)
	if runtime.ProviderBoundaryObservationsEnabled() != true {
		t.Fatal("explicit opt-in was not reflected for an inert service")
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
