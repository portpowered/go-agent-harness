package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// countingReadSeeker records how many bytes the WAV source actually consumed
// so tests can prove frame-by-frame streaming instead of wholesale buffering.
type countingReadSeeker struct {
	r        io.ReadSeeker
	consumed int64
}

func (c *countingReadSeeker) Read(p []byte) (int, error) {
	count, err := c.r.Read(p)
	c.consumed += int64(count)
	return count, err
}

func (c *countingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.r.Seek(offset, whence)
}

func (c *countingReadSeeker) Close() error { return nil }

// nopReadSeekCloser adapts an in-memory ReadSeeker for parser tests.
type nopReadSeekCloser struct {
	io.ReadSeeker
}

func (nopReadSeekCloser) Close() error { return nil }

// sessionWAVTestBytes builds a canonical 44-byte-header PCM16 mono 16 kHz WAV
// wrapping dataChunk, then applies byte-level mutations for rejection tests.
// Header layout: RIFF(0) WAVE(8) "fmt "(12) size(16) format(20) channels(22)
// sampleRate(24) byteRate(28) blockAlign(32) bits(34) "data"(36) size(40).
func sessionWAVTestBytes(t *testing.T, dataChunk []byte, mutate func(header []byte)) []byte {
	t.Helper()
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	header[4] = byte(36 + len(dataChunk))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	header[16] = 16 // fmt chunk size
	header[20] = 1  // PCM
	header[22] = 1  // mono
	header[24] = 0x80
	header[25] = 0x3e // 16000 Hz little-endian
	header[28] = 0
	header[29] = 0x7d // byte rate
	header[32] = 2    // block align
	header[34] = 16   // bits per sample
	copy(header[36:40], "data")
	header[40] = byte(len(dataChunk))
	header[41] = byte(len(dataChunk) >> 8)
	if mutate != nil {
		mutate(header)
	}
	return append(header, dataChunk...)
}

func TestNewSessionWAVSourceStreamsFrameByFrame(t *testing.T) {
	frameCount := 5
	data := make([]byte, frameCount*audio.FrameSize*2)
	for index := range data {
		data[index] = byte(index%251 + 1)
	}
	wav := sessionWAVTestBytes(t, data, nil)
	reader := &countingReadSeeker{r: bytes.NewReader(wav)}
	source, err := newSessionWAVSource("utterance.wav", reader)
	if err != nil {
		t.Fatalf("open wav: %v", err)
	}
	defer func() { _ = source.Close() }()

	const headerSize = 44
	if reader.consumed != headerSize {
		t.Fatalf("open consumed %d bytes, want only the %d-byte header", reader.consumed, headerSize)
	}

	frame := make([]int16, audio.FrameSize)
	ctx := context.Background()
	for frameIndex := range frameCount {
		if err := source.ReadFrame(ctx, frame); err != nil {
			t.Fatalf("frame %d: %v", frameIndex, err)
		}
		wantConsumed := int64(headerSize + (frameIndex+1)*audio.FrameSize*2)
		if reader.consumed != wantConsumed {
			t.Fatalf("after frame %d consumed %d bytes, want %d; payload was not streamed incrementally", frameIndex, reader.consumed, wantConsumed)
		}
		for sampleIndex := range frame {
			offset := frameIndex*audio.FrameSize*2 + sampleIndex*2
			want := int16(uint16(data[offset]) | uint16(data[offset+1])<<8)
			if frame[sampleIndex] != want {
				t.Fatalf("frame %d sample %d = %d, want %d", frameIndex, sampleIndex, frame[sampleIndex], want)
			}
		}
	}
	if err := source.ReadFrame(ctx, frame); !errors.Is(err, io.EOF) {
		t.Fatalf("frame after payload end = %v, want io.EOF", err)
	}
	if reader.consumed != int64(len(wav)) {
		t.Fatalf("EOF read consumed beyond payload: %d vs %d", reader.consumed, len(wav))
	}
}

func TestSessionWAVSourceZeroPadsFinalShortFrame(t *testing.T) {
	samplePairs := audio.FrameSize + 6
	data := make([]byte, samplePairs*2)
	for index := range data {
		data[index] = byte(index%13 + 1)
	}
	wav := sessionWAVTestBytes(t, data, nil)
	reader := &countingReadSeeker{r: bytes.NewReader(wav)}
	source, err := newSessionWAVSource("short.wav", reader)
	if err != nil {
		t.Fatalf("open wav: %v", err)
	}
	defer func() { _ = source.Close() }()

	frame := make([]int16, audio.FrameSize)
	ctx := context.Background()
	if err := source.ReadFrame(ctx, frame); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if err := source.ReadFrame(ctx, frame); err != nil {
		t.Fatalf("final short frame: %v", err)
	}
	remaining := samplePairs - audio.FrameSize
	shortBase := audio.FrameSize * 2
	for index := range remaining {
		offset := shortBase + index*2
		want := int16(uint16(data[offset]) | uint16(data[offset+1])<<8)
		if frame[index] != want {
			t.Fatalf("short-frame sample %d = %d, want %d", index, frame[index], want)
		}
	}
	for index := remaining; index < audio.FrameSize; index++ {
		if frame[index] != 0 {
			t.Fatalf("zero padding sample %d = %d, want 0", index, frame[index])
		}
	}
	if err := source.ReadFrame(ctx, frame); !errors.Is(err, io.EOF) {
		t.Fatalf("read after final frame = %v, want io.EOF", err)
	}
}

func TestNewSessionWAVSourceRejectsMalformedHeaders(t *testing.T) {
	payload := make([]byte, audio.FrameSize*2)
	tests := []struct {
		name    string
		content []byte
		wantMsg string
	}{
		{
			name:    "empty file",
			content: nil,
			wantMsg: "descriptor",
		},
		{
			name:    "not riff",
			content: []byte("NOTAWAVE........"),
			wantMsg: "RIFF",
		},
		{
			name: "missing data chunk",
			content: sessionWAVTestBytes(t, payload, func(header []byte) {
				copy(header[36:40], "fakt")
			})[:44],
			wantMsg: "missing fmt or data",
		},
		{
			name: "wrong sample rate",
			content: sessionWAVTestBytes(t, payload, func(header []byte) {
				header[24] = 0x00
			}),
			wantMsg: "sample rate",
		},
		{
			name: "stereo",
			content: sessionWAVTestBytes(t, payload, func(header []byte) {
				header[22] = 2
			}),
			wantMsg: "channel count",
		},
		{
			name: "eight bit",
			content: sessionWAVTestBytes(t, payload, func(header []byte) {
				header[34] = 8
			}),
			wantMsg: "bit depth",
		},
		{
			name: "non pcm compression",
			content: sessionWAVTestBytes(t, payload, func(header []byte) {
				header[20] = 6
			}),
			wantMsg: "not PCM",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, err := newSessionWAVSource(tc.name+".wav", nopReadSeekCloser{bytes.NewReader(tc.content)})
			if err == nil {
				_ = source.Close()
				t.Fatal("expected format rejection")
			}
			var typed *SessionAudioInputError
			if !errors.As(err, &typed) || typed.Kind != SessionAudioInputFormat {
				t.Fatalf("error = %v, want kind %s", err, SessionAudioInputFormat)
			}
			if !errors.Is(err, audio.ErrUnsupportedFormat) {
				t.Fatalf("error = %v, want errors.Is(audio.ErrUnsupportedFormat)", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("error = %v, want context %q", err, tc.wantMsg)
			}
		})
	}
}

func TestOpenSessionWAVSourceClassifiesFileErrors(t *testing.T) {
	if _, err := openSessionWAVSource(filepath.Join(t.TempDir(), "missing.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v, want errors.Is(os.ErrNotExist)", err)
	} else if !errors.Is(err, ErrSessionAudioInputMissing) {
		t.Fatalf("missing file error = %v, want missing kind sentinel", err)
	}

	directory := filepath.Join(t.TempDir(), "directory.wav")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := openSessionWAVSource(directory)
	if err == nil {
		t.Fatal("expected directory rejection")
	}
	if !errors.Is(err, ErrSessionAudioInputUnreadable) && !errors.Is(err, ErrSessionAudioInputFormat) {
		t.Fatalf("directory error = %v, want unreadable or format kind", err)
	}
}

func TestSessionWAVSourceCloseIsOnceAndGuardsReads(t *testing.T) {
	data := make([]byte, audio.FrameSize*2)
	wav := sessionWAVTestBytes(t, data, nil)
	reader := &countingReadSeeker{r: bytes.NewReader(wav)}
	source, err := newSessionWAVSource("close.wav", reader)
	if err != nil {
		t.Fatalf("open wav: %v", err)
	}
	frame := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(context.Background(), frame); err != nil {
		t.Fatalf("pre-close read: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second close must stay nil: %v", err)
	}
	if err := source.ReadFrame(context.Background(), frame); !errors.Is(err, audio.ErrClosed) {
		t.Fatalf("post-close read = %v, want errors.Is(audio.ErrClosed)", err)
	}
}

// TestShouldStopAudioInputAwaitingResponseSemantics pins the explicit
// awaiting-response stop rules: local audio EOF alone never stops the run,
// mid-response deltas never stop it, and only terminal response frames,
// provider errors, or SESSION.CLOSE end an awaiting session.
func TestShouldStopAudioInputAwaitingResponseSemantics(t *testing.T) {
	cases := []struct {
		name         string
		msg          messages.StreamMessageType
		awaiting     bool
		waitForClose bool
		wantStop     bool
	}{
		{"mid-response text end does not stop", messages.StreamTypeTextEnd, true, false, false},
		{"audio delta does not stop", messages.StreamTypeAudioDelta, true, false, false},
		{"transcript delta does not stop", messages.StreamTypeTranscriptDelta, true, false, false},
		{"message end from response.done stops", messages.StreamTypeMessageEnd, true, false, true},
		{"wait for close keeps response.done open", messages.StreamTypeMessageEnd, true, true, false},
		{"provider error stops", messages.StreamTypeError, true, false, true},
		{"session close stops", messages.StreamTypeSessionClose, true, false, true},
		{"before end-of-turn message end does not stop", messages.StreamTypeMessageEnd, false, false, false},
		{"before end-of-turn session close stops", messages.StreamTypeSessionClose, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldStopAudioInputSessionLoop(messages.StreamMessage{Type: tc.msg}, sessionLoopOptions{WaitForClose: tc.waitForClose}, false, tc.awaiting)
			if got != tc.wantStop {
				t.Fatalf("stop = %v; want %v", got, tc.wantStop)
			}
		})
	}
}

func TestShouldStopAudioInputSessionLoopWaitsForFinalAssistantResponse(t *testing.T) {
	observer := newSessionProgressObserver(nil, nil, "openai", "gpt-realtime")
	observer.setToolResultsEnabled(true)
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewToolCallEndValue("call-1", "lookup", `{}`),
	})

	intermediateProviderEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(intermediateProviderEnd)
	if shouldStopAudioInputSessionLoop(intermediateProviderEnd, sessionLoopOptions{
		RequireAssistantResponse: true,
		observer:                 observer,
	}, false, true) {
		t.Fatal("provider tool-call MESSAGE.END stopped before the tool result and follow-up response")
	}

	toolRunnerEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleTool,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(toolRunnerEnd)
	if shouldStopAudioInputSessionLoop(toolRunnerEnd, sessionLoopOptions{
		RequireAssistantResponse: true,
		observer:                 observer,
	}, false, true) {
		t.Fatal("ToolRunner RoleTool MESSAGE.END stopped before the follow-up response")
	}

	observer.noteToolResultAccepted("call-1")
	finalAssistantEnd := messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	observer.observe(finalAssistantEnd)
	if !shouldStopAudioInputSessionLoop(finalAssistantEnd, sessionLoopOptions{
		RequireAssistantResponse: true,
		observer:                 observer,
	}, false, true) {
		t.Fatal("completed non-tool assistant MESSAGE.END did not stop the awaiting audio session")
	}
}

// TestNewSessionWAVSourceResamples24kHz proves the 24 kHz corpus path: the
// source decodes the payload once, resamples it to the harness rate, and
// serves harness-rate frames with the established zero-pad/EOF semantics.
func TestNewSessionWAVSourceResamples24kHz(t *testing.T) {
	input := make([]int16, 24000) // one second of 24 kHz audio
	for i := range input {
		input[i] = int16(i % 327)
	}
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, wavio.Rate24kHz, input); err != nil {
		t.Fatalf("encode 24 kHz wav: %v", err)
	}
	source, err := newSessionWAVSource("utterance_24k.wav", struct {
		io.ReadSeeker
		io.Closer
	}{bytes.NewReader(encoded.Bytes()), nopReadSeekCloser{}})
	if err != nil {
		t.Fatalf("open 24 kHz wav: %v", err)
	}
	defer func() { _ = source.Close() }()

	var got []int16
	frame := make([]int16, audio.FrameSize)
	ctx := context.Background()
	for {
		err := source.ReadFrame(ctx, frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		got = append(got, frame...)
	}
	want, err := wavio.Resample(input, wavio.Rate24kHz, audio.SampleRate)
	if err != nil {
		t.Fatalf("reference resample: %v", err)
	}
	if len(got) < len(want) || len(got)-len(want) >= audio.FrameSize {
		t.Fatalf("resampled stream delivered %d samples, want ~%d (one padded frame tolerance)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resampled sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}
