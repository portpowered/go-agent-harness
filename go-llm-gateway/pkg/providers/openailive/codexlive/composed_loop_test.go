package codexlive_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/codexlive"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openailive/quicksilver"
)

// sendLog records the stream types the loop sends to the session.
type sendLog struct {
	mu    sync.Mutex
	types []messages.StreamMessageType
}

func (l *sendLog) add(msg messages.StreamMessage) {
	l.mu.Lock()
	l.types = append(l.types, msg.Type)
	l.mu.Unlock()
}

func (l *sendLog) saw(want messages.StreamMessageType) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, got := range l.types {
		if got == want {
			return true
		}
	}
	return false
}

// recordingSession forwards every capability and records each send.
type recordingSession struct {
	messages.Session
	messages.SessionCapabilities
	log *sendLog
}

func (s recordingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	s.log.add(msg)
	return s.Session.Send(ctx, msg)
}

func (s recordingSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.log.add(msg)
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

// codexInferencer connects the codex provider for the agent loop.
type codexInferencer struct {
	provider *codexlive.Provider
	log      *sendLog
}

func (i codexInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.provider.ConnectSession(ctx, codexConfig())
	if err != nil {
		return nil, err
	}
	return recordingSession{Session: session, SessionCapabilities: messages.SessionCapabilities{Wrapped: session}, log: i.log}, nil
}

// loudPCM is 100 ms of loud 24 kHz audio, well over the loop's barge-in
// onset energy.
func loudPCM() []byte {
	samples := make([]int16, 2400)
	for i := range samples {
		samples[i] = 20000
		if i%2 == 1 {
			samples[i] = -20000
		}
	}
	return codec.EncodePCM16(samples)
}

// TestUserBargeInDuringASegmentRaisesNoLocalCancel composes the real duplex
// agent loop with the codex session. Loud user audio arrives while the
// assistant speaks; GPT-Live owns turn-taking, so the loop sends no
// RESPONSE.CANCEL and the segment ends completed at the backend's turn.done.
func TestUserBargeInDuringASegmentRaisesNoLocalCancel(t *testing.T) {
	ctx := deadline(t)
	r := newRig(t)
	log := &sendLog{}
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(codexInferencer{provider: r.provider(), log: log}),
		agentloop.WithToolExecutionDisabled(),
	)
	if err != nil {
		t.Fatalf("agentloop.New: %v", err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- loop.Run(runCtx) }()
	defer func() {
		stop()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run: %v", err)
		}
	}()
	if err := r.backend.WaitSidebands(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.backend.Send(outputText("Let me explain ")); err != nil {
		t.Fatal(err)
	}
	end := readSegmentWithBargeIn(t, ctx, loop, r)
	if end.Status != completed {
		t.Fatalf("segment ended %q, want completed", end.Status)
	}
	if log.saw(messages.StreamTypeResponseCancel) {
		t.Fatal("the loop sent RESPONSE.CANCEL to a full-duplex session")
	}
}

// readSegmentWithBargeIn reads the loop's deltas until the first segment
// ends. When the segment opens it sends loud user audio, then lets the
// backend finish the turn.
func readSegmentWithBargeIn(t *testing.T, ctx context.Context, loop *agentloop.AgentLoop, r *rig) *messages.MessageEndValue {
	t.Helper()
	for {
		var msg messages.StreamMessage
		select {
		case msg = <-loop.Deltas().Chan():
		case <-ctx.Done():
			t.Fatalf("no segment end before the deadline")
		}
		switch value := msg.Value.(type) {
		case *messages.TranscriptDeltaValue:
			if msg.Role != messages.RoleAssistant {
				continue
			}
			for range 3 {
				if err := loop.SendAudioInput(ctx, loudPCM()); err != nil {
					t.Fatalf("SendAudioInput: %v", err)
				}
			}
			if err := r.backend.Send(inputText("wait"), outputText("then finish."), turnDone(quicksilver.RoleAssistant, "")); err != nil {
				t.Fatal(err)
			}
		case *messages.MessageEndValue:
			return value
		}
	}
}
