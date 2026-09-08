package deviceprobe

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestProbeBridgeReportsTerminalAndMalformedEvents(t *testing.T) {
	tests := []struct {
		name    string
		message messages.StreamMessage
		want    string
	}{
		{name: "provider error", message: messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("wire failed")}, want: "wire failed"},
		{name: "bad transcript", message: messages.StreamMessage{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptDeltaValue("wrong value")}, want: "transcript end value"},
		{name: "closed session", message: messages.StreamMessage{Type: messages.StreamTypeSessionClose}, want: "session closed before response completion"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := messages.NewTypedBuffer[messages.StreamMessage](4)
			runner := &participants.ModelRunner{DeltaOutbox: out}
			bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { bridge.Run(ctx); close(done) }()
			if !out.Write(ctx, tc.message) {
				t.Fatal("write bridge event")
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatalf("bridge did not stop: %v", ctx.Err())
			}
			if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte(tc.want)) {
				t.Fatalf("bridge error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProbeBridgeIgnoresNonTerminalDiagnostics(t *testing.T) {
	out := messages.NewTypedBuffer[messages.StreamMessage](4)
	runner := &participants.ModelRunner{DeltaOutbox: out}
	bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { bridge.Run(ctx); close(done) }()
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("diagnostic", "info")}) {
		t.Fatal("write non-terminal diagnostic")
	}
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose}) {
		t.Fatal("write session close")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("bridge did not stop: %v", ctx.Err())
	}
	if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("session closed")) {
		t.Fatalf("bridge error = %v, want close after diagnostic", err)
	}
}

func TestProbeBridgeLifecycleAndAudioValidation(t *testing.T) {
	out := messages.NewTypedBuffer[messages.StreamMessage](8)
	runner := &participants.ModelRunner{DeltaOutbox: out}
	bridge := newLiveDeviceProbeSessionBridge(runner, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { bridge.Run(ctx); close(done) }()
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen}) {
		t.Fatal("write session open")
	}
	if err := bridge.waitOpened(ctx); err != nil {
		t.Fatalf("waitOpened = %v, want success", err)
	}
	if !out.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValueWithMediaType([]byte{1, 2}, "audio/opus")}) {
		t.Fatal("write unsupported audio")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after unsupported audio")
	}
	if err := bridge.errorValue(nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("not PCM16")) {
		t.Fatalf("bridge audio error = %v, want PCM format diagnostic", err)
	}
	if err := bridge.waitResponse(context.Background()); err == nil {
		t.Fatal("waitResponse succeeded after bridge error")
	}
	if got := liveDeviceProbeSessionError(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("boom")}); got == nil || !bytes.Contains([]byte(got.Error()), []byte("boom")) {
		t.Fatalf("session error = %v, want provider message", got)
	}
	if got := liveDeviceProbeSessionError(messages.StreamMessage{Type: messages.StreamTypeError}); got == nil {
		t.Fatal("nil error payload produced nil diagnostic")
	}
}

func TestProbeHelperContracts(t *testing.T) {
	bridge := newLiveDeviceProbeSessionBridge(&participants.ModelRunner{DeltaOutbox: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bridge.waitOpened(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitOpened cancelled = %v, want context.Canceled", err)
	}
	if err := bridge.waitResponse(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitResponse cancelled = %v, want context.Canceled", err)
	}
	bridge.setError(errors.New("first"))
	bridge.setError(errors.New("second"))
	if got := bridge.errorValue(nil); got == nil || got.Error() != "first" {
		t.Fatalf("errorValue = %v, want first error", got)
	}
	if got := selectLiveDeviceProbeDevice(nil, []devicegw.Device{{ID: "fallback"}}, devicegw.DirectionInput); got.ID != "fallback" {
		t.Fatalf("fallback device = %q, want fallback", got.ID)
	}
	if err := closeDeviceProbeResource("test", func() error { return errors.New("close failed") }); err == nil || !bytes.Contains([]byte(err.Error()), []byte("close test")) {
		t.Fatalf("close resource error = %v, want resource context", err)
	}
	if got := scenarioDeviceProbeTranscript(probe.Scenario{Expectations: []probe.ExpectedBehavior{{Type: probe.ExpectTranscriptContains, Value: "from value"}}}); got != "from value" {
		t.Fatalf("transcript expectation fallback = %q, want from value", got)
	}
	if _, err := runDeviceProbeScenario(context.Background(), validProbeScenario(), devicegw.DeviceProbeAvailability{}, nil, runtimeDevices.ProbeRequest{}, nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("status")) {
		t.Fatalf("invalid availability error = %v, want status diagnostic", err)
	}
}

func validProbeScenario() probe.Scenario {
	return probe.Scenario{Steps: []probe.Step{{Type: probe.StepSendAudio, CorpusID: "probe-corpus", Text: "speak"}}}
}
