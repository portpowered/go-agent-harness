package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type sessionAudioReader struct {
	reader        io.Reader
	closeOnCancel bool
	mu            sync.RWMutex
	ctx           context.Context
	closeOnce     sync.Once
	closeErr      error
}

type contextAudioReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type deadlineAudioReader interface {
	SetReadDeadline(time.Time) error
}

const sessionAudioReadDeadline = 250 * time.Millisecond

func newSessionAudioReader(reader io.Reader, closeOnCancel bool) *sessionAudioReader {
	return &sessionAudioReader{reader: reader, closeOnCancel: closeOnCancel}
}

func (r *sessionAudioReader) bindContext(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

func (r *sessionAudioReader) Read(destination []byte) (int, error) {
	ctx, reader := r.readState()
	if ctx == nil {
		return reader.Read(destination)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if cancellable, ok := reader.(contextAudioReader); ok {
		return cancellable.ReadContext(ctx, destination)
	}
	if isFiniteAudioReader(reader) {
		return reader.Read(destination)
	}
	return r.readInterruptibly(ctx, reader, destination)
}

func (r *sessionAudioReader) readState() (context.Context, io.Reader) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ctx, r.reader
}

func isFiniteAudioReader(reader io.Reader) bool {
	// These standard in-memory readers have a finite, synchronous Read
	// contract and are used by injected command stdin in tests and embedders.
	// They cannot block waiting for more input, so they do not need a helper
	// goroutine or a close operation to make cancellation safe.
	switch reader.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return true
	}
	return false
}

func (r *sessionAudioReader) readInterruptibly(ctx context.Context, reader io.Reader, destination []byte) (int, error) {
	deadliner, ok := reader.(deadlineAudioReader)
	if !ok {
		if r.closeOnCancel {
			if _, closeOK := reader.(io.Closer); closeOK {
				return readAudioReaderWithCancellation(ctx, reader, r, destination)
			}
		}
		return 0, fmt.Errorf("%w: stdin must implement ReadContext or SetReadDeadline", ErrRuntimeAudioInputUninterruptible)
	}
	return r.readWithDeadline(ctx, reader, deadliner, destination)
}

func (r *sessionAudioReader) readWithDeadline(ctx context.Context, reader io.Reader, deadliner deadlineAudioReader, destination []byte) (int, error) {
	if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
		if r.closeOnCancel {
			if _, closeOK := reader.(io.Closer); closeOK {
				return readAudioReaderWithCancellation(ctx, reader, r, destination)
			}
		}
		return 0, errors.Join(
			ErrRuntimeAudioInputUninterruptible,
			fmt.Errorf("stdin read deadline setup failed: %w", err),
		)
	}
	for {
		count, readErr := reader.Read(destination)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return count, ctxErr
		}
		if errors.Is(readErr, os.ErrDeadlineExceeded) && count == 0 {
			if err := renewAudioReadDeadline(deadliner); err != nil {
				return 0, err
			}
			continue
		}
		_ = deadliner.SetReadDeadline(time.Time{})
		return count, readErr
	}
}

func renewAudioReadDeadline(deadliner deadlineAudioReader) error {
	if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
		return errors.Join(ErrRuntimeAudioInputUninterruptible, fmt.Errorf("stdin read deadline renewal failed: %w", err))
	}
	return nil
}

// Close releases only the process-owned duplicate created for --audio-in -.
// Caller-owned command input remains untouched when CloseStdinOnCancel is
// false. It is idempotent because cancellation may close the reader before
// source teardown runs.
func (r *sessionAudioReader) Close() error {
	if r == nil || !r.closeOnCancel {
		return nil
	}
	r.closeOnce.Do(func() {
		if closer, ok := r.reader.(io.Closer); ok {
			r.closeErr = closer.Close()
		}
	})
	return r.closeErr
}

type audioReadResult struct {
	count int
	err   error
}

// readAudioReaderWithCancellation is reserved for descriptors explicitly
// owned by the CLI process. Closing the descriptor is the only portable way
// to interrupt a blocked pipe read on platforms where SetReadDeadline is not
// implemented for os.File pipes.
func readAudioReaderWithCancellation(ctx context.Context, reader io.Reader, closer io.Closer, destination []byte) (int, error) {
	resultCh := make(chan audioReadResult, 1)
	go func() {
		count, err := reader.Read(destination)
		resultCh <- audioReadResult{count: count, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.count, result.err
	case <-ctx.Done():
		_ = closer.Close()
		result := <-resultCh
		if result.count > 0 {
			return result.count, ctx.Err()
		}
		return 0, ctx.Err()
	}
}

func streamRuntimeAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *runtimeAudioSource) (runErr error) {
	return streamSessionAudioInput(ctx, loop, source)
}

// shouldStopAudioInputSessionLoop applies audio-aware stop rules. Before the
// end-of-turn signal is accepted, only a provider-initiated SESSION.CLOSE may
// stop the run. Once awaitingResponse is set (end-of-turn delivered after
// local EOF), only a completed non-tool assistant response, a terminal ERROR,
// or a provider SESSION.CLOSE ends the session. When the session has a real
// executor, the provider's tool-call MESSAGE.END and the ToolRunner's
// RoleTool MESSAGE.END are intermediate boundaries and must not stop the
// session before the follow-up assistant response is consumed.
func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent, awaitingResponse bool) bool {
	_ = closeSent
	if !awaitingResponse {
		return msg.Type == messages.StreamTypeSessionClose
	}
	if hasAudioTerminalFailure(msg, opts) {
		return true
	}
	if opts.WaitForClose {
		return isTerminalErrorMessage(msg) || msg.Type == messages.StreamTypeSessionClose
	}
	return shouldStopAfterAudioResponse(msg, opts)
}

func hasAudioTerminalFailure(msg messages.StreamMessage, opts sessionLoopOptions) bool {
	return msg.Type == messages.StreamTypeMessageEnd && opts.observer != nil && (opts.observer.hasTerminalToolContinuationFailure() || opts.observer.hasTerminalScheduledResponseFailure())
}

func shouldStopAfterAudioResponse(msg messages.StreamMessage, opts sessionLoopOptions) bool {
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		if opts.observer != nil && !opts.observer.lastMessageEndAdmitted() {
			return false
		}
		if opts.RequireAssistantResponse {
			if msg.Role == messages.RoleTool || opts.observer == nil || !opts.observer.assistantResponseCompleted() {
				return false
			}
		}
		return true
	case messages.StreamTypeSessionClose:
		return true
	default:
		return isTerminalErrorMessage(msg)
	}
}

// The shared audio source streams WAV samples at their declared rate. The
// shared processor converts them continuously at the provider boundary without
// loading the complete file or resetting DSP state between input packets.

type sessionAudioRateSource interface {
	SampleRate() int
}

func runtimeAudioSourceSampleRate(source audio.AudioSource, fallback int) int {
	if rated, ok := source.(sessionAudioRateSource); ok && rated.SampleRate() > 0 {
		return rated.SampleRate()
	}
	return fallback
}

func sessionWAVFormatError(path string, reason string) error {
	return &RuntimeAudioInputError{
		Kind: RuntimeAudioInputFormat,
		Path: path,
		Err: &audio.FormatError{
			Path:      path,
			Extension: ".wav",
			Format:    "wav",
			Reason:    reason,
		},
	}
}

// openSessionWAVSource validates the RIFF/fmt contract before returning so
// every rejected format fails during preflight with zero delivered frames.
func openSessionWAVSource(path string) (audio.AudioSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, classifySessionAudioOpenError(path, err)
	}
	source, err := newSessionWAVSource(path, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return source, nil
}

// newSessionWAVSource parses the RIFF descriptor and chunk headers from r and
// returns a source positioned at the first data-chunk byte. Harness-rate
// payloads stream frame by frame; 24 kHz payloads are decoded once and
// resampled to the harness rate. The payload is otherwise never read here.
func newSessionWAVSource(path string, r io.ReadSeekCloser) (audio.AudioSource, error) {
	source, err := audio.NewWAVSource(path, r)
	if err != nil {
		return nil, errors.Join(sessionWAVFormatError(path, err.Error()), err)
	}
	return source, nil
}
