package services

import (
	"bytes"
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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionAudioInput carries the command-line presence bit separately from the
// value so --audio-in= can be rejected instead of treated as an omitted flag.
type SessionAudioInput struct {
	Path  string
	Stdin io.Reader
	// MaxDuration bounds an audio-enabled session through the shared loop
	// options when the caller supplies one.
	MaxDuration time.Duration
	// Source and SendAudioInput are optional deterministic service-test seams.
	// CLI callers leave them nil so paths use the file-backed sources and
	// frames use the AgentLoop's SendAudioInput method.
	Source         audio.AudioSource
	SendAudioInput func(context.Context, []byte) error
	Present        bool
	DevicePresent  bool
}

// SessionAudioInputErrorKind identifies the failed session audio boundary.
type SessionAudioInputErrorKind string

const (
	SessionAudioInputEmpty      SessionAudioInputErrorKind = "empty"
	SessionAudioInputMissing    SessionAudioInputErrorKind = "missing"
	SessionAudioInputUnreadable SessionAudioInputErrorKind = "unreadable"
	SessionAudioInputFormat     SessionAudioInputErrorKind = "format"
	SessionAudioInputConflict   SessionAudioInputErrorKind = "conflict"
	SessionAudioInputRead       SessionAudioInputErrorKind = "read"
	SessionAudioInputSend       SessionAudioInputErrorKind = "send"
	SessionAudioInputClose      SessionAudioInputErrorKind = "close"
)

var (
	ErrSessionAudioInputEmpty           = errors.New("session audio input path is empty")
	ErrSessionAudioInputMissing         = errors.New("session audio input is missing")
	ErrSessionAudioInputUnreadable      = errors.New("session audio input is unreadable")
	ErrSessionAudioInputFormat          = errors.New("session audio input format is unsupported")
	ErrSessionAudioInputConflict        = errors.New("--audio-in and --audio-in-device (audio device input) cannot be used together")
	ErrSessionAudioInputRead            = errors.New("session audio input read failed")
	ErrSessionAudioInputSend            = errors.New("session audio input send failed")
	ErrSessionAudioInputClose           = errors.New("session audio input close failed")
	ErrSessionAudioInputUninterruptible = errors.New("session audio input reader cannot be interrupted safely")
)

// SessionAudioInputError adds the command boundary and preserves the
// underlying audio, filesystem, context, and loop error identity.
type SessionAudioInputError struct {
	Kind SessionAudioInputErrorKind
	Path string
	Err  error
}

func (e *SessionAudioInputError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("agent session --audio-in %q: %s", e.Path, e.Kind)
	}
	return fmt.Sprintf("agent session --audio-in %q: %s: %v", e.Path, e.Kind, e.Err)
}

func (e *SessionAudioInputError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(sessionAudioInputKindError(e.Kind), e.Err)
}

func sessionAudioInputKindError(kind SessionAudioInputErrorKind) error {
	switch kind {
	case SessionAudioInputEmpty:
		return ErrSessionAudioInputEmpty
	case SessionAudioInputMissing:
		return ErrSessionAudioInputMissing
	case SessionAudioInputUnreadable:
		return ErrSessionAudioInputUnreadable
	case SessionAudioInputFormat:
		return ErrSessionAudioInputFormat
	case SessionAudioInputConflict:
		return ErrSessionAudioInputConflict
	case SessionAudioInputRead:
		return ErrSessionAudioInputRead
	case SessionAudioInputSend:
		return ErrSessionAudioInputSend
	case SessionAudioInputClose:
		return ErrSessionAudioInputClose
	default:
		return nil
	}
}

// RunSessionWithAudioInput runs the shared session runtime while streaming the
// selected file or raw stdin through the agent loop's session audio inbox.
// The ordinary session path remains untouched when the flag is absent.
func RunSessionWithAudioInput(ctx context.Context, out io.Writer, opts SessionRunOptions, input SessionAudioInput) error {
	if !sessionAudioInputSelected(input) {
		return RunSession(ctx, out, opts)
	}
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	return runSessionWithAudioInputPlan(ctx, out, input, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionRuntime(opts)
	})
}

// RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration composes
// the instructions, text-seed, duration, and audio-input extensions on the
// command surface. The no-audio path remains on the established instructions
// entry point so its replay and duration artifact behavior stays unchanged.
func RunSessionWithInstructionsAndAudioInputAndTextSeedAndMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, seed SessionTextSeed, input SessionAudioInput, systemPrompt string) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if !sessionAudioInputSelected(input) {
		return RunSessionWithInstructionsAndAudioOutAndTextSeedAndMaxDuration(ctx, out, opts, "", maxDuration, seed, systemPrompt)
	}
	if seed.Present {
		opts.Prompt = seed.Value
	}
	input.MaxDuration = maxDuration
	if err := validateSessionAudioInput(input); err != nil {
		return err
	}
	if systemPrompt == "" || (opts.ReplayPath != "" && opts.SessionInferencer == nil) {
		return runSessionWithAudioInputPlan(ctx, out, input, func() (sessionRuntimePlan, error) {
			if err := validateSessionRunOptions(opts); err != nil {
				return sessionRuntimePlan{}, err
			}
			return planSessionRuntime(opts)
		})
	}
	return runSessionWithAudioInputPlan(ctx, out, input, func() (sessionRuntimePlan, error) {
		if err := validateSessionRunOptions(opts); err != nil {
			return sessionRuntimePlan{}, err
		}
		instructions, err := resolveSessionInstructions(opts, systemPrompt)
		if err != nil {
			return sessionRuntimePlan{}, err
		}
		return planSessionWithResolvedInstructions(opts, instructions)
	})
}

func sessionAudioInputSelected(input SessionAudioInput) bool {
	return input.Present || input.Path != ""
}

// runSessionWithAudioInputPlan validates and opens the audio input before the
// plan is built so every preflight failure happens before any provider dial,
// then hands the opened source to the shared session lifecycle through the
// loop options. No session behavior changes when the flag is absent.
func runSessionWithAudioInputPlan(ctx context.Context, out io.Writer, input SessionAudioInput, planFactory func() (sessionRuntimePlan, error)) (runErr error) {
	source, err := openSessionAudioInput(input)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	plan, err := planFactory()
	if err != nil {
		return err
	}
	if input.MaxDuration > 0 {
		plan.loop.MaxDuration = input.MaxDuration
	}
	// A finite audio source is the input lifetime. Do not close immediately on
	// SESSION.OPEN; allow every source frame to reach the loop first.
	plan.loop.CloseAfterOpen = false
	plan.loop.AudioIn = source
	return plan.run(ctx, out)
}

func validateSessionAudioInput(input SessionAudioInput) error {
	if input.DevicePresent {
		return &SessionAudioInputError{
			Kind: SessionAudioInputConflict,
			Path: input.Path,
			Err:  ErrSessionAudioInputConflict,
		}
	}
	if strings.TrimSpace(input.Path) == "" {
		return &SessionAudioInputError{
			Kind: SessionAudioInputEmpty,
			Path: input.Path,
			Err:  ErrSessionAudioInputEmpty,
		}
	}
	return nil
}

func openSessionAudioInput(input SessionAudioInput) (*sessionAudioSource, error) {
	if input.Source != nil {
		return &sessionAudioSource{source: input.Source, path: input.Path, send: input.SendAudioInput}, nil
	}

	if strings.EqualFold(filepath.Ext(input.Path), ".wav") {
		source, err := openSessionWAVSource(input.Path)
		if err != nil {
			return nil, err
		}
		return &sessionAudioSource{source: source, path: input.Path, send: input.SendAudioInput}, nil
	}

	stdin := input.Stdin
	var inputReader *sessionAudioReader
	if input.Path == "-" {
		if stdin == nil {
			return nil, classifySessionAudioOpenError(input.Path, audio.ErrNilStream)
		}
		inputReader = newSessionAudioReader(stdin)
		stdin = inputReader
	}
	source, err := audio.NewFileSource(input.Path, stdin)
	if err != nil {
		return nil, classifySessionAudioOpenError(input.Path, err)
	}
	if input.Path != "-" {
		info, statErr := os.Stat(input.Path)
		if statErr != nil {
			_ = source.Close()
			return nil, classifySessionAudioOpenError(input.Path, statErr)
		}
		if info.IsDir() {
			_ = source.Close()
			return nil, &SessionAudioInputError{
				Kind: SessionAudioInputUnreadable,
				Path: input.Path,
				Err:  fmt.Errorf("path is a directory; provide a .wav, .pcm, or .raw file"),
			}
		}
	}
	return &sessionAudioSource{source: source, path: input.Path, reader: inputReader, send: input.SendAudioInput}, nil
}

func classifySessionAudioOpenError(path string, err error) error {
	kind := SessionAudioInputUnreadable
	switch {
	case errors.Is(err, audio.ErrUnsupportedFormat):
		kind = SessionAudioInputFormat
	case errors.Is(err, os.ErrNotExist):
		kind = SessionAudioInputMissing
	case errors.Is(err, audio.ErrNilStream):
		kind = SessionAudioInputUnreadable
	}
	return &SessionAudioInputError{Kind: kind, Path: path, Err: err}
}

type sessionAudioSource struct {
	source audio.AudioSource
	path   string
	reader *sessionAudioReader
	send   func(context.Context, []byte) error
	once   sync.Once
	err    error
}

func (s *sessionAudioSource) bindContext(ctx context.Context) {
	if s.reader != nil {
		s.reader.bindContext(ctx)
	}
}

func (s *sessionAudioSource) Close() error {
	s.once.Do(func() { s.err = s.source.Close() })
	if s.err == nil {
		return nil
	}
	return &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: s.err}
}

// sessionAudioReader carries cancellation into readers that can honor it
// without closing the caller-owned stdin. The standard io.Reader contract has
// no cancellation method, so a reader must implement ReadContext or support
// read deadlines once the session context is bound. Calling an arbitrary
// blocking Read in a helper goroutine would leak that goroutine when stdin is
// caller-owned and cannot be closed.
type sessionAudioReader struct {
	reader io.Reader
	mu     sync.RWMutex
	ctx    context.Context
}

type contextAudioReader interface {
	ReadContext(context.Context, []byte) (int, error)
}

type deadlineAudioReader interface {
	SetReadDeadline(time.Time) error
}

const sessionAudioReadDeadline = 250 * time.Millisecond

func newSessionAudioReader(reader io.Reader) *sessionAudioReader {
	return &sessionAudioReader{reader: reader}
}

func (r *sessionAudioReader) bindContext(ctx context.Context) {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
}

func (r *sessionAudioReader) Read(destination []byte) (int, error) {
	r.mu.RLock()
	ctx := r.ctx
	reader := r.reader
	r.mu.RUnlock()
	if ctx == nil {
		return reader.Read(destination)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if cancellable, ok := reader.(contextAudioReader); ok {
		return cancellable.ReadContext(ctx, destination)
	}
	// These standard in-memory readers have a finite, synchronous Read
	// contract and are used by injected command stdin in tests and embedders.
	// They cannot block waiting for more input, so they do not need a helper
	// goroutine or a close operation to make cancellation safe.
	switch reader.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		return reader.Read(destination)
	}
	deadliner, ok := reader.(deadlineAudioReader)
	if !ok {
		return 0, fmt.Errorf("%w: stdin must implement ReadContext or SetReadDeadline", ErrSessionAudioInputUninterruptible)
	}
	if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
		return 0, errors.Join(
			ErrSessionAudioInputUninterruptible,
			fmt.Errorf("stdin read deadline setup failed: %w", err),
		)
	}
	for {
		count, readErr := reader.Read(destination)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return count, ctxErr
		}
		if errors.Is(readErr, os.ErrDeadlineExceeded) && count == 0 {
			if err := deadliner.SetReadDeadline(time.Now().Add(sessionAudioReadDeadline)); err != nil {
				return 0, errors.Join(
					ErrSessionAudioInputUninterruptible,
					fmt.Errorf("stdin read deadline renewal failed: %w", err),
				)
			}
			continue
		}
		_ = deadliner.SetReadDeadline(time.Time{})
		return count, readErr
	}
}

func streamSessionAudioInput(ctx context.Context, loop *agentloop.AgentLoop, source *sessionAudioSource) (runErr error) {
	defer func() {
		if closeErr := source.Close(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	frame := make([]int16, audio.FrameSize)
	for {
		clear(frame)
		if err := source.source.ReadFrame(ctx, frame); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: source.path, Err: err}
		}

		pcm := make([]byte, len(frame)*2)
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		send := source.send
		if send == nil {
			send = loop.SendAudioInput
		}
		if err := send(ctx, pcm); err != nil {
			return &SessionAudioInputError{Kind: SessionAudioInputSend, Path: source.path, Err: err}
		}
	}
}

// joinSessionTerminationErrors drops expected cancellations and preserves the
// identity of real loop and audio producer failures.
func joinSessionTerminationErrors(runErr, audioErr error) error {
	var errs []error
	if runErr != nil && !isSessionCancellation(runErr) {
		errs = append(errs, fmt.Errorf("session error: %w", runErr))
	}
	if audioErr != nil && !isSessionCancellation(audioErr) {
		errs = append(errs, audioErr)
	}
	return errors.Join(errs...)
}

func isSessionCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func shouldStopAudioInputSessionLoop(msg messages.StreamMessage, opts sessionLoopOptions, closeSent, audioDone bool) bool {
	if !audioDone {
		return msg.Type == messages.StreamTypeSessionClose
	}
	return shouldStopSessionLoop(msg, opts, closeSent)
}

// Incremental WAV streaming lives beside the session audio boundary so
// .wav input streams frame-by-frame through the same raw PCM16 path without
// buffering the whole payload. The shared pkg/wavio decoder reads whole files
// and is deliberately not used here.
const (
	sessionWAVDescriptorBytes  = 12
	sessionWAVChunkHeaderBytes = 8
	sessionWAVFmtChunkMinBytes = 16
	sessionWAVAudioFormatPCM   = 1
	sessionWAVBitsPerSample    = 16
)

// sessionWAVSource streams PCM16 frames from a RIFF WAVE file incrementally.
// Header chunks are parsed once at open; data-chunk bytes are read one frame
// at a time by ReadFrame.
type sessionWAVSource struct {
	path      string
	file      io.ReadSeekCloser
	remaining int64
	done      bool
	closed    bool
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

var _ audio.AudioSource = (*sessionWAVSource)(nil)

func sessionWAVFormatError(path string, reason string) error {
	return &SessionAudioInputError{
		Kind: SessionAudioInputFormat,
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
func openSessionWAVSource(path string) (*sessionWAVSource, error) {
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
// returns a source positioned at the first data-chunk byte. The payload is
// never read here; ReadFrame streams it frame by frame.
func newSessionWAVSource(path string, r io.ReadSeekCloser) (*sessionWAVSource, error) {
	fail := func(err error) (*sessionWAVSource, error) {
		return nil, err
	}

	descriptor := make([]byte, sessionWAVDescriptorBytes)
	if _, err := io.ReadFull(r, descriptor); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fail(sessionWAVFormatError(path, "file is too short for a RIFF WAVE descriptor"))
		}
		return fail(classifySessionAudioOpenError(path, err))
	}
	if string(descriptor[0:4]) != "RIFF" || string(descriptor[8:12]) != "WAVE" {
		return fail(sessionWAVFormatError(path, `missing RIFF/WAVE descriptor`))
	}

	fmtSeen := false
	skip := func(count int64) error {
		if count <= 0 {
			return nil
		}
		_, err := r.Seek(count, io.SeekCurrent)
		return err
	}
	for {
		var header [sessionWAVChunkHeaderBytes]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fail(sessionWAVFormatError(path, "missing fmt or data chunk"))
			}
			return fail(classifySessionAudioOpenError(path, err))
		}
		id := string(header[0:4])
		size := int64(binary.LittleEndian.Uint32(header[4:8]))
		switch id {
		case "fmt ":
			if size < sessionWAVFmtChunkMinBytes {
				return fail(sessionWAVFormatError(path, "fmt chunk is truncated"))
			}
			body := make([]byte, sessionWAVFmtChunkMinBytes)
			if _, err := io.ReadFull(r, body); err != nil {
				return fail(sessionWAVFormatError(path, "fmt chunk is truncated"))
			}
			compression := binary.LittleEndian.Uint16(body[0:2])
			channels := binary.LittleEndian.Uint16(body[2:4])
			rate := binary.LittleEndian.Uint32(body[4:8])
			bits := binary.LittleEndian.Uint16(body[14:16])
			switch {
			case compression != sessionWAVAudioFormatPCM:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("WAV compression format %d is not PCM", compression)))
			case channels != audio.Channels:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("channel count is %d; want exactly %d", channels, audio.Channels)))
			case rate != audio.SampleRate:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("sample rate is %d Hz; want exactly %d Hz", rate, audio.SampleRate)))
			case bits != sessionWAVBitsPerSample:
				return fail(sessionWAVFormatError(path, fmt.Sprintf("bit depth is %d; want exactly %d-bit PCM", bits, sessionWAVBitsPerSample)))
			}
			fmtSeen = true
			if err := skip(size - sessionWAVFmtChunkMinBytes); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		case "data":
			if !fmtSeen {
				return fail(sessionWAVFormatError(path, "data chunk appears before fmt chunk"))
			}
			return &sessionWAVSource{path: path, file: r, remaining: size}, nil
		default:
			if err := skip(size); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		}
		// RIFF chunks are word aligned: odd lengths carry one pad byte.
		if size%2 == 1 {
			if err := skip(1); err != nil {
				return fail(classifySessionAudioOpenError(path, err))
			}
		}
	}
}

// ReadFrame fills buf with the next data-chunk frame, zero-padding a final
// short frame. Once the payload is exhausted it returns io.EOF. Each call
// consumes at most FrameSize*2 payload bytes, never the remaining file.
func (s *sessionWAVSource) ReadFrame(ctx context.Context, buf []int16) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(buf) != audio.FrameSize {
		return &audio.FrameSizeError{Operation: "read", Got: len(buf), Want: audio.FrameSize}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &audio.ClosedError{Operation: "read", Path: s.path}
	}
	if s.done {
		return io.EOF
	}

	want := int64(audio.FrameSize * 2)
	count := want
	if s.remaining < count {
		count = s.remaining
	}
	encoded := make([]byte, count)
	if _, err := io.ReadFull(s.file, encoded); err != nil {
		s.done = true
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return &audio.TruncatedPCMError{Path: s.path, Bytes: int(count % 2)}
		}
		return &SessionAudioInputError{Kind: SessionAudioInputRead, Path: s.path, Err: err}
	}
	s.remaining -= count
	if count%2 != 0 {
		s.done = true
		return &audio.TruncatedPCMError{Path: s.path, Bytes: 1}
	}
	clear(buf)
	for index := range int(count) / 2 {
		buf[index] = int16(binary.LittleEndian.Uint16(encoded[index*2:]))
	}
	if s.remaining == 0 {
		s.done = true
	}
	return nil
}

// Close releases the owned file. It is safe to call more than once.
func (s *sessionWAVSource) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.done = true
		err := s.file.Close()
		s.mu.Unlock()
		if err != nil {
			s.closeErr = &SessionAudioInputError{Kind: SessionAudioInputClose, Path: s.path, Err: err}
		}
	})
	return s.closeErr
}
