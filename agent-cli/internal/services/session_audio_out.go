package services

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const sessionAudioOutputBufferSize = 256

// RunSessionWithAudioOut runs a session and writes assistant AUDIO.DELTA
// samples to path as they arrive. An empty path preserves the normal session
// output behavior. A path of "-" writes raw little-endian PCM16 to out.
func RunSessionWithAudioOut(ctx context.Context, out io.Writer, opts SessionRunOptions, path string) (runErr error) {
	return RunSessionWithAudioOutAndTextSeed(ctx, out, opts, path, SessionTextSeed{})
}

// RunSessionWithAudioOutAndTextSeed combines the session text-seed behavior
// with assistant audio output. An empty path preserves the normal session
// output behavior, including the --prompt presence contract.
func RunSessionWithAudioOutAndTextSeed(ctx context.Context, out io.Writer, opts SessionRunOptions, path string, seed SessionTextSeed) (runErr error) {
	if path == "" {
		if seed.Present {
			return RunSessionWithTextSeed(ctx, out, opts, seed)
		}
		return RunSession(ctx, out, opts)
	}
	if seed.Present {
		opts.Prompt = seed.Value
	}

	sink, err := audio.NewFileSink(path, out)
	if err != nil {
		return fmt.Errorf("--audio-out %q: %w", path, err)
	}
	audioOut := &sessionAudioOutput{path: path, sink: sink}
	defer func() { runErr = errors.Join(runErr, audioOut.close()) }()

	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}

	if plan.inferencer != nil {
		wirePrompt := ""
		if seed.Present {
			wirePrompt = nextSessionTextWirePrompt()
			plan.loop.Prompt = wirePrompt
		}
		wrapped := newSessionAudioOutputInferencer(plan.inferencer, audioOut, wirePrompt, seed.Value)
		plan.inferencer = wrapped

		// A binary stdout stream cannot also carry session text, announcements,
		// or terminal decorations. File output keeps the established text path.
		sessionOut := out
		if path == "-" {
			sessionOut = io.Discard
		}
		runErr = plan.run(ctx, sessionOut)
		wrapped.wait()
		if outputErr := wrapped.err(); outputErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("--audio-out %q: %w", path, outputErr))
		}
		return runErr
	}

	sessionOut := out
	if path == "-" {
		sessionOut = io.Discard
	}
	return plan.run(ctx, sessionOut)
}

type sessionAudioOutput struct {
	path string
	sink audio.AudioSink

	mu        sync.Mutex
	closed    bool
	wrote     bool
	pending   []int16
	closeOnce sync.Once
	closeErr  error
}

func (o *sessionAudioOutput) writeDelta(ctx context.Context, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	if len(content)%2 != 0 {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d", len(content))
	}

	samples := make([]int16, len(content)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(content[index*2:]))
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return audio.ErrClosed
	}
	o.pending = append(o.pending, samples...)
	for len(o.pending) >= audio.FrameSize {
		frame := append([]int16(nil), o.pending[:audio.FrameSize]...)
		o.pending = o.pending[audio.FrameSize:]
		if err := o.sink.WriteFrame(ctx, frame); err != nil {
			return err
		}
		o.wrote = true
	}
	return nil
}

func (o *sessionAudioOutput) close() error {
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		sinkErr := o.sink.Close()
		var pendingErr error
		if len(o.pending) != 0 {
			pendingErr = fmt.Errorf("PCM16 audio output ended with %d samples in an incomplete %d-sample frame", len(o.pending), audio.FrameSize)
		}
		wrote := o.wrote
		o.mu.Unlock()
		if sinkErr != nil && errors.Is(sinkErr, wavio.ErrEmptySamples) && !wrote && strings.EqualFold(filepath.Ext(o.path), ".wav") {
			// The shared WAV sink rejects an empty sample payload. A no-audio
			// session is represented by no output file, not a corrupt stub.
			if removeErr := os.Remove(o.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				sinkErr = errors.Join(sinkErr, removeErr)
			} else {
				sinkErr = nil
			}
		}
		o.mu.Lock()
		o.closeErr = errors.Join(pendingErr, sinkErr)
		o.mu.Unlock()
	})
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closeErr
}

type sessionAudioOutputInferencer struct {
	inner      messages.SessionInferencer
	output     *sessionAudioOutput
	wirePrompt string
	seedValue  string

	mu        sync.Mutex
	lastErr   error
	connected *sessionAudioOutputSession
}

func newSessionAudioOutputInferencer(inner messages.SessionInferencer, output *sessionAudioOutput, wirePrompt string, seedValue string) *sessionAudioOutputInferencer {
	return &sessionAudioOutputInferencer{
		inner:      inner,
		output:     output,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
	}
}

func (i *sessionAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	wrapped := newSessionAudioOutputSession(ctx, session, i.output, i.recordErr, i.wirePrompt, i.seedValue)
	i.mu.Lock()
	i.connected = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionAudioOutputInferencer) wait() {
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		<-connected.done
	}
}

func (i *sessionAudioOutputInferencer) recordErr(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	if i.lastErr == nil {
		i.lastErr = err
	}
	i.mu.Unlock()
}

func (i *sessionAudioOutputInferencer) err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type sessionAudioOutputSession struct {
	messages.Session
	ctx        context.Context
	output     *sessionAudioOutput
	record     func(error)
	wirePrompt string
	seedValue  string

	receive  *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	seedMu   sync.Mutex
	seedSent bool
}

func newSessionAudioOutputSession(ctx context.Context, inner messages.Session, output *sessionAudioOutput, record func(error), wirePrompt string, seedValue string) *sessionAudioOutputSession {
	s := &sessionAudioOutputSession{
		Session:    inner,
		ctx:        ctx,
		output:     output,
		record:     record,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
		receive:    messages.NewTypedBuffer[messages.StreamMessage](sessionAudioOutputBufferSize),
		done:       make(chan struct{}),
	}
	go s.forward()
	return s
}

func (s *sessionAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seedValue)
	}
	return s.Session.Send(ctx, msg)
}

func (s *sessionAudioOutputSession) replaceSeed(msg messages.StreamMessage) bool {
	if s.wirePrompt == "" || msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}

	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}

func (s *sessionAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionAudioOutputSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionAudioOutputSession) forward() {
	defer s.once.Do(func() { close(s.done) })
	input := s.Session.Receive()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessage(msg) {
				return
			}
		case <-s.Session.Done():
			s.drain(input)
			return
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *sessionAudioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := input.Read()
		if !ok {
			return
		}
		if !s.forwardMessage(msg) {
			return
		}
	}
}

func (s *sessionAudioOutputSession) forwardMessage(msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.record(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
			_ = s.Close()
			return false
		}
		if err := s.output.writeDelta(s.ctx, value.Content); err != nil {
			s.record(err)
			_ = s.Close()
			return false
		}
	}

	for {
		if outcome := s.receive.WriteContext(s.ctx, msg); outcome.OK() {
			return true
		} else if outcome.Err != nil {
			return false
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	// Provider session adapters currently omit Role on server events; an
	// explicitly user/tool/system-authored delta must still be ignored.
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}
