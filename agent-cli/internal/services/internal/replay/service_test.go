package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	publicreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/recording"
)

func TestServicePrepareAndRecordedToolExactOnce(t *testing.T) {
	directory := writeBundle(t, true)
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), 10)
	prepared, err := New(Dependencies{ClockFactory: func(time.Time) *clock.Deterministic { return scheduler }}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory, Provider: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WireEvents != 3 || prepared.ToolCalls != 1 || prepared.Dialer == nil || prepared.Audio == nil {
		t.Fatalf("prepared=%+v", prepared)
	}
	conn, err := prepared.Dialer.Dial("offline", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range prepared.Capture.Records {
		if event.Direction == "client_to_server" {
			if err := conn.WriteMessage(1, event.Payload); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	call := messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`}
	response, err := prepared.ToolExecutor.Execute(context.Background(), call)
	if err != nil || response.ToolCallID != call.ID || response.Content != "answer" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := prepared.ToolExecutor.Execute(context.Background(), call); !errors.Is(err, publicreplay.ErrToolMismatch) {
		t.Fatalf("duplicate execution err=%v", err)
	}
	if err := prepared.ValidateComplete(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsMissingToolResult(t *testing.T) {
	directory := writeBundle(t, false)
	_, err := New(Dependencies{ClockFactory: func(time.Time) *clock.Deterministic { return clock.NewDeterministic(time.Unix(0, 0).UTC(), 10) }}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory})
	if !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("err=%v, want incomplete", err)
	}
}

func TestServiceAcceptsCanonicalRecordDirectory(t *testing.T) {
	direct := writeBundle(t, true)
	root := t.TempDir()
	if err := os.Rename(direct, filepath.Join(root, "audio-trace")); err != nil {
		t.Fatal(err)
	}
	prepared, err := New(Dependencies{ClockFactory: func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, 10)
	}}).Prepare(context.Background(), publicreplay.Request{BundlePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Audio == nil {
		t.Fatal("canonical record directory did not prepare audio replay")
	}
}

func TestServiceCreatesIndependentClocksPerPreparation(t *testing.T) {
	directory := writeBundle(t, true)
	service := New(Dependencies{ClockFactory: func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, time.Millisecond)
	}})
	first, err := service.Prepare(context.Background(), publicreplay.Request{BundlePath: directory})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Prepare(context.Background(), publicreplay.Request{BundlePath: directory})
	if err != nil {
		t.Fatal(err)
	}
	if first.Clock == second.Clock {
		t.Fatal("preparations share mutable deterministic clock")
	}
	first.Audio.Clock.AdvanceBy(time.Second)
	if got := second.Clock.Now(); got != second.Audio.Clock.Now() {
		t.Fatalf("second clock changed with first: %v", got)
	}
}

func TestServiceRejectsHostClock(t *testing.T) {
	directory := writeBundle(t, true)
	_, err := New(Dependencies{ClockFactory: nil}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory})
	if !errors.Is(err, publicreplay.ErrDeterministicClockRequired) {
		t.Fatalf("err=%v, want deterministic clock error", err)
	}
}

func TestServiceRejectsHandshakeModelMismatch(t *testing.T) {
	directory := writeBundle(t, true)
	_, err := New(Dependencies{ClockFactory: func(time.Time) *clock.Deterministic { return clock.NewDeterministic(time.Unix(0, 0).UTC(), 10) }}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory, Model: "different-model"})
	if !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("err=%v, want bundle mismatch", err)
	}
}

func TestServiceVerifiesRequestedModelFromSessionCreated(t *testing.T) {
	events := []recording.Event{
		{Kind: "runtime", RuntimeKind: providerWireSend, Payload: wireEnvelope(t, `{"type":"session.update","session":{}}`), Clean: true},
		{Kind: "runtime", RuntimeKind: providerWireReceive, Payload: wireEnvelope(t, `{"type":"session.created","session":{"model":"gpt-test"}}`), Clean: true},
		{Kind: "runtime", RuntimeKind: providerWireReceive, Payload: wireEnvelope(t, `{"type":"response.done"}`), Clean: true},
	}
	capture, _, _, _, _, err := deriveEvidence(events, publicreplay.Request{Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	if capture.Provider.Model != "gpt-test" {
		t.Fatalf("captured model = %q, want session.created model", capture.Provider.Model)
	}
}

func TestServiceRejectsRequestedModelWithoutCapturedModel(t *testing.T) {
	events := []recording.Event{
		{Kind: "runtime", RuntimeKind: providerWireSend, Payload: wireEnvelope(t, `{"type":"session.update","session":{}}`), Clean: true},
		{Kind: "runtime", RuntimeKind: providerWireReceive, Payload: wireEnvelope(t, `{"type":"session.created","session":{}}`), Clean: true},
		{Kind: "runtime", RuntimeKind: providerWireReceive, Payload: wireEnvelope(t, `{"type":"response.done"}`), Clean: true},
	}
	_, _, _, _, _, err := deriveEvidence(events, publicreplay.Request{Model: "gpt-test"})
	if !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("err=%v, want incomplete", err)
	}
}

func TestOpenAIRuntimeFactoryRejectsUnsupportedProvider(t *testing.T) {
	directory := writeCoreToolBundle(t)
	service := New(Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic {
			return clock.NewDeterministic(origin, time.Millisecond)
		},
		Runtime: NewOpenAIRuntimeFactory(),
	})
	_, err := service.Run(context.Background(), io.Discard, publicreplay.Request{BundlePath: directory, Provider: "grok", Model: "gpt-test"})
	if !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("err=%v, want provider mismatch", err)
	}
}

func TestRecordedToolRejectsMismatchedRequest(t *testing.T) {
	directory := writeBundle(t, true)
	prepared, err := New(Dependencies{ClockFactory: func(time.Time) *clock.Deterministic { return clock.NewDeterministic(time.Unix(0, 0).UTC(), 10) }}).Prepare(context.Background(), publicreplay.Request{BundlePath: directory})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepared.ToolExecutor.Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "other", Arguments: `{}`})
	if !errors.Is(err, publicreplay.ErrToolMismatch) {
		t.Fatalf("err=%v, want tool mismatch", err)
	}
	if err := prepared.Close(); !errors.Is(err, publicreplay.ErrBundleIncomplete) {
		t.Fatalf("close err=%v, want incomplete after rejected call", err)
	}
}

type runtimeFactoryFunc func(publicreplay.Prepared) (publicreplay.Runtime, error)

func (f runtimeFactoryFunc) New(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
	return f(prepared)
}

type replayRuntimeFunc func(context.Context, io.Writer) error

func (f replayRuntimeFunc) Run(ctx context.Context, out io.Writer) error { return f(ctx, out) }

func TestServiceRunInvokesHeadlessRuntimeAndStrictCompletion(t *testing.T) {
	directory := writeBundle(t, true)
	var invoked bool
	service := New(Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic { return clock.NewDeterministic(origin, time.Millisecond) },
		Runtime: runtimeFactoryFunc(func(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
			return replayRuntimeFunc(func(ctx context.Context, out io.Writer) error {
				invoked = true
				conn, err := prepared.Dialer.Dial("offline", nil)
				if err != nil {
					return err
				}
				for _, event := range prepared.Capture.Records {
					if event.Direction == "client_to_server" {
						if err := conn.WriteMessage(1, event.Payload); err != nil {
							return err
						}
					} else if _, _, err := conn.ReadMessage(); err != nil {
						return err
					}
				}
				if _, err := prepared.ToolExecutor.Execute(ctx, messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`}); err != nil {
					return err
				}
				_, err = io.WriteString(out, "replayed")
				return err
			}), nil
		}),
	})
	var out bytes.Buffer
	result, err := service.Run(context.Background(), &out, publicreplay.Request{BundlePath: directory})
	if err != nil {
		t.Fatal(err)
	}
	if !invoked || out.String() != "replayed" || result.WireEvents != 3 || result.ToolCalls != 1 {
		t.Fatalf("invoked=%v out=%q result=%+v", invoked, out.String(), result)
	}
}

func TestServiceRunRejectsChangedOutbound(t *testing.T) {
	directory := writeBundle(t, true)
	service := New(Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic { return clock.NewDeterministic(origin, time.Millisecond) },
		Runtime: runtimeFactoryFunc(func(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
			return replayRuntimeFunc(func(context.Context, io.Writer) error {
				conn, err := prepared.Dialer.Dial("offline", nil)
				if err != nil {
					return err
				}
				if err := conn.WriteMessage(1, []byte(`{"type":"session.update","session":{"model":"changed"}}`)); err == nil {
					return errors.New("changed outbound was unexpectedly accepted")
				} else {
					return err
				}
			}), nil
		}),
	})
	_, err := service.Run(context.Background(), io.Discard, publicreplay.Request{BundlePath: directory})
	if !errors.Is(err, publicreplay.ErrBundleMismatch) {
		t.Fatalf("err=%v, want outbound mismatch", err)
	}
}

func TestServiceRunMakesRecordedPCMAvailableToHeadlessRuntime(t *testing.T) {
	directory := writeAudioBundle(t)
	var gotSamples int
	service := New(Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic {
			return clock.NewDeterministic(origin, time.Millisecond)
		},
		Runtime: runtimeFactoryFunc(func(prepared publicreplay.Prepared) (publicreplay.Runtime, error) {
			return replayRuntimeFunc(func(ctx context.Context, _ io.Writer) error {
				for {
					_, frame, err := prepared.Audio.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return err
					}
					if frame != nil {
						gotSamples += len(frame.Samples)
					}
				}
				conn, err := prepared.Dialer.Dial("offline", nil)
				if err != nil {
					return err
				}
				for _, event := range prepared.Capture.Records {
					if event.Direction == "client_to_server" {
						if err := conn.WriteMessage(1, event.Payload); err != nil {
							return err
						}
					} else if _, _, err := conn.ReadMessage(); err != nil {
						return err
					}
				}
				_, err = prepared.ToolExecutor.Execute(ctx, messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`})
				return err
			}), nil
		}),
	})
	result, err := service.Run(context.Background(), io.Discard, publicreplay.Request{BundlePath: directory})
	if err != nil {
		t.Fatal(err)
	}
	if gotSamples != 3 {
		t.Fatalf("replayed samples=%d, want 3", gotSamples)
	}
	if !result.Scope.Protocol || !result.Scope.Tools || !result.Scope.RecordedPCM || !result.Scope.RenderTapUnavailable || result.Scope.DeviceExecution {
		t.Fatalf("inaccurate replay scope: %+v", result.Scope)
	}
}

func TestOpenAIRuntimeFactoryRunsCoreAgainstPreparedReplay(t *testing.T) {
	directory := writeCoreToolBundle(t)
	service := New(Dependencies{
		ClockFactory: func(origin time.Time) *clock.Deterministic {
			return clock.NewDeterministic(origin, time.Millisecond)
		},
		Runtime: NewOpenAIRuntimeFactory(),
	})
	var output bytes.Buffer
	if _, err := service.Run(context.Background(), &output, publicreplay.Request{BundlePath: directory, Provider: "openai", Model: "gpt-test"}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "offline answer" {
		t.Fatalf("output=%q, want core response", output.String())
	}
}

func writeBundle(t *testing.T, includeResult bool) string {
	t.Helper()
	directory := t.TempDir()
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), 10)
	trace, err := recording.NewTrace(directory, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	observeWire(t, trace, "provider_wire_receive", `{"type":"session.created"}`)
	observeWire(t, trace, "provider_wire_send", `{"type":"session.update","session":{"model":"gpt-test"}}`)
	call := messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`}
	callPayload, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Payload: callPayload, Clean: true})
	if includeResult {
		resultPayload, err := json.Marshal(struct {
			CallID   string                    `json:"call_id"`
			Name     string                    `json:"name"`
			Response messages.ToolCallResponse `json:"response"`
			Failed   bool                      `json:"failed"`
		}{call.ID, call.Name, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "answer"}, false})
		if err != nil {
			t.Fatal(err)
		}
		trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Payload: resultPayload, Clean: true})
	}
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.done"}`)
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeAudioBundle(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), 10)
	trace, err := recording.NewTrace(directory, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	trace.CaptureMicrophonePreGate(16000, []int16{1, 2, 3})
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "audio_render_tap_unavailable", Clean: false, Error: "render callback unavailable"})
	observeWire(t, trace, "provider_wire_send", `{"type":"session.update","session":{"model":"gpt-test"}}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.done"}`)
	call := messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`}
	callPayload, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Payload: callPayload, Clean: true})
	resultPayload, err := json.Marshal(struct {
		CallID   string                    `json:"call_id"`
		Name     string                    `json:"name"`
		Response messages.ToolCallResponse `json:"response"`
		Failed   bool                      `json:"failed"`
	}{call.ID, call.Name, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "answer"}, false})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Payload: resultPayload, Clean: true})
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func writeCoreToolBundle(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	scheduler := clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	trace, err := recording.NewTrace(directory, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	observeWire(t, trace, "provider_wire_send", `{"type":"session.update","session":{"model":"gpt-test"}}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"session.created","session":{"id":"session-1","model":"gpt-test"}}`)
	observeWire(t, trace, "provider_wire_send", `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`)
	observeWire(t, trace, "provider_wire_send", `{"type":"response.create"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.created","response":{"id":"response-1"}}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.output_item.added","response_id":"response-1","item":{"type":"function_call","call_id":"call-1","name":"lookup"}}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.function_call_arguments.delta","response_id":"response-1","call_id":"call-1","delta":"{\"q\":"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.function_call_arguments.done","response_id":"response-1","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"value\"}"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.done","response":{"id":"response-1","status":"completed"}}`)
	observeWire(t, trace, "provider_wire_send", `{"type":"conversation.item.create","item":{"type":"function_call_output","call_id":"call-1","output":"answer"}}`)
	observeWire(t, trace, "provider_wire_send", `{"type":"response.create"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.created","response":{"id":"response-2"}}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.output_text.delta","response_id":"response-2","delta":"offline answer"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.output_text.done","response_id":"response-2"}`)
	observeWire(t, trace, "provider_wire_receive", `{"type":"response.done","response":{"id":"response-2","status":"completed"}}`)
	callPayload, err := json.Marshal(messages.ToolCall{ID: "call-1", Name: "lookup", Arguments: `{"q":"value"}`})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_call", Payload: callPayload, Clean: true})
	resultPayload, err := json.Marshal(struct {
		CallID   string                    `json:"call_id"`
		Name     string                    `json:"name"`
		Response messages.ToolCallResponse `json:"response"`
		Failed   bool                      `json:"failed"`
	}{"call-1", "lookup", messages.ToolCallResponse{ToolCallID: "call-1", Name: "lookup", Content: "answer"}, false})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: "tool_result", Payload: resultPayload, Clean: true})
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

func observeWire(t *testing.T, trace *recording.Trace, kind, payload string) {
	t.Helper()
	envelope, err := json.Marshal(struct {
		MessageType int             `json:"message_type"`
		Payload     json.RawMessage `json:"payload"`
	}{1, json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(recording.RuntimeEvent{Kind: kind, Payload: envelope, Clean: true})
}

func wireEnvelope(t *testing.T, payload string) []byte {
	t.Helper()
	envelope, err := json.Marshal(struct {
		MessageType int             `json:"message_type"`
		Payload     json.RawMessage `json:"payload"`
	}{1, json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
