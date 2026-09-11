package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestValidateInputRejectsConflictsBeforeSourceUse(t *testing.T) {
	called := false
	input := Input{
		Source: audioSourceFunc(func(context.Context, []int16) error {
			called = true
			return nil
		}),
		Reader: bytes.NewReader(nil),
	}
	if err := ValidateInput(input); !errors.Is(err, ErrConflict) {
		t.Fatalf("ValidateInput error = %v, want ErrConflict", err)
	}
	if called {
		t.Fatal("validation read from a conflicting source")
	}
}

func TestNewBufferSourceCopiesAndReadPCMPreservesShortTail(t *testing.T) {
	pcm := pcm16Bytes(11, -22, 33)
	source, err := NewBufferSource(pcm)
	if err != nil {
		t.Fatalf("NewBufferSource: %v", err)
	}
	pcm[0] = 0
	pcm[1] = 0
	got, rate, err := ReadPCM(context.Background(), source, 16000)
	if err != nil {
		t.Fatalf("ReadPCM: %v", err)
	}
	if rate != 16000 {
		t.Fatalf("rate = %d, want 16000", rate)
	}
	if want := pcm16Bytes(11, -22, 33); !bytes.Equal(got, want) {
		t.Fatalf("PCM = %v, want %v", got, want)
	}
}

func TestNewWAVSourcePreservesUnsupportedFormatCause(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.wav")
	if err := os.WriteFile(path, []byte("not a wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close WAV fixture: %v", err)
		}
	}()
	_, err = NewWAVSource(path, file)
	if !errors.Is(err, ErrFormat) || !errors.Is(err, audio.ErrUnsupportedFormat) {
		t.Fatalf("NewWAVSource error = %v, want runtime and audio format identities", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindFormat || typed.Path != path {
		t.Fatalf("typed error = %#v, want format path %q", typed, path)
	}
}

func TestReaderSourceClosePolicyAndExactOnce(t *testing.T) {
	reader := &countingReader{reader: bytes.NewReader(pcm16Bytes(1, 2))}
	source, err := NewReaderSource(reader, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := reader.closes.Load(); got != 1 {
		t.Fatalf("reader close count = %d, want 1", got)
	}

	callerOwned := &countingReader{reader: bytes.NewReader(nil)}
	ownedSource, err := NewReaderSource(callerOwned, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ownedSource.Close(); err != nil {
		t.Fatalf("caller-owned Close: %v", err)
	}
	if got := callerOwned.closes.Load(); got != 0 {
		t.Fatalf("caller-owned reader close count = %d, want 0", got)
	}
}

func TestReaderSourceRejectsUninterruptibleReaderWithCause(t *testing.T) {
	reader := uninterruptibleReader{}
	source, err := NewReaderSource(reader, false)
	if err != nil {
		t.Fatal(err)
	}
	err = source.ReadFrame(context.Background(), make([]int16, audio.FrameSize))
	if !errors.Is(err, ErrUninterruptible) {
		t.Fatalf("ReadFrame error = %v, want ErrUninterruptible", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("uninterruptible reader fabricated cancellation")
	}
}

func TestReaderSourceDeadlineSetupPreservesCause(t *testing.T) {
	deadlineErr := errors.New("deadline setup failed")
	reader := &testDeadlineReader{err: deadlineErr}
	source, err := NewReaderSource(reader, false)
	if err != nil {
		t.Fatal(err)
	}
	err = source.ReadFrame(context.Background(), make([]int16, audio.FrameSize))
	if !errors.Is(err, ErrUninterruptible) || !errors.Is(err, deadlineErr) {
		t.Fatalf("ReadFrame error = %v, want interruptibility and setup causes", err)
	}
}

func TestContractsValidateAndAdapt(t *testing.T) {
	service := New(nil)
	if err := service.ValidateSpec(InputSpec{Path: "  "}, nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("blank spec error = %v, want ErrEmpty", err)
	}
	if err := service.ValidateSpec(InputSpec{Path: "audio.raw", DevicePresent: true}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("device spec error = %v, want ErrConflict", err)
	}
	if err := service.ValidateSpec(InputSpec{Path: "audio.raw"}, nil); err != nil {
		t.Fatalf("valid spec error = %v", err)
	}

	adapted := service.AdaptError(&Error{Kind: KindUninterruptible, Path: "old", Err: errors.New("blocked")}, "new", KindRead, nil)
	var typed *Error
	if !errors.As(adapted, &typed) || typed.Kind != KindRead || typed.Path != "new" || !errors.Is(adapted, ErrUninterruptible) {
		t.Fatalf("adapted error = %#v (%v), want read/new with uninterruptible cause", typed, adapted)
	}
	conflictCause := errors.New("conflict cause")
	adapted = service.AdaptError(&Error{Kind: KindConflict, Path: "old", Err: ErrConflict}, "new", KindRead, conflictCause)
	if !errors.Is(adapted, conflictCause) || !errors.As(adapted, &typed) || typed.Path != "new" {
		t.Fatalf("adapted conflict = %v, want path and cause preserved", adapted)
	}
	raw := errors.New("raw")
	if adapted := service.AdaptError(raw, "path", KindRead, nil); !errors.Is(adapted, raw) {
		t.Fatal("AdaptError changed an untyped error")
	}
}

func TestContractsClassifyAndNegotiateRates(t *testing.T) {
	service := New(nil)
	var typed *Error
	for _, test := range []struct {
		name   string
		cause  error
		kind   ErrorKind
		format bool
	}{
		{name: "missing", cause: fs.ErrNotExist, kind: KindMissing},
		{name: "malformed", cause: wavio.ErrMalformed, kind: KindFormat, format: true},
		{name: "nil stream", cause: audio.ErrNilStream, kind: KindUnreadable},
		{name: "other", cause: errors.New("read open"), kind: KindUnreadable},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := service.ClassifyOpenError("audio.raw", test.cause)
			if !errors.As(err, &typed) || typed.Kind != test.kind || typed.Path != "audio.raw" {
				t.Fatalf("classified error = %#v (%v), want %s", typed, err, test.kind)
			}
			if test.format && !errors.Is(err, audio.ErrUnsupportedFormat) {
				t.Fatalf("format error = %v, want audio unsupported-format identity", err)
			}
		})
	}

	if service.PreferRate(24000, 16000) != 24000 || service.PreferRate(0, 16000) != 16000 {
		t.Fatal("PreferRate did not prefer a declared rate")
	}
	conflictCause := errors.New("conflict cause")
	if got, err := service.NegotiateRate(0, 0, 0, nil); err != nil || got != audio.SampleRate {
		t.Fatalf("default NegotiateRate = %d, %v", got, err)
	}
	if got, err := service.NegotiateRate(16000, 24000, 0, conflictCause); !errors.Is(err, conflictCause) || got != 0 {
		t.Fatalf("conflicting NegotiateRate = %d, %v", got, err)
	}
	if got, err := service.NegotiateRate(0, 24000, 16000, nil); err != nil || got != 24000 {
		t.Fatalf("output NegotiateRate = %d, %v", got, err)
	}
	if err := service.Validate(Input{Buffer: pcm16Bytes(1), SourceSampleRate: -1}); !errors.Is(err, ErrFormat) {
		t.Fatalf("negative-rate Validate error = %v, want ErrFormat", err)
	}
}

func TestTurnPoliciesAndTerminationCancellation(t *testing.T) {
	service := New(nil)
	closeMessage := messages.StreamMessage{Type: messages.StreamTypeSessionClose}
	endMessage := messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}
	if !service.ShouldStop(closeMessage, audioinput.TurnStopPolicy{}) || service.ShouldStop(endMessage, audioinput.TurnStopPolicy{}) {
		t.Fatal("non-awaiting stop policy mismatch")
	}
	if !service.ShouldStop(endMessage, audioinput.TurnStopPolicy{AwaitingResponse: true}) {
		t.Fatal("message end should stop an admitted response")
	}
	if service.ShouldStop(endMessage, audioinput.TurnStopPolicy{AwaitingResponse: true, MessageEndAdmitted: func() bool { return false }}) {
		t.Fatal("unadmitted message end stopped the response")
	}
	if service.ShouldStop(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleTool}, audioinput.TurnStopPolicy{AwaitingResponse: true, RequireAssistantResponse: true, AssistantResponseCompleted: func() bool { return true }}) {
		t.Fatal("tool message end satisfied assistant-response policy")
	}
	if !service.ShouldStop(endMessage, audioinput.TurnStopPolicy{AwaitingResponse: true, RequireAssistantResponse: true, AssistantResponseCompleted: func() bool { return true }}) {
		t.Fatal("completed assistant response did not stop")
	}
	terminal := func(messages.StreamMessage) bool { return true }
	if !service.ShouldStop(messages.StreamMessage{Type: messages.StreamTypeTextDelta}, audioinput.TurnStopPolicy{AwaitingResponse: true, WaitForClose: true, Terminal: terminal}) || !service.ShouldStop(closeMessage, audioinput.TurnStopPolicy{AwaitingResponse: true, WaitForClose: true}) {
		t.Fatal("wait-for-close policy mismatch")
	}
	if !service.ShouldStop(endMessage, audioinput.TurnStopPolicy{AwaitingResponse: true, TerminalToolFailure: func() bool { return true }}) || !service.ShouldStop(endMessage, audioinput.TurnStopPolicy{AwaitingResponse: true, TerminalScheduledFailure: func() bool { return true }}) {
		t.Fatal("terminal failure policy did not stop")
	}
	if !service.IsExpectedCancellation(context.Canceled) || !service.IsExpectedCancellation(context.DeadlineExceeded) || service.IsExpectedCancellation(errors.Join(context.Canceled, ErrEndOfTurnLost)) {
		t.Fatal("unexpected cancellation classification")
	}
	independent := errors.New("independent")
	joined := service.JoinTerminationErrors(independent, context.Canceled)
	if !errors.Is(joined, independent) || errors.Is(joined, context.Canceled) {
		t.Fatalf("termination join = %v, want only independent error", joined)
	}
}

func TestScheduleAndInvocationAdapters(t *testing.T) {
	load := func(string) ([]byte, int, error) { return pcm16Bytes(1, 2), audio.SampleRate, nil }
	prepared, err := PrepareScheduledAs([]string{"first"}, load, func(input ScheduledInput) ScheduledInput { return input })
	if err != nil || len(prepared) != 1 || prepared[0].AfterCompletedTurns != 0 || !prepared[0].EndOfTurn {
		t.Fatalf("PrepareScheduledAs = %#v, %v", prepared, err)
	}
	inputs := []ScheduledInput{{PCM: pcm16Frame(1), SourceSampleRate: audio.SampleRate, EndOfTurn: true}}
	converted, err := ConvertScheduled(New(nil), inputs, 24000, func(input ScheduledInput) ScheduledInput { return input }, func(input ScheduledInput) ScheduledInput { return input })
	if err != nil || len(converted) != 1 || converted[0].SourceSampleRate != 24000 || len(converted[0].PCM) == len(inputs[0].PCM) {
		t.Fatalf("ConvertScheduled adapter = %#v, %v", converted, err)
	}

	events := make(chan InvocationEvent, 2)
	var parent context.Context
	released, cancel := New(nil).ReleaseOnInvocation(parent, events, inputs, "target")
	events <- InvocationEvent{Dispatched: true, InvocationID: "id", ToolName: "other"}
	events <- InvocationEvent{Dispatched: true, InvocationID: "id", ToolName: "target"}
	got, ok := <-released
	if !ok || !bytes.Equal(got.PCM, inputs[0].PCM) {
		t.Fatalf("released input = %#v, open=%v", got, ok)
	}
	inputs[0].PCM[0] ^= 0xff
	if bytes.Equal(got.PCM, inputs[0].PCM) {
		t.Fatal("released input aliases caller PCM")
	}
	if _, ok := <-released; ok {
		t.Fatal("release channel remained open")
	}
	cancel()

	closedEvents := make(chan InvocationEvent)
	close(closedEvents)
	closed, _ := New(nil).ReleaseOnInvocation(context.Background(), closedEvents, inputs, "target")
	if _, ok := <-closed; ok {
		t.Fatal("closed event stream released input")
	}
}

func TestReaderPortsAndContextAdapter(t *testing.T) {
	source, err := NewReaderSource(bytes.NewReader(pcm16Bytes(9, 10)), false)
	if err != nil {
		t.Fatal(err)
	}
	destination := make([]int16, 4)
	count, err := source.ReadSamples(context.Background(), destination)
	if err != nil || count != 2 || destination[0] != 9 || destination[1] != 10 {
		t.Fatalf("ReadSamples count=%d err=%v destination=%v", count, err, destination[:2])
	}
	if count, err := source.ReadSamples(context.Background(), destination); !errors.Is(err, io.EOF) || count != 0 {
		t.Fatalf("ReadSamples after EOF count=%d err=%v", count, err)
	}
	if count, err := source.ReadSamples(context.Background(), nil); err != nil || count != 0 {
		t.Fatalf("empty ReadSamples count=%d err=%v", count, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadSamples(context.Background(), destination); !errors.Is(err, ErrRead) {
		t.Fatalf("closed ReadSamples error=%v, want ErrRead", err)
	}

	contextual, err := NewReaderSource(&contextChunkReader{data: pcm16Frame(11)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := contextual.ReadFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil {
		t.Fatalf("contextual ReadFrame: %v", err)
	}
}

func TestReaderDeadlineAdapters(t *testing.T) {
	deadline := &recordingDeadlineReader{reader: bytes.NewReader(pcm16Frame(12))}
	deadlineSource, err := NewReaderSource(deadline, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := deadlineSource.ReadFrame(context.Background(), make([]int16, audio.FrameSize)); err != nil || len(deadline.deadlines) != 2 {
		t.Fatalf("deadline ReadFrame error=%v deadlines=%d", err, len(deadline.deadlines))
	}
	one := &recordingDeadlineReader{reader: bytes.NewReader(pcm16Bytes(13))}
	oneSource, err := NewReaderSource(one, false)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if count, err := oneSource.Read(buf); err != nil || count != 2 || len(one.deadlines) != 2 {
		t.Fatalf("deadline Read count=%d err=%v deadlines=%d", count, err, len(one.deadlines))
	}

	fallback := &deadlineClosingReader{reader: bytes.NewReader(pcm16Bytes(14)), err: errors.New("deadline unavailable")}
	fallbackSource, err := NewReaderSource(fallback, true)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := fallbackSource.Read(buf); err != nil || count != 2 {
		t.Fatalf("deadline close fallback count=%d err=%v", count, err)
	}
	if !isTimeout(timeoutError{}) || isTimeout(errors.New("not timeout")) {
		t.Fatal("isTimeout classification mismatch")
	}
}

func TestFrameReadPCMAndSourceOwnershipBranches(t *testing.T) {
	frameCalls := 0
	frameSource := audioSourceFunc(func(_ context.Context, destination []int16) error {
		if frameCalls > 0 {
			return io.EOF
		}
		frameCalls++
		for index := range destination {
			destination[index] = int16(index)
		}
		return nil
	})
	pcm, rate, err := ReadPCM(context.Background(), frameSource, 12000)
	if err != nil || len(pcm) != audio.FrameSize*2 || rate != 12000 {
		t.Fatalf("frame ReadPCM len=%d rate=%d err=%v", len(pcm), rate, err)
	}
	endRead := false
	endSource := audioSourceFunc(func(_ context.Context, destination []int16) error {
		if !endRead {
			endRead = true
			destination[0] = 1
			return nil
		}
		return audio.ErrEndOfTurn
	})
	if pcm, _, err := ReadPCM(context.Background(), endSource, audio.SampleRate); err != nil || len(pcm) == 0 {
		t.Fatalf("end-of-turn ReadPCM len=%d err=%v", len(pcm), err)
	}
	failing := errors.New("frame read failed")
	if _, _, err := ReadPCM(context.Background(), audioSourceFunc(func(context.Context, []int16) error { return failing }), audio.SampleRate); !errors.Is(err, failing) || !errors.Is(err, ErrRead) {
		t.Fatalf("failing ReadPCM error=%v", err)
	}
	if _, _, err := ReadPCM(context.Background(), audioSourceFunc(func(context.Context, []int16) error { return io.EOF }), audio.SampleRate); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty frame ReadPCM error=%v", err)
	}
	if SourceRate(&ratedAudioSource{rate: 24000}, audio.SampleRate) != 24000 || SourceRate(frameSource, audio.SampleRate) != audio.SampleRate {
		t.Fatal("SourceRate fallback mismatch")
	}
	closeErr := errors.New("source close")
	closerErr := errors.New("extra close")
	joined := CloseSources(&ratedAudioSource{closeErr: closeErr}, &testCloser{err: closerErr}, nil)
	if !errors.Is(joined, closeErr) || !errors.Is(joined, closerErr) {
		t.Fatalf("CloseSources error=%v", joined)
	}
	if _, err := NewBufferSource(nil); !errors.Is(err, ErrEmpty) {
		t.Fatalf("nil buffer source error=%v", err)
	}
	if _, err := NewBufferSource([]byte{1}); !errors.Is(err, ErrPCM16Truncated) {
		t.Fatalf("odd buffer source error=%v", err)
	}
	if _, err := NewReaderSource(nil, false); !errors.Is(err, audio.ErrNilStream) {
		t.Fatalf("nil reader source error=%v", err)
	}
}

func TestOpenWAVFileClosesOnParseFailureAndSuccess(t *testing.T) {
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, audio.SampleRate, []int16{1, 2}); err != nil {
		t.Fatal(err)
	}
	file := &testReadSeekCloser{Reader: bytes.NewReader(encoded.Bytes())}
	source, err := OpenWAVFile("sample.wav", func(string) (*testReadSeekCloser, error) { return file, nil })
	if err != nil || source == nil {
		t.Fatalf("OpenWAVFile success source=%v err=%v", source, err)
	}
	if err := source.Close(); err != nil || file.closes.Load() != 1 {
		t.Fatalf("opened WAV close error=%v closes=%d", err, file.closes.Load())
	}
	broken := &testReadSeekCloser{Reader: bytes.NewReader([]byte("not wav"))}
	if _, err := OpenWAVFile("broken.wav", func(string) (*testReadSeekCloser, error) { return broken, nil }); !errors.Is(err, ErrFormat) || broken.closes.Load() != 1 {
		t.Fatalf("broken OpenWAVFile error=%v closes=%d", err, broken.closes.Load())
	}
}

func pcm16Bytes(samples ...int16) []byte {
	result := make([]byte, len(samples)*2)
	for index, sample := range samples {
		result[index*2] = byte(sample)
		result[index*2+1] = byte(uint16(sample) >> 8)
	}
	return result
}

type audioSourceFunc func(context.Context, []int16) error

func (f audioSourceFunc) ReadFrame(ctx context.Context, destination []int16) error {
	return f(ctx, destination)
}

func (audioSourceFunc) Close() error { return nil }

type countingReader struct {
	reader io.Reader
	closes atomic.Int32
}

func (r *countingReader) Read(destination []byte) (int, error) { return r.reader.Read(destination) }
func (r *countingReader) Close() error {
	r.closes.Add(1)
	return nil
}

type uninterruptibleReader struct{}

func (uninterruptibleReader) Read([]byte) (int, error) { return 0, io.EOF }

type testDeadlineReader struct{ err error }

func (r *testDeadlineReader) Read([]byte) (int, error)        { return 0, io.EOF }
func (r *testDeadlineReader) SetReadDeadline(time.Time) error { return r.err }

type contextChunkReader struct {
	data []byte
	pos  int
}

func (r *contextChunkReader) Read(destination []byte) (int, error) {
	return r.ReadContext(context.Background(), destination)
}

func (r *contextChunkReader) ReadContext(ctx context.Context, destination []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if r.pos == len(r.data) {
		return 0, io.EOF
	}
	count := len(destination)
	if count > 7 {
		count = 7
	}
	if remaining := len(r.data) - r.pos; count > remaining {
		count = remaining
	}
	copy(destination, r.data[r.pos:r.pos+count])
	r.pos += count
	return count, nil
}

type recordingDeadlineReader struct {
	reader    io.Reader
	deadlines []time.Time
}

func (r *recordingDeadlineReader) Read(destination []byte) (int, error) {
	return r.reader.Read(destination)
}
func (r *recordingDeadlineReader) SetReadDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

type deadlineClosingReader struct {
	reader io.Reader
	err    error
	closes atomic.Int32
}

func (r *deadlineClosingReader) Read(destination []byte) (int, error) {
	return r.reader.Read(destination)
}
func (r *deadlineClosingReader) SetReadDeadline(time.Time) error { return r.err }
func (r *deadlineClosingReader) Close() error {
	r.closes.Add(1)
	return nil
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type ratedAudioSource struct {
	rate     int
	closeErr error
}

func (s *ratedAudioSource) ReadFrame(context.Context, []int16) error { return io.EOF }
func (s *ratedAudioSource) Close() error                             { return s.closeErr }
func (s *ratedAudioSource) SampleRate() int                          { return s.rate }

type testCloser struct{ err error }

func (c *testCloser) Close() error { return c.err }

type testReadSeekCloser struct {
	*bytes.Reader
	closes atomic.Int32
}

func (r *testReadSeekCloser) Close() error {
	r.closes.Add(1)
	return nil
}
