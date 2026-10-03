package live

import (
	"context"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/stretchr/testify/require"
)

// A completion-gated scheduled turn follows the prior response; it is the
// next turn, not a barge-in. It reached the loop as interrupting audio, so
// when the prior reply's audio was still queued for playback (audio arrives
// faster than real time) the loop's local barge-in discarded that reply's
// unplayed audio and the recording held no reply audio for the turn. Only
// explicit barge admission may interrupt.
func TestScheduledTurnAudioInterruptsOnlyUnderBargeAdmission(t *testing.T) {
	cases := []struct {
		admission session.AudioTurnAdmission
		want      messages.SessionAudioInputPolicy
	}{
		{admission: "", want: messages.SessionAudioInputPolicyDoNotInterrupt},
		{admission: session.AudioTurnAdmissionCompletionGated, want: messages.SessionAudioInputPolicyDoNotInterrupt},
		{admission: session.AudioTurnAdmissionBarge, want: messages.SessionAudioInputPolicyInterrupt},
	}
	for _, tc := range cases {
		t.Run(string(tc.admission), func(t *testing.T) {
			handle := &policyRecordingHandle{}
			invocation := &liveInvocation{
				handle:    handle,
				endpoints: sharedaudio.MediaEndpoints{Outbound: discardOutbound{}},
				options: session.LiveRunOptions{
					Devices:            oneFrameCaptureDevices{},
					AudioTurnAdmission: tc.admission,
					CaptureTurns:       []devices.FileInput{{}, {}},
				},
			}
			require.NoError(t, invocation.runCaptureTurns(context.Background()))
			require.Equal(t, []messages.SessionAudioInputPolicy{tc.want, tc.want}, handle.policies())
		})
	}
}

// policyRecordingHandle records the barge-in intent of each admitted audio
// input. The embedded handle is nil: the scheduled-turn path must use only
// the audio sender and controls.
type policyRecordingHandle struct {
	session.LiveHandle
	mu       sync.Mutex
	admitted []messages.SessionAudioInputPolicy
}

func (h *policyRecordingHandle) sendAudioInput(_ context.Context, _ []byte, policy messages.SessionAudioInputPolicy) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.admitted = append(h.admitted, policy)
	return nil
}

func (h *policyRecordingHandle) Send(context.Context, session.LiveControl) error { return nil }

func (h *policyRecordingHandle) policies() []messages.SessionAudioInputPolicy {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]messages.SessionAudioInputPolicy(nil), h.admitted...)
}

type discardOutbound struct{}

func (discardOutbound) WriteFrame(context.Context, sharedaudio.PCMFrame) error { return nil }
func (discardOutbound) Close() error                                           { return nil }

// oneFrameCaptureDevices opens a finite capture that speaks one frame per turn.
type oneFrameCaptureDevices struct{}

func (oneFrameCaptureDevices) Open(context.Context, devices.Request) (devices.Handle, error) {
	return oneFrameCaptureHandle{}, nil
}

type oneFrameCaptureHandle struct{}

func (oneFrameCaptureHandle) Media() devices.MediaPorts {
	return devices.MediaPorts{Capture: oneFrameCapture{}}
}
func (oneFrameCaptureHandle) Close() error { return nil }

type oneFrameCapture struct{}

func (oneFrameCapture) Pump(ctx context.Context, target sharedaudio.OutboundMedia) error {
	frame := sharedaudio.PCMFrame{Samples: []int16{1200, -1200, 1200, -1200}, Format: sharedaudio.PCM16DeviceFormat(16000)}
	return target.WriteFrame(ctx, frame)
}
func (oneFrameCapture) Close() error { return nil }
