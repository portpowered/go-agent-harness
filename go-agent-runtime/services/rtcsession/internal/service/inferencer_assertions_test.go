package service

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func assertAudioConfigurationCapability(t *testing.T, value any) {
	t.Helper()
	configurer, ok := value.(interface {
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
	})
	if !ok {
		t.Fatal("audio configuration capability was not retained")
	}
	configurer.SetSessionAudioOutput(models.AudioFormat("pcm"), models.SampleRate(24000))
	configurer.SetSessionAudioInput(models.AudioFormat("pcm"), models.SampleRate(24000))
}

func assertMediaCapability(t *testing.T, value messages.Session, provider *testSession) {
	t.Helper()
	mediaSession, ok := value.(sharedaudio.MediaSession)
	if !ok || mediaSession.RTCMedia().Inbound != provider.media.Inbound {
		t.Fatalf("media capability = %T, want provider media", value)
	}
}

func assertSendOutcome(t *testing.T, value messages.Session, providerErr error) {
	t.Helper()
	sender, ok := value.(messages.SessionSendOutcomeSender)
	if !ok {
		t.Fatal("send outcome capability was not forwarded")
	}
	outcome := sender.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	if outcome.Status != messages.SessionSendBufferFull || !errors.Is(outcome.Err, providerErr) {
		t.Fatalf("send outcome = %#v, want provider outcome", outcome)
	}
}

func assertDropCounters(t *testing.T, value messages.Session) {
	t.Helper()
	counters, ok := value.(messages.SessionDropCounters)
	if !ok || counters.InputDrops() != 7 || counters.OutputDrops() != 11 {
		t.Fatalf("drop counters = (%d, %d), want (7, 11)", counters.InputDrops(), counters.OutputDrops())
	}
}

func assertResponseCapabilities(t *testing.T, value messages.Session) {
	t.Helper()
	if !messages.SupportsSessionResponseRequests(value) {
		t.Fatal("response request capability was not forwarded")
	}
	requester, ok := value.(messages.SessionResponseRequester)
	if !ok || requester.RequestResponse(context.Background()).Status != messages.SessionSendSucceeded {
		t.Fatal("response request did not reach provider")
	}
}

func assertCompleteMessageCapabilities(t *testing.T, value messages.Session) {
	t.Helper()
	complete, ok := value.(interface {
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	})
	if !ok || !complete.SupportsCompleteMessages() || !complete.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("complete-message capability was not forwarded")
	}
}

func assertMessageCapabilities(t *testing.T, value messages.Session) {
	t.Helper()
	sender, ok := value.(interface {
		SendMessage(context.Context, messages.Message) bool
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	if !ok || !sender.SendMessage(context.Background(), messages.Message{}) || !sender.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("message capability was not forwarded")
	}
}

func assertTerminalCapability(t *testing.T, value messages.Session, providerErr error) {
	t.Helper()
	terminal, ok := value.(interface{ TerminalError() error })
	if !ok || !errors.Is(terminal.TerminalError(), providerErr) {
		t.Fatal("terminal error capability was not forwarded")
	}
}

func assertSessionClose(t *testing.T, wrapped rtcsession.Inferencer, session messages.Session, provider *testSession, runtime *testRuntime, providerErr error) {
	t.Helper()
	if err := wrapped.CloseSession(); !errors.Is(err, providerErr) {
		t.Fatalf("inferencer close session = %v, want provider close identity", err)
	}
	if err := session.Close(); !errors.Is(err, providerErr) {
		t.Fatalf("session close = %v, want provider close identity", err)
	}
	if err := session.Close(); !errors.Is(err, providerErr) {
		t.Fatalf("repeated session close = %v, want stable provider identity", err)
	}
	if provider.closeCount.Load() != 1 || runtime.closeCount.Load() != 1 {
		t.Fatalf("close counts = provider %d/runtime %d, want one each", provider.closeCount.Load(), runtime.closeCount.Load())
	}
}
