package embedding_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestLiveCommitCannotBeOvertakenByLaterPCM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := newEmbeddedLiveProvider()
	defer closeForTest(t, provider)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	provider.onSend = func(ctx context.Context, message messages.StreamMessage) bool {
		if message.Type != messages.StreamTypeMessageEnd {
			return true
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	}
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return provider, nil
	}})
	handle, err := host.OpenLive(ctx, session.LiveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	controlDone := make(chan error, 1)
	go func() { controlDone <- handle.Send(ctx, session.LiveControl{Kind: session.LiveControlAudioCommit}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("commit did not reach provider")
	}
	audioDone := make(chan error, 1)
	go func() { audioDone <- handle.Media().Outbound.WriteFrame(ctx, audio.PCMFrame{Samples: []int16{7}}) }()
	select {
	case <-provider.outbound:
		t.Error("later PCM reached provider before the preceding commit completed")
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		t.Error("live ordering test timed out")
	}
	close(release)
	for _, done := range []<-chan error{controlDone, audioDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ordered operation failed: %v", err)
			}
		case <-ctx.Done():
			t.Error("ordered operation did not finish after release")
		}
	}
}

type finiteTurnsProvider struct {
	*embeddedLiveProvider
	mu        sync.Mutex
	sent      []messages.StreamMessage
	responses int
}

func (provider *finiteTurnsProvider) Send(ctx context.Context, message messages.StreamMessage) bool {
	if !provider.embeddedLiveProvider.Send(ctx, message) {
		return false
	}
	provider.mu.Lock()
	provider.sent = append(provider.sent, message)
	if message.Type != messages.StreamTypeMessageEnd {
		provider.mu.Unlock()
		return true
	}
	provider.responses++
	responseID := "finite-response-" + string(rune('0'+provider.responses))
	provider.mu.Unlock()
	return provider.receive.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant,
		ResponseID: responseID, Value: messages.NewMessageStartValue(),
	}) && provider.receive.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant,
		ResponseID: responseID, Value: messages.NewTextDeltaValue("ok"),
	}) && provider.receive.Write(ctx, messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant,
		ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

type finiteTurnsInferencer struct{ session messages.Session }

func (inferencer *finiteTurnsInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return inferencer.session, nil
}

func TestExternalFiniteCaptureTurnsRemainOrderedAndResponseGated(t *testing.T) {
	provider := &finiteTurnsProvider{embeddedLiveProvider: newEmbeddedLiveProvider()}
	defer closeForTest(t, provider)
	if !provider.receive.Write(context.Background(), messages.StreamMessage{
		Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider", "audio"),
	}) {
		t.Fatal("queue provider SESSION.OPEN")
	}
	service := sessionwire.NewLiveService(sessionwire.LiveDependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &finiteTurnsInferencer{session: provider}, nil
	}})
	runner, ok := service.(session.LiveRunner)
	if !ok {
		t.Fatalf("live service type %T does not expose the finite runner contract", service)
	}
	err := runner.RunLive(context.Background(), session.LiveRunOptions{
		Request:       session.LiveRequest{SessionID: "finite-turns", FinishAfterResponse: true, ExpectedResponses: 2},
		Devices:       devicewire.NewFileService(),
		DeviceRequest: devices.Request{SampleRate: 24000, Channels: audio.Channels},
		CaptureTurns: []devices.FileInput{
			{Source: audio.NewSliceSource([]int16{1, 2, 3}), SampleRate: audio.SampleRate},
			{Source: audio.NewSliceSource([]int16{4, 5}), SampleRate: audio.SampleRate},
		},
		CaptureCompleteControls: []session.LiveControl{{Kind: session.LiveControlAudioCommit}},
	})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	provider.mu.Lock()
	sent := append([]messages.StreamMessage(nil), provider.sent...)
	provider.mu.Unlock()
	var commits int
	for _, message := range sent {
		if message.Type == messages.StreamTypeMessageEnd {
			commits++
		}
	}
	if commits != 2 {
		t.Fatalf("provider commits = %d, want two ordered finite turns; sent=%#v", commits, sent)
	}
}
