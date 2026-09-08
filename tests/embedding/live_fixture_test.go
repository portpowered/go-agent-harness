package embedding_test

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type embeddedLiveProvider struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	media     *audio.SessionMedia
	outbound  chan audio.PCMFrame
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	onSend    func(context.Context, messages.StreamMessage) bool
}

func newEmbeddedLiveProvider() *embeddedLiveProvider {
	provider := &embeddedLiveProvider{
		receive:  messages.NewTypedBuffer[messages.StreamMessage](16),
		outbound: make(chan audio.PCMFrame, 4), done: make(chan struct{}),
	}
	provider.media = audio.NewSessionMediaAtRate(func(ctx context.Context, frame audio.PCMFrame) error {
		frame.Samples = append([]int16(nil), frame.Samples...)
		select {
		case provider.outbound <- frame:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, 24000)
	return provider
}

func (provider *embeddedLiveProvider) ConnectSession(context.Context) (messages.Session, error) {
	return provider, nil
}

func (provider *embeddedLiveProvider) Send(ctx context.Context, message messages.StreamMessage) bool {
	if provider.onSend != nil {
		return provider.onSend(ctx, message)
	}
	return ctx.Err() == nil
}

func (provider *embeddedLiveProvider) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return provider.receive
}

func (provider *embeddedLiveProvider) Done() <-chan struct{} { return provider.done }

func (provider *embeddedLiveProvider) RTCMedia() audio.MediaEndpoints {
	return provider.media.Endpoints()
}

func (provider *embeddedLiveProvider) Close() error {
	provider.closeOnce.Do(func() {
		close(provider.done)
		provider.closeErr = provider.media.Close()
	})
	return provider.closeErr
}

func closeForTest(t testing.TB, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close test resource: %v", err)
	}
}

type embeddedPlaybackController struct{ started chan audio.PlaybackResponse }

func (controller *embeddedPlaybackController) StartPlayback(response audio.PlaybackResponse) {
	select {
	case controller.started <- response:
	default:
	}
}

func (*embeddedPlaybackController) InterruptPlayback(audio.PlaybackResponse) (int, bool) {
	return 17, true
}
