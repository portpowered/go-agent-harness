package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

type pcmBridge struct {
	reader   *io.PipeReader
	writer   *io.PipeWriter
	once     sync.Once
	mu       sync.Mutex
	closed   bool
	closeErr error
}

const pcmBridgeBufferBytes = 64 * 1024

func newPCMBridge(ctx context.Context) *pcmBridge {
	reader, writer := io.Pipe()
	bridge := &pcmBridge{reader: reader, writer: writer}
	go func() {
		<-ctx.Done()
		bridge.close()
	}()
	return bridge
}

func (b *pcmBridge) write(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	n, err := b.writer.Write(pcm)
	if err != nil {
		return err
	}
	if n != len(pcm) {
		return io.ErrShortWrite
	}
	return nil
}

func (b *pcmBridge) pump(ctx context.Context, ready <-chan selfplay.AudioInput, fail func(error), name string, observe func([]byte)) {
	input, ok := waitForAudioInput(ctx, ready)
	if !ok {
		return
	}
	buffer := make([]byte, pcmBridgeBufferBytes)
	for {
		count, err := b.reader.Read(buffer)
		if count > 0 && !b.forward(input, ctx, buffer[:count], fail, name, observe) {
			return
		}
		if err != nil {
			b.handleReadError(err, fail, name)
			return
		}
	}
}

func waitForAudioInput(ctx context.Context, ready <-chan selfplay.AudioInput) (selfplay.AudioInput, bool) {
	select {
	case input, ok := <-ready:
		return input, ok && input != nil
	case <-ctx.Done():
		return nil, false
	}
}

func (b *pcmBridge) forward(input selfplay.AudioInput, ctx context.Context, raw []byte, fail func(error), name string, observe func([]byte)) bool {
	pcm := append([]byte(nil), raw...)
	if sendErr := input.SendAudioInput(ctx, pcm); sendErr != nil {
		if !isCancellation(sendErr) {
			fail(fmt.Errorf("%s PCM bridge send: %w", name, sendErr))
		}
		return false
	}
	if observe != nil {
		observe(pcm)
	}
	return true
}

func (b *pcmBridge) handleReadError(err error, fail func(error), name string) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if !closed && !isCancellation(err) {
		fail(fmt.Errorf("%s PCM bridge read: %w", name, err))
	}
}

func (b *pcmBridge) close() {
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		writerErr := b.writer.Close()
		readerErr := b.reader.Close()
		b.mu.Lock()
		b.closeErr = errors.Join(writerErr, readerErr)
		b.mu.Unlock()
	})
}

func (b *pcmBridge) closeError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeErr
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrClosedPipe)
}
