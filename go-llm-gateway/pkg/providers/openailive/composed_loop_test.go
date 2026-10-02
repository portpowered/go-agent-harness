package openailive_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	live "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/fakelive"
)

// liveInferencer connects the real GPT-Live provider for the agent loop.
type liveInferencer struct {
	provider *live.Provider
	cfg      models.SessionConfig
}

func (i liveInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	return i.provider.ConnectSession(ctx, i.cfg)
}

// loudPCM is 100 ms of loud 24 kHz speech-like audio, well over the loop's
// barge-in onset energy.
func loudPCM() []byte {
	samples := make([]int16, 2400)
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 20000
		} else {
			samples[i] = -20000
		}
	}
	return codec.EncodePCM16(samples)
}

// TestUserBargeInDuringASegmentRaisesNoLocalCancel composes the real duplex
// agent loop with the GPT-Live provider. Loud user audio arrives while the
// assistant speaks; GPT-Live owns turn-taking, so the loop must not cancel
// the segment locally: every output delta reaches the loop's delta stream and
// the segment ends completed.
func TestUserBargeInDuringASegmentRaisesNoLocalCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := newFake(
			fakelive.AwaitStarted(),
			fakelive.Send(audioDelta(make([]byte, 48000)), outputText("Let me ", 0, 400)),
			fakelive.AwaitClient(live.TypeInputAudioAppend, 3),
			fakelive.Send(inputText("wait", 400, 700), audioDelta(make([]byte, 4800)), outputText("finish.", 400, 900)),
		)
		provider := live.New(live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)), live.WithWebSocketDialer(server.Dialer()))
		loop, err := agentloop.New(
			agentloop.WithMode(engine.DuplexSession),
			agentloop.WithSessionInferencer(liveInferencer{provider: provider, cfg: pcmConfig()}),
			agentloop.WithToolExecutionDisabled(),
		)
		if err != nil {
			t.Fatalf("agentloop.New: %v", err)
		}
		ctx, stop := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()
		defer func() {
			stop()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run: %v", err)
			}
		}()

		audio, end := readSegmentWithBargeIn(t, ctx, loop)
		if audio != 2 || end.Status != completed {
			t.Fatalf("segment delivered %d audio deltas and ended %q, want both deltas and completed", audio, end.Status)
		}
	})
}

// readSegmentWithBargeIn reads the loop's deltas until the first segment
// ends, sending three loud user frames when its first audio arrives. It
// returns the audio deltas seen and the segment's MESSAGE.END.
func readSegmentWithBargeIn(t *testing.T, ctx context.Context, loop *agentloop.AgentLoop) (int, *messages.MessageEndValue) {
	t.Helper()
	audio := 0
	for {
		msg, ok := <-loop.Deltas().Chan()
		if !ok {
			t.Fatal("delta stream closed")
		}
		switch value := msg.Value.(type) {
		case *messages.AudioDeltaValue:
			audio++
			if audio > 1 {
				continue
			}
			for range 3 {
				if err := loop.SendAudioInput(ctx, loudPCM()); err != nil {
					t.Fatalf("SendAudioInput: %v", err)
				}
			}
		case *messages.MessageEndValue:
			return audio, value
		}
	}
}

// TestStoppingTheLoopMidStreamClosesWithoutWaitingForTheReader stops the
// agent loop while the assistant is streaming far more audio than the receive
// buffer holds. The runner closes the session and stops reading Receive; the
// close handshake must still reach session.closed at once instead of waiting
// out the close timeout. It runs on the host clock, so a regression fails on
// the elapsed-time bound rather than hanging a synctest bubble.
func TestStoppingTheLoopMidStreamClosesWithoutWaitingForTheReader(t *testing.T) {
	const closeTimeout = 10 * time.Second
	burst := make([]live.Event, 3000)
	for i := range burst {
		burst[i] = audioDelta(make([]byte, 480))
	}
	server := newFake(fakelive.AwaitStarted(), fakelive.Send(burst...))
	provider := live.New(
		live.WithCredentialProvider(live.APIKeyCredentials(sessionKey)),
		live.WithWebSocketDialer(server.Dialer()),
		live.WithCloseTimeout(closeTimeout),
	)
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(liveInferencer{provider: provider, cfg: pcmConfig()}),
		agentloop.WithToolExecutionDisabled(),
	)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	for msg := range loop.Deltas().Chan() {
		if msg.Type == messages.StreamTypeAudioDelta {
			break
		}
	}
	stopped := time.Now()
	stop()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(closeTimeout + 5*time.Second):
		t.Fatal("Run did not return after the close timeout")
	}
	if elapsed := time.Since(stopped); elapsed > closeTimeout/2 {
		t.Fatalf("Run returned %v after the stop, want well under the %v close timeout", elapsed, closeTimeout)
	}
	sawClose := false
	for _, event := range server.ClientEvents() {
		if _, ok := event.(live.SessionClose); ok {
			sawClose = true
		}
	}
	if !sawClose {
		t.Fatal("the fake never received session.close")
	}
}
