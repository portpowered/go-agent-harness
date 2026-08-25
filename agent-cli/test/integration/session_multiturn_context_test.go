package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// The depth-4 multiturn fixtures encode a four-turn spoken conversation in
// which turn 1 introduces the codeword ZEPHYR, turn 2 is unrelated filler,
// turn 3 asks for the word back, and turn 4 follows up on the answer. The
// recorded turn-3/turn-4 responses therefore depend on information stated in
// turn 1, and the negative-control fixture differs only in that its later
// responses omit that fact.
const (
	multiturnPositiveFixture = "multiturn_zephyr_4turn.session.json"
	multiturnNegativeFixture = "multiturn_zephyr_no_carry.session.json"
	multiturnTurnCount       = 4
	multiturnCodeword        = "ZEPHYR"
)

var multiturnTurnWAVs = []string{
	"multiturn_turn1.wav",
	"multiturn_turn2.wav",
	"multiturn_turn3.wav",
	"multiturn_turn4.wav",
}

// multiturnSliceFixture materializes one single-turn replay fixture derived
// from the committed conversation capture: the shared session handshake,
// exactly one turn's records, and a terminal provider close. The committed
// fixture redacts raw audio fields (repo fixture-hygiene policy), so the
// exact PCM16 frames of the committed per-turn corpus WAVs are injected here,
// mirroring the depth-3 proof's runtime wire-capture assembly. One CLI session
// invocation replays one slice, so the whole conversation is driven one
// invocation per turn over the same committed fixture wiring.
func multiturnSliceFixture(t *testing.T, fixturePath string, turn int) string {
	t.Helper()

	capture := captureCopy(t, fixturePath)
	injectMultiturnAudioFrames(t, capture)

	var prefix []gwtesting.CapturedSessionEvent
	var suffix []gwtesting.CapturedSessionEvent
	var turns [][]gwtesting.CapturedSessionEvent
	for _, record := range capture.Records {
		switch record.Type {
		case "session.update", "session.created":
			prefix = append(prefix, record)
		case "session.closed":
			suffix = append(suffix, record)
		default:
			if len(turns) == 0 || hasTurnTerminated(turns[len(turns)-1]) {
				turns = append(turns, nil)
			}
			if record.Type == "response.done" {
				turns[len(turns)-1] = append(turns[len(turns)-1], record)
				continue
			}
			turns[len(turns)-1] = append(turns[len(turns)-1], record)
		}
	}
	if len(turns) != multiturnTurnCount {
		t.Fatalf("fixture turn partition produced %d turns, want %d", len(turns), multiturnTurnCount)
	}

	sequence := 0
	slice := make([]gwtesting.CapturedSessionEvent, 0, len(prefix)+len(turns[turn-1])+len(suffix))
	for _, record := range prefix {
		slice = append(slice, resequenced(record, &sequence))
	}
	slice = append(slice, resequencedBatch(turns[turn-1], &sequence)...)
	slice = append(slice, resequenced(suffix[0], &sequence))

	capture.Records = slice
	path := filepath.Join(t.TempDir(), fmt.Sprintf("multiturn_slice_turn%d.session.json", turn))
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal turn slice: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write turn slice: %v", err)
	}
	return path
}

func hasTurnTerminated(records []gwtesting.CapturedSessionEvent) bool {
	for _, record := range records {
		if record.Type == "response.done" {
			return true
		}
	}
	return false
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

// injectMultiturnAudioFrames fills every input_audio_buffer.append record of
// the redacted committed capture with the exact frame bytes of the committed
// per-turn WAV corpus, in turn order. The replay transport validates every
// outbound append byte-for-byte, so any drift between the WAVs and this
// injection fails the run.
func injectMultiturnAudioFrames(t *testing.T, capture *gwtesting.SessionCapture) {
	t.Helper()

	framesPerTurn := make([][][]byte, 0, multiturnTurnCount)
	for _, name := range multiturnTurnWAVs {
		framesPerTurn = append(framesPerTurn, multiturnAudioFrames(t, locateCLIFixture(t, name)))
	}

	turn, frame := 0, 0
	for i := range capture.Records {
		record := &capture.Records[i]
		if record.Type != "input_audio_buffer.append" {
			continue
		}
		if turn >= len(framesPerTurn) || frame >= len(framesPerTurn[turn]) {
			t.Fatalf("committed fixture expects more audio frames than the committed WAV corpus provides (turn %d frame %d)", turn+1, frame+1)
		}
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode append payload: %v", err)
		}
		payload["audio"] = base64.StdEncoding.EncodeToString(framesPerTurn[turn][frame])
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode append payload: %v", err)
		}
		record.Payload = encoded
		frame++
		if frame == len(framesPerTurn[turn]) {
			turn++
			frame = 0
		}
	}
	if turn != multiturnTurnCount {
		t.Fatalf("committed fixture consumed %d turns of audio, want %d", turn, multiturnTurnCount)
	}
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
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runMultiturnTurn drives the shipped 'session' command surface over the
// record/replay transport with --audio-in for exactly one conversation turn
// and returns the captured command stdout. It waits for the terminal close
// marker so asynchronous terminal formatting is always observed.
func runMultiturnTurn(t *testing.T, fixturePath, wavPath string) (string, error) {
	t.Helper()

	stdout := &syncBuffer{}
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil).Generate()
	cmd.SetOut(stdout)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs([]string{"--replay", fixturePath, "--audio-in", wavPath})
	err := cmd.ExecuteContext(t.Context())
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), "[session closed:") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return stdout.String(), err
}

// assertMultiturnContextCarried is the shared depth-4 assertion. It fails
// unless every turn completed, the later-turn outputs carry the fact stated
// in turn 1 (content-level dependency, not just N completed turns), and the
// accumulated conversation transcript grows monotonically across turns.
func assertMultiturnContextCarried(turnOutputs []string) error {
	if len(turnOutputs) != multiturnTurnCount {
		return fmt.Errorf("drove %d turns, want %d", len(turnOutputs), multiturnTurnCount)
	}
	for i, output := range turnOutputs {
		if !strings.Contains(output, "[session closed: fixture_complete]") {
			return fmt.Errorf("turn %d did not complete cleanly, got:\n%s", i+1, output)
		}
	}
	wantLater := []string{multiturnCodeword, "RYHPEZ"}
	for i, want := range wantLater {
		if !strings.Contains(turnOutputs[2+i], want) {
			return fmt.Errorf("turn %d output missing turn-1 dependent content %q; context was not carried:\n%s", 3+i, want, turnOutputs[2+i])
		}
	}
	cumulative := &strings.Builder{}
	for i, output := range turnOutputs {
		before := cumulative.Len()
		cumulative.WriteString(output + "\n")
		if cumulative.Len() <= before {
			return fmt.Errorf("conversation transcript did not grow at turn %d", i+1)
		}
		for j := 0; j < i; j++ {
			if !strings.Contains(cumulative.String(), multiturnTurnReply(turnOutputs[j])) {
				return fmt.Errorf("accumulated transcript lost turn %d content after turn %d", j+1, i+1)
			}
		}
	}
	return nil
}

// multiturnTurnReply extracts the assistant reply text printed during one
// turn: everything before the terminal close marker.
func multiturnTurnReply(output string) string {
	if idx := strings.Index(output, "[session closed:"); idx >= 0 {
		return strings.TrimSpace(output[:idx])
	}
	return strings.TrimSpace(output)
}

// TestMultiturnZephyrFixtureIsWellFormed proves the committed positive
// fixture loads cleanly through the existing replayer validation surface and
// really encodes >=3 sequential audio-in turns whose later recorded responses
// depend on the turn-1 fact.
func TestMultiturnZephyrFixtureIsWellFormed(t *testing.T) {
	fixturePath := locateCLIFixture(t, multiturnPositiveFixture)

	if violations := gwtesting.ValidateSessionCaptureFile(fixturePath); len(violations) > 0 {
		t.Fatalf("committed multiturn fixture failed validation: %v", violations)
	}

	// The replayer transport itself must accept the fixture once the
	// sanitized audio fields are restored from the committed WAV corpus.
	injected := captureCopy(t, fixturePath)
	injectMultiturnAudioFrames(t, injected)
	injectedPath := filepath.Join(t.TempDir(), "multiturn_injected.session.json")
	data, err := json.MarshalIndent(injected, "", "  ")
	if err != nil {
		t.Fatalf("marshal injected fixture: %v", err)
	}
	if err := os.WriteFile(injectedPath, data, 0o600); err != nil {
		t.Fatalf("write injected fixture: %v", err)
	}
	if _, err := gwtesting.NewReplayWebSocketDialer(injectedPath); err != nil {
		t.Fatalf("replayer rejected multiturn fixture: %v", err)
	}

	capture := injected
	appends, commits, itemAdded := 0, 0, 0
	lastItemSequence := 0
	commitsSeen := 0
	turnThreePlusText := ""
	for _, record := range capture.Records {
		switch record.Type {
		case "input_audio_buffer.append":
			appends++
		case "input_audio_buffer.commit":
			commits++
			commitsSeen++
		case "response.output_item.added":
			itemAdded++
			if record.Sequence <= lastItemSequence {
				t.Fatalf("conversation item event at sequence %d did not accumulate monotonically (previous %d)", record.Sequence, lastItemSequence)
			}
			lastItemSequence = record.Sequence
		case "response.output_text.delta":
			if commitsSeen >= 3 {
				turnThreePlusText += jsonStringField(record.Payload, "delta")
			}
		}
	}
	if commits < 3 {
		t.Fatalf("fixture records %d audio-in turns, want >= 3", commits)
	}
	if appends == 0 || itemAdded != commits {
		t.Fatalf("fixture appends/commits/item-events = %d/%d/%d, want audio frames plus one commit and one item event per turn", appends, commits, itemAdded)
	}
	if !strings.Contains(turnThreePlusText, multiturnCodeword) {
		t.Fatalf("recorded turn-3+ responses do not carry the turn-1 codeword: %q", turnThreePlusText)
	}
}

// TestSessionCommandMultiturnReplayCarriesFactAcrossTurns is the depth-4
// milestone proof: driving every turn of the committed conversation through
// the shipped CLI session command (--audio-in over record/replay, one
// invocation per turn) must produce later-turn output that carries the fact
// introduced in turn 1, with conversation items accumulating monotonically.
func TestSessionCommandMultiturnReplayCarriesFactAcrossTurns(t *testing.T) {
	fixturePath := locateCLIFixture(t, multiturnPositiveFixture)

	turnOutputs := make([]string, 0, multiturnTurnCount)
	for turn := 1; turn <= multiturnTurnCount; turn++ {
		slicePath := multiturnSliceFixture(t, fixturePath, turn)
		output, err := runMultiturnTurn(t, slicePath, locateCLIFixture(t, multiturnTurnWAVs[turn-1]))
		if err != nil {
			t.Fatalf("session command turn %d over replay: %v\nstdout:\n%s", turn, err, output)
		}
		turnOutputs = append(turnOutputs, output)
	}

	if err := assertMultiturnContextCarried(turnOutputs); err != nil {
		t.Fatal(err)
	}
}

// TestSessionCommandMultiturnNegativeControlFailsContextAssertion is the
// non-vacuousness proof: against the negative-control fixture, identical
// except that its later-turn responses omit the carried fact, the exact same
// assertion must fail.
func TestSessionCommandMultiturnNegativeControlFailsContextAssertion(t *testing.T) {
	negativePath := locateCLIFixture(t, multiturnNegativeFixture)
	positivePath := locateCLIFixture(t, multiturnPositiveFixture)
	assertSameShapeExceptLaterResponses(t, positivePath, negativePath)

	turnOutputs := make([]string, 0, multiturnTurnCount)
	for turn := 1; turn <= multiturnTurnCount; turn++ {
		slicePath := multiturnSliceFixture(t, negativePath, turn)
		output, err := runMultiturnTurn(t, slicePath, locateCLIFixture(t, multiturnTurnWAVs[turn-1]))
		if err != nil {
			t.Fatalf("negative-control session command turn %d over replay: %v\nstdout:\n%s", turn, err, output)
		}
		turnOutputs = append(turnOutputs, output)
	}

	violation := assertMultiturnContextCarried(turnOutputs)
	if violation == nil {
		t.Fatal("context-carry assertion passed on the negative control; the check is vacuous")
	}
	if !strings.Contains(violation.Error(), multiturnCodeword) {
		t.Fatalf("negative-control violation should name the missing carried context, got: %v", violation)
	}
	t.Logf("negative control rejected as expected: %v", violation)
}

// assertSameShapeExceptLaterResponses pins the negative-control contract: the
// fixtures are structurally identical, and only the turn-3+ recorded response
// text omits the turn-1 fact.
func assertSameShapeExceptLaterResponses(t *testing.T, positivePath, negativePath string) {
	t.Helper()

	positive, err := gwtesting.LoadSessionCapture(positivePath)
	if err != nil {
		t.Fatalf("load positive fixture: %v", err)
	}
	negative, err := gwtesting.LoadSessionCapture(negativePath)
	if err != nil {
		t.Fatalf("load negative fixture: %v", err)
	}
	if len(positive.Records) != len(negative.Records) {
		t.Fatalf("negative control changed the record structure: %d vs %d records", len(negative.Records), len(positive.Records))
	}
	deltasSeen := 0
	for i, posRecord := range positive.Records {
		negRecord := negative.Records[i]
		if posRecord.Type != negRecord.Type || posRecord.Direction != negRecord.Direction {
			t.Fatalf("record %d structure diverged: %s/%s vs %s/%s", i, posRecord.Type, posRecord.Direction, negRecord.Type, negRecord.Direction)
		}
		if posRecord.Type != "response.output_text.delta" {
			continue
		}
		deltasSeen++
		posText := jsonStringField(posRecord.Payload, "delta")
		negText := jsonStringField(negRecord.Payload, "delta")
		// Turns 1 and 2 carry two deltas each; later turns are the controls.
		if deltasSeen <= 4 {
			if posText != negText {
				t.Fatalf("negative control diverged before turn 3 at delta %d: %q vs %q", deltasSeen, posText, negText)
			}
			continue
		}
		if posText == negText {
			t.Fatalf("negative-control turn-3+ response should omit the carried fact, got %q in both fixtures", posText)
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

func jsonStringField(raw json.RawMessage, field string) string {
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return payload[field]
}
