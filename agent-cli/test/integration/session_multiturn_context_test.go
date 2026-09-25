package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// multiturnPositiveFixture is the committed depth-4 spoken conversation
// capture (turn 1 introduces the codeword ZEPHYR, turn 3 asks for it back);
// multiturnTurnWAVs are its committed per-turn corpus WAVs.
const multiturnPositiveFixture = "multiturn_zephyr_4turn.session.json"

var multiturnTurnWAVs = []string{
	"multiturn_turn1.wav",
	"multiturn_turn2.wav",
	"multiturn_turn3.wav",
	"multiturn_turn4.wav",
}

// multiturnAudioFrames reads one committed per-turn corpus WAV and returns its
// base64-encoded PCM16 frames, zero-padding the final short frame exactly as
// the audio source documents.
func multiturnAudioFrames(t *testing.T, wavPath string) [][]byte {
	t.Helper()

	wavBytes, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("read committed turn WAV %s: %v", wavPath, err)
	}
	_, samples, err := wavio.Read(bytes.NewReader(wavBytes))
	if err != nil {
		t.Fatalf("parse committed turn WAV %s: %v", wavPath, err)
	}
	frame := make([]int16, audio.FrameSize)
	frames := make([][]byte, 0, (len(samples)+len(frame)-1)/len(frame))
	for start := 0; start < len(samples); start += len(frame) {
		clear(frame)
		copy(frame, samples[start:])
		pcm := make([]byte, len(frame)*2)
		for i, sample := range frame {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
		}
		frames = append(frames, pcm)
	}
	return frames
}

func resequenced(record gwtesting.CapturedSessionEvent, sequence *int) gwtesting.CapturedSessionEvent {
	*sequence++
	record.Sequence = *sequence
	record.TimestampMs = int64(*sequence)
	return record
}

func resequencedBatch(records []gwtesting.CapturedSessionEvent, sequence *int) []gwtesting.CapturedSessionEvent {
	out := make([]gwtesting.CapturedSessionEvent, 0, len(records))
	for _, record := range records {
		out = append(out, resequenced(record, sequence))
	}
	return out
}

// syncBuffer is a mutex-protected output buffer because the session command
// writes replay text and terminal status from independent goroutines.
type syncBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	if b.changed == nil {
		b.changed = make(chan struct{}, 1)
	}
	changed := b.changed
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case changed <- struct{}{}:
	default:
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) waitFor(fragment string, timeout time.Duration) bool {
	return b.waitForContext(context.Background(), fragment, timeout)
}

func (b *syncBuffer) waitForContext(ctx context.Context, fragment string, timeout time.Duration) bool {
	b.mu.Lock()
	if b.changed == nil {
		b.changed = make(chan struct{}, 1)
	}
	changed := b.changed
	b.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if strings.Contains(b.String(), fragment) {
			return true
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false
		case <-timer.C:
			return strings.Contains(b.String(), fragment)
		}
	}
}

func captureCopy(t *testing.T, path string) *gwtesting.SessionCapture {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("load session capture %s: %v", path, err)
	}
	return &capture
}

// spokenWireIndices records where the spoken read_image client events sit.
type spokenWireIndices struct {
	appendCount, commitCount                                 int
	responseCreates                                          []int
	lastAppend, commitIndex, functionOutputIndex, imageIndex int
}

func (w *spokenWireIndices) observe(t *testing.T, index int, record gwtesting.CapturedSessionEvent, frames [][]byte) {
	t.Helper()
	payload := readImageSpokenRecordPayload(record)
	switch record.Type {
	case rtEventInputAudioAppend:
		assertSpokenAppendFrame(t, payload, frames, w.appendCount)
		w.appendCount++
		w.lastAppend = index
	case rtEventInputAudioCommit:
		w.commitCount++
		w.commitIndex = index
	case rtEventResponseCreate:
		w.responseCreates = append(w.responseCreates, index)
	case rtEventConversationItemCreate:
		w.observeItem(t, index, payload)
	}
}

func (w *spokenWireIndices) observeItem(t *testing.T, index int, payload []byte) {
	t.Helper()
	var event struct {
		Item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
			ID     string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode spoken conversation item: %v", err)
	}
	switch event.Item.Type {
	case rtItemFunctionCallOutput:
		w.functionOutputIndex = index
		if len(event.Item.Output) > readImageSpokenTextBudget {
			t.Fatalf("spoken function output is %d bytes, want <= %d", len(event.Item.Output), readImageSpokenTextBudget)
		}
	case rtItemMessage:
		if event.Item.ID == readImageToolImageItemID(readImageCallID) {
			w.imageIndex = index
		}
	}
}

// assertSpokenAppendFrame requires the ordinal-th append to carry the
// matching WAV frame unchanged.
func assertSpokenAppendFrame(t *testing.T, payload []byte, frames [][]byte, ordinal int) {
	t.Helper()
	var event struct {
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode spoken audio append %d: %v", ordinal+1, err)
	}
	if ordinal >= len(frames) {
		t.Fatalf("spoken audio append count exceeded WAV frame count %d", len(frames))
	}
	decoded, err := base64.StdEncoding.DecodeString(event.Audio)
	if err != nil {
		t.Fatalf("decode spoken audio append %d: %v", ordinal+1, err)
	}
	if string(decoded) != string(frames[ordinal]) {
		t.Fatalf("spoken audio frame %d changed before provider delivery", ordinal+1)
	}
}
