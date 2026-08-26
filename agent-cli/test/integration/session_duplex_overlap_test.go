package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	v8OverlapTick        uint64 = 7
	v8OverlapTickLimit   uint64 = 8
	v8TickDuration              = 10 * time.Millisecond
	v8CommandMaxDuration        = time.Second
	v8RunTimeout                = 2 * time.Second
	v8VADThreshold              = 300.0
	v8TurnBound                 = 2
	v8PCMFrameBytes             = audio.FrameSize * 2
)

const (
	v8HarnessAInstruction = "harness-A: answer with the amber profile"
	v8HarnessBInstruction = "harness-B: answer with the cobalt profile"
)

// v8Crossing is the transport-level record. Emitted is what the CLI wrote to
// its raw --audio-out stream; delivered is what the peer consumed from its
// raw --audio-in stream. Keeping both makes the silence mutation observable
// without ever using transcript text as a bridge.
type v8Crossing struct {
	Sequence  int
	Direction string
	Tick      uint64
	Timestamp time.Time
	Emitted   []byte
	Delivered []byte
}

type v8CrossingCoordinator struct {
	overlapTick uint64

	mu            sync.Mutex
	nextDirection string
	crossings     []v8Crossing
	aToBReady     chan struct{}
	deliveryReady chan struct{}
	abort         chan struct{}
	abortOnce     sync.Once
	deliveryOnce  sync.Once
}

func newV8CrossingCoordinator() *v8CrossingCoordinator {
	return &v8CrossingCoordinator{
		overlapTick:   v8OverlapTick,
		nextDirection: "A-to-B",
		aToBReady:     make(chan struct{}),
		deliveryReady: make(chan struct{}),
		abort:         make(chan struct{}),
	}
}

func (c *v8CrossingCoordinator) abortRun() {
	c.abortOnce.Do(func() { close(c.abort) })
}

func (c *v8CrossingCoordinator) releaseDelivery() {
	c.deliveryOnce.Do(func() { close(c.deliveryReady) })
}

func (c *v8CrossingCoordinator) record(direction string, tick uint64, timestamp time.Time, emitted, delivered []byte) (v8Crossing, error) {
	if direction == "B-to-A" {
		select {
		case <-c.aToBReady:
		case <-c.abort:
			return v8Crossing{}, context.Canceled
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.abort:
		return v8Crossing{}, context.Canceled
	default:
	}
	if direction != c.nextDirection {
		return v8Crossing{}, fmt.Errorf("crossing order %s arrived while expecting %s", direction, c.nextDirection)
	}
	if tick != c.overlapTick {
		return v8Crossing{}, fmt.Errorf("crossing %s observed at logical tick %d, want overlap tick %d", direction, tick, c.overlapTick)
	}
	if timestamp.IsZero() {
		return v8Crossing{}, fmt.Errorf("crossing %s has no runtime timestamp", direction)
	}

	crossing := v8Crossing{
		Sequence:  len(c.crossings) + 1,
		Direction: direction,
		Tick:      tick,
		Timestamp: timestamp,
		Emitted:   append([]byte(nil), emitted...),
		Delivered: append([]byte(nil), delivered...),
	}
	c.crossings = append(c.crossings, crossing)
	if direction == "A-to-B" {
		c.nextDirection = "B-to-A"
		close(c.aToBReady)
	}
	return crossing, nil
}

func (c *v8CrossingCoordinator) snapshot() []v8Crossing {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]v8Crossing, len(c.crossings))
	for i, crossing := range c.crossings {
		out[i] = crossing
		out[i].Emitted = append([]byte(nil), crossing.Emitted...)
		out[i].Delivered = append([]byte(nil), crossing.Delivered...)
	}
	return out
}

type v8BridgePacket struct {
	eof      bool
	crossing v8Crossing
}

type v8PCMBridge struct {
	coordinator *v8CrossingCoordinator
	direction   string
	sender      *v8RecordingView
	receiver    *v8RecordingView
	silence     []byte
	mutateFirst bool
	runtimeOut  chan services.SessionRuntimeObservation

	packets chan v8BridgePacket
	mu      sync.Mutex
	written bool
	eofRead bool
}

func newV8PCMBridge(coordinator *v8CrossingCoordinator, direction string, sender, receiver *v8RecordingView, silence []byte, mutateFirst bool) *v8PCMBridge {
	return &v8PCMBridge{
		coordinator: coordinator,
		direction:   direction,
		sender:      sender,
		receiver:    receiver,
		silence:     append([]byte(nil), silence...),
		mutateFirst: mutateFirst,
		packets:     make(chan v8BridgePacket, 2),
		runtimeOut:  make(chan services.SessionRuntimeObservation, 1),
	}
}

func (b *v8PCMBridge) acceptRuntimeOutput(observation services.SessionRuntimeObservation) {
	select {
	case b.runtimeOut <- observation:
	case <-b.coordinator.abort:
	}
}

func (b *v8PCMBridge) nextRuntimeOutput() (services.SessionRuntimeObservation, error) {
	select {
	case observation := <-b.runtimeOut:
		return observation, nil
	case <-b.coordinator.abort:
		return services.SessionRuntimeObservation{}, context.Canceled
	}
}

func (b *v8PCMBridge) write(data []byte) (int, error) {
	if len(data) != v8PCMFrameBytes {
		return 0, fmt.Errorf("%s emitted %d PCM bytes, want one %d-byte frame", b.direction, len(data), v8PCMFrameBytes)
	}

	b.mu.Lock()
	if b.written {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	b.written = true
	b.mu.Unlock()

	emitted := append([]byte(nil), data...)
	delivered := append([]byte(nil), emitted...)
	if b.mutateFirst {
		if len(b.silence) != len(delivered) {
			return 0, fmt.Errorf("%s silence mutation is %d bytes, want %d", b.direction, len(b.silence), len(delivered))
		}
		delivered = append([]byte(nil), b.silence...)
	}
	outputObservation, err := b.nextRuntimeOutput()
	if err != nil {
		return 0, err
	}
	if outputObservation.Kind != services.SessionRuntimeObservationAudioOutput {
		return 0, fmt.Errorf("%s runtime observation kind = %q, want %q", b.direction, outputObservation.Kind, services.SessionRuntimeObservationAudioOutput)
	}
	if !bytes.Equal(outputObservation.Payload, emitted) {
		return 0, fmt.Errorf("%s runtime audio output payload differs from the CLI writer payload: runtime hash=%s writer hash=%s", b.direction, v8PCMHash(outputObservation.Payload), v8PCMHash(emitted))
	}
	crossing, err := b.coordinator.record(b.direction, outputObservation.Tick, outputObservation.Timestamp, emitted, delivered)
	if err != nil {
		return 0, err
	}
	b.sender.record(crossing, emitted)
	if b.direction == "A-to-B" {
		select {
		case <-b.coordinator.deliveryReady:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
	} else {
		// Do not release A-to-B to B until B has emitted its own response. This
		// preserves the replayed server-output-before-client-input order on
		// both CLIs while retaining one equal-tick overlap window.
		b.coordinator.releaseDelivery()
	}

	select {
	case b.packets <- v8BridgePacket{crossing: crossing}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	// The one-frame input turn is intentionally closed after the first
	// crossing. That EOF is what makes the peer CLI send its MESSAGE.END and
	// exercises the shipped audio-input commit path.
	select {
	case b.packets <- v8BridgePacket{eof: true}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	return len(data), nil
}

func (b *v8PCMBridge) read(ctx context.Context, destination []byte) (int, error) {
	if len(destination) < v8PCMFrameBytes {
		return 0, fmt.Errorf("%s receiver requested %d PCM bytes, want at least %d", b.direction, len(destination), v8PCMFrameBytes)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.coordinator.abort:
		return 0, context.Canceled
	case packet := <-b.packets:
		if packet.eof {
			b.mu.Lock()
			b.eofRead = true
			b.mu.Unlock()
			return 0, io.EOF
		}
		copy(destination, packet.crossing.Delivered)
		b.receiver.record(packet.crossing, packet.crossing.Delivered)
		return len(packet.crossing.Delivered), nil
	}
}

func (b *v8PCMBridge) wroteFrame() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written
}

func (b *v8PCMBridge) observedEOF() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eofRead
}

type v8RuntimeObserver struct {
	outputBridge *v8PCMBridge

	mu           sync.Mutex
	observations []services.SessionRuntimeObservation
}

func (o *v8RuntimeObserver) ObserveSessionRuntime(observation services.SessionRuntimeObservation) {
	if o == nil {
		return
	}
	observation.Payload = append([]byte(nil), observation.Payload...)
	o.mu.Lock()
	o.observations = append(o.observations, observation)
	o.mu.Unlock()
	if observation.Kind == services.SessionRuntimeObservationAudioOutput && o.outputBridge != nil {
		o.outputBridge.acceptRuntimeOutput(observation)
	}
}

func (o *v8RuntimeObserver) snapshot() []services.SessionRuntimeObservation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	observations := make([]services.SessionRuntimeObservation, len(o.observations))
	for i, observation := range o.observations {
		observations[i] = observation
		observations[i].Payload = append([]byte(nil), observation.Payload...)
	}
	return observations
}

type v8PCMWriter struct{ bridge *v8PCMBridge }

func (w v8PCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8PCMReader struct{ bridge *v8PCMBridge }

func (r v8PCMReader) Read(data []byte) (int, error) {
	return r.bridge.read(context.Background(), data)
}

func (r v8PCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	return r.bridge.read(ctx, data)
}

type v8ViewRecord struct {
	Order     int       `json:"order"`
	Direction string    `json:"direction"`
	Tick      uint64    `json:"tick"`
	Timestamp time.Time `json:"timestamp"`
	Payload   []byte    `json:"payload"`
	SHA256    string    `json:"sha256"`
	RMS       float64   `json:"rms"`
}

type v8RecordingView struct {
	Harness string
	Role    string

	mu      sync.Mutex
	records []v8ViewRecord
}

func (v *v8RecordingView) record(crossing v8Crossing, payload []byte) {
	hash, rms := v8PCMStats(payload)
	v.mu.Lock()
	v.records = append(v.records, v8ViewRecord{
		Order:     crossing.Sequence,
		Direction: crossing.Direction,
		Tick:      crossing.Tick,
		Timestamp: crossing.Timestamp,
		Payload:   append([]byte(nil), payload...),
		SHA256:    hash,
		RMS:       rms,
	})
	v.mu.Unlock()
}

func (v *v8RecordingView) snapshot() []v8ViewRecord {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]v8ViewRecord, len(v.records))
	for i, record := range v.records {
		out[i] = record
		out[i].Payload = append([]byte(nil), record.Payload...)
	}
	return out
}

type v8TerminalFact struct {
	Clean          bool      `json:"clean"`
	Turns          int       `json:"turns"`
	FinalTick      uint64    `json:"final_tick"`
	FinalTimestamp time.Time `json:"final_timestamp"`
	InputEOF       bool      `json:"input_eof"`
	OutputFrame    bool      `json:"output_frame"`
	Error          string    `json:"error,omitempty"`
}

type v8ViewArtifact struct {
	Harness    string         `json:"harness"`
	Role       string         `json:"role"`
	SampleRate int            `json:"sample_rate_hz"`
	Records    []v8ViewRecord `json:"records"`
	Terminal   v8TerminalFact `json:"terminal"`
}

type v8HarnessResult struct {
	Name        string
	Instruction string
	ReplayPath  string
	Err         error
	Elapsed     time.Duration
	Runtime     []services.SessionRuntimeObservation
}

type v8DuplexRun struct {
	base       time.Time
	crossings  []v8Crossing
	harnesses  map[string]v8HarnessResult
	views      map[string]*v8RecordingView
	artifacts  map[string]string
	terminal   map[string]v8TerminalFact
	finalTick  uint64
	turnsBound int
}

func v8PCMStats(payload []byte) (string, float64) {
	digest := sha256.Sum256(payload)
	if len(payload) == 0 || len(payload)%2 != 0 {
		return hex.EncodeToString(digest[:]), 0
	}
	var energy float64
	for offset := 0; offset < len(payload); offset += 2 {
		sample := int16(binary.LittleEndian.Uint16(payload[offset:]))
		energy += float64(sample) * float64(sample)
	}
	return hex.EncodeToString(digest[:]), math.Sqrt(energy / float64(len(payload)/2))
}

func v8PCMHash(payload []byte) string {
	hash, _ := v8PCMStats(payload)
	return hash
}

func v8PCM16Bytes(samples []int16) []byte {
	payload := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(payload[i*2:], uint16(sample))
	}
	return payload
}

func v8AudioFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve v8 audio fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "go-agent-loop", "testdata", "audio", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("v8 audio fixture %q not found at %q: %v", name, path, err)
	}
	return path
}

func v8LoudFrames(t *testing.T, path string) ([]byte, []byte) {
	t.Helper()
	wav, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v8 overlap fixture: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("parse v8 overlap fixture: %v", err)
	}
	if rate != audio.SampleRate {
		t.Fatalf("v8 overlap fixture rate = %d, want %d", rate, audio.SampleRate)
	}
	find := func(excludeStart int) []byte {
		bestStart := -1
		bestEnergy := -1.0
		for start := 0; start+audio.FrameSize <= len(samples); start += audio.FrameSize {
			if excludeStart >= 0 && absInt(start-excludeStart) < audio.FrameSize*4 {
				continue
			}
			var energy float64
			for _, sample := range samples[start : start+audio.FrameSize] {
				energy += float64(sample) * float64(sample)
			}
			if energy > bestEnergy {
				bestStart, bestEnergy = start, energy
			}
		}
		if bestStart < 0 {
			t.Fatalf("v8 overlap fixture has no %d-sample frame outside exclusion", audio.FrameSize)
		}
		return v8PCM16Bytes(samples[bestStart : bestStart+audio.FrameSize])
	}
	first := find(-1)
	second := find(findFrameStart(samples, first))
	for _, payload := range [][]byte{first, second} {
		_, rms := v8PCMStats(payload)
		if rms <= v8VADThreshold {
			t.Fatalf("v8 overlap fixture frame RMS = %.1f, want > %.1f", rms, v8VADThreshold)
		}
	}
	return first, second
}

func findFrameStart(samples []int16, frame []byte) int {
	frameSamples := len(frame) / 2
	for start := 0; start+frameSamples <= len(samples); start += audio.FrameSize {
		if bytes.Equal(v8PCM16Bytes(samples[start:start+frameSamples]), frame) {
			return start
		}
	}
	return -1
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func v8CaptureRecord(sequence int, direction gwtesting.SessionEventDirection, msg messages.StreamMessage) gwtesting.CapturedSessionEvent {
	payload, err := gwtesting.MarshalStreamMessage(msg)
	if err != nil {
		panic(fmt.Sprintf("marshal v8 capture event %s: %v", msg.Type, err))
	}
	return gwtesting.CapturedSessionEvent{
		Sequence:    sequence,
		Direction:   direction,
		TimestampMs: int64(sequence - 1),
		Type:        string(msg.Type),
		PayloadType: gwtesting.SessionPayloadTypeStreamMessage,
		Payload:     payload,
	}
}

func writeV8ReplayCapture(t *testing.T, path, sessionID, instruction string, output, expectedInput []byte) {
	t.Helper()
	records := []gwtesting.CapturedSessionEvent{
		v8CaptureRecord(1, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue(sessionID, "audio_inference"),
		}),
		v8CaptureRecord(2, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue(instruction),
		}),
		v8CaptureRecord(3, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(output),
		}),
		v8CaptureRecord(4, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Value: messages.NewAudioDeltaValue(expectedInput),
		}),
		// The audio source sends a type-only MESSAGE.END after it reads EOF.
		v8CaptureRecord(5, gwtesting.DirectionClientToServer, messages.StreamMessage{
			Type: messages.StreamTypeMessageEnd,
		}),
		v8CaptureRecord(6, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeAudioEnd,
			Value: messages.NewAudioEndValue(),
		}),
		v8CaptureRecord(7, gwtesting.DirectionServerToClient, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		}),
	}
	capture := gwtesting.SessionCapture{
		Version:  gwtesting.SessionCaptureVersion,
		Provider: gwtesting.SessionProviderMetadata{Name: "synthetic-t1", Model: "session-replay"},
		Session: gwtesting.SessionMetadata{
			ID:                sessionID,
			StartedAtUTC:      "2026-08-26T00:00:00Z",
			FixtureProvenance: gwtesting.SessionFixtureProvenanceSynthetic,
		},
		Records: records,
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 replay capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write v8 replay capture: %v", err)
	}
}

func newV8CLI(t *testing.T, logicalClock *clock.Deterministic, observer *v8RuntimeObserver) *cli.AgentCLI {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewPortSwap(wire.PortClock, logicalClock),
		wire.NewPortSwap(wire.PortSessionRuntimeObserver, observer),
	)
	if err != nil {
		t.Fatalf("initialize v8 CLI with shared clock and runtime observer: %v", err)
	}
	return agentCLI
}

func runV8Duplex(t *testing.T, aToB, bToA []byte, mutateFirst bool) v8DuplexRun {
	t.Helper()
	base := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	logicalClock := clock.NewDeterministic(base, v8TickDuration)
	logicalClock.AdvanceTo(v8OverlapTick)
	coordinator := newV8CrossingCoordinator()

	silencePath := v8AudioFixturePath(t, "silence_16k.wav")
	silenceWAV, err := os.ReadFile(silencePath)
	if err != nil {
		t.Fatalf("read v8 silence fixture: %v", err)
	}
	_, silenceSamples, err := wavio.Read(bytes.NewReader(silenceWAV))
	if err != nil {
		t.Fatalf("parse v8 silence fixture: %v", err)
	}
	if len(silenceSamples) < audio.FrameSize {
		t.Fatalf("v8 silence fixture has %d samples, want at least %d", len(silenceSamples), audio.FrameSize)
	}
	silenceFrame := v8PCM16Bytes(silenceSamples[:audio.FrameSize])

	views := map[string]*v8RecordingView{
		"A/client": {Harness: "A", Role: "client"},
		"A/agent":  {Harness: "A", Role: "agent"},
		"B/client": {Harness: "B", Role: "client"},
		"B/agent":  {Harness: "B", Role: "agent"},
	}
	aToBBridge := newV8PCMBridge(coordinator, "A-to-B", views["A/client"], views["B/agent"], silenceFrame, mutateFirst)
	bToABridge := newV8PCMBridge(coordinator, "B-to-A", views["B/client"], views["A/agent"], silenceFrame, false)
	aObserver := &v8RuntimeObserver{outputBridge: aToBBridge}
	bObserver := &v8RuntimeObserver{outputBridge: bToABridge}

	runDir := t.TempDir()
	aReplay := filepath.Join(runDir, "harness-a.session.json")
	bReplay := filepath.Join(runDir, "harness-b.session.json")
	writeV8ReplayCapture(t, aReplay, "s2s-v8-harness-a", v8HarnessAInstruction, aToB, bToA)
	replayedAToB := aToB
	if mutateFirst {
		replayedAToB = silenceFrame
	}
	writeV8ReplayCapture(t, bReplay, "s2s-v8-harness-b", v8HarnessBInstruction, bToA, replayedAToB)

	// Construct both generated shipped CLIs before starting either command and
	// pass the same *clock.Deterministic identity and a runtime observer through
	// both composition graphs. The goroutines execute only `agent session`; no
	// loop, provider, or replay helper is the evidence path. The observer is
	// fed by the session runtime itself, including its clock-stamped output.
	aCLI := newV8CLI(t, logicalClock, aObserver)
	bCLI := newV8CLI(t, logicalClock, bObserver)

	ctx, cancel := context.WithTimeout(context.Background(), v8RunTimeout)
	defer cancel()
	results := make(chan v8HarnessResult, 2)
	startGate := make(chan struct{})
	var wg sync.WaitGroup
	start := func(name, instruction, replayPath string, input io.Reader, output io.Writer, commandCLI *cli.AgentCLI, observer *v8RuntimeObserver) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			started := time.Now()
			root := commandCLI.Generate()
			root.SetIn(input)
			root.SetOut(output)
			root.SetErr(io.Discard)
			root.SetArgs([]string{
				"session",
				"--replay", replayPath,
				"--audio-in", "-",
				"--audio-out", "-",
				"--max-duration", v8CommandMaxDuration.String(),
				instruction,
			})
			results <- v8HarnessResult{
				Name:        name,
				Instruction: instruction,
				ReplayPath:  replayPath,
				Err:         root.ExecuteContext(ctx),
				Elapsed:     time.Since(started),
				Runtime:     observer.snapshot(),
			}
		}()
	}
	start("A", v8HarnessAInstruction, aReplay, v8PCMReader{bridge: bToABridge}, v8PCMWriter{bridge: aToBBridge}, aCLI, aObserver)
	start("B", v8HarnessBInstruction, bReplay, v8PCMReader{bridge: aToBBridge}, v8PCMWriter{bridge: bToABridge}, bCLI, bObserver)
	close(startGate)

	harnesses := make(map[string]v8HarnessResult, 2)
	contextDone := ctx.Done()
	cleanupTimer := time.NewTimer(v8RunTimeout + time.Second)
	defer cleanupTimer.Stop()
	for len(harnesses) < 2 {
		select {
		case result := <-results:
			harnesses[result.Name] = result
			if result.Err != nil {
				coordinator.abortRun()
				cancel()
			}
		case <-contextDone:
			coordinator.abortRun()
			cancel()
			contextDone = nil
		case <-cleanupTimer.C:
			coordinator.abortRun()
			cancel()
			t.Fatal("v8 CLI harnesses did not return after the bounded cleanup window")
		}
	}
	wg.Wait()

	finalTick := uint64(0)
	run := v8DuplexRun{
		base:       base,
		crossings:  coordinator.snapshot(),
		harnesses:  harnesses,
		views:      views,
		terminal:   map[string]v8TerminalFact{},
		finalTick:  finalTick,
		turnsBound: v8TurnBound,
	}
	for name, result := range harnesses {
		terminalObservation, err := v8RuntimeObservation(result.Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			t.Fatalf("harness %s terminal runtime observation: %v", name, err)
		}
		terminal := v8TerminalFact{
			Clean:          terminalObservation.Clean,
			Turns:          terminalObservation.TurnsCompleted,
			FinalTick:      terminalObservation.Tick,
			FinalTimestamp: terminalObservation.Timestamp,
			Error:          terminalObservation.Error,
		}
		if terminal.FinalTick > finalTick {
			finalTick = terminal.FinalTick
		}
		if name == "A" {
			terminal.InputEOF = bToABridge.observedEOF()
			terminal.OutputFrame = aToBBridge.wroteFrame()
		} else {
			terminal.InputEOF = aToBBridge.observedEOF()
			terminal.OutputFrame = bToABridge.wroteFrame()
		}
		run.terminal[name] = terminal
	}
	run.finalTick = finalTick
	for name, view := range views {
		terminal := run.terminal[view.Harness]
		viewPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".json")
		wavPath := filepath.Join(runDir, strings.ReplaceAll(name, "/", "-")+".wav")
		writeV8ViewArtifacts(t, view, terminal, viewPath, wavPath)
		run.artifacts = appendArtifactPaths(run.artifacts, name, viewPath, wavPath)
	}
	return run
}

func v8RuntimeObservation(observations []services.SessionRuntimeObservation, kind services.SessionRuntimeObservationKind) (services.SessionRuntimeObservation, error) {
	var found services.SessionRuntimeObservation
	count := 0
	for _, observation := range observations {
		if observation.Kind != kind {
			continue
		}
		found = observation
		count++
	}
	if count != 1 {
		return services.SessionRuntimeObservation{}, fmt.Errorf("runtime observation %q count = %d, want exactly one", kind, count)
	}
	return found, nil
}

func appendArtifactPaths(artifacts map[string]string, viewName, jsonPath, wavPath string) map[string]string {
	if artifacts == nil {
		artifacts = make(map[string]string)
	}
	artifacts[viewName+".json"] = jsonPath
	artifacts[viewName+".wav"] = wavPath
	return artifacts
}

func writeV8ViewArtifacts(t *testing.T, view *v8RecordingView, terminal v8TerminalFact, jsonPath, wavPath string) {
	t.Helper()
	artifact := v8ViewArtifact{
		Harness:    view.Harness,
		Role:       view.Role,
		SampleRate: audio.SampleRate,
		Records:    view.snapshot(),
		Terminal:   terminal,
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatalf("marshal v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("write v8 %s/%s recording artifact: %v", view.Harness, view.Role, err)
	}
	payload := []byte{}
	for _, record := range artifact.Records {
		payload = append(payload, record.Payload...)
	}
	if len(payload) == 0 {
		t.Fatalf("v8 %s/%s recording has no PCM payload", view.Harness, view.Role)
	}
	samples := make([]int16, len(payload)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
	}
	var wav bytes.Buffer
	if err := wavio.Write(&wav, audio.SampleRate, samples); err != nil {
		t.Fatalf("encode v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
	if err := os.WriteFile(wavPath, wav.Bytes(), 0o600); err != nil {
		t.Fatalf("write v8 %s/%s WAV artifact: %v", view.Harness, view.Role, err)
	}
}

func verifyV8Run(run v8DuplexRun, expected map[string][]byte) error {
	if len(run.harnesses) != 2 {
		return fmt.Errorf("expected two CLI harness results, observed %d", len(run.harnesses))
	}
	aHarness, aOK := run.harnesses["A"]
	bHarness, bOK := run.harnesses["B"]
	if !aOK || !bOK {
		return fmt.Errorf("expected harness results for A and B")
	}
	if aHarness.Instruction == bHarness.Instruction {
		return fmt.Errorf("harness instructions are not distinct: %q", aHarness.Instruction)
	}
	if aHarness.Instruction != v8HarnessAInstruction || bHarness.Instruction != v8HarnessBInstruction {
		return fmt.Errorf("harness instructions do not match the two scripted profiles")
	}
	if len(run.crossings) != 2 {
		return fmt.Errorf("expected two retained PCM crossings, observed %d", len(run.crossings))
	}
	wantDirections := []string{"A-to-B", "B-to-A"}
	for i, crossing := range run.crossings {
		if crossing.Sequence != i+1 || crossing.Direction != wantDirections[i] {
			return fmt.Errorf("crossing order mismatch at index %d: got sequence=%d direction=%s", i, crossing.Sequence, crossing.Direction)
		}
		if crossing.Tick != v8OverlapTick {
			return fmt.Errorf("%s crossing recorded at logical tick %d, want %d", crossing.Direction, crossing.Tick, v8OverlapTick)
		}
		wantTime := run.base.Add(time.Duration(crossing.Tick) * v8TickDuration)
		if !crossing.Timestamp.Equal(wantTime) {
			return fmt.Errorf("%s tick %d timestamp=%s, want deterministic timestamp %s", crossing.Direction, crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano), wantTime.Format(time.RFC3339Nano))
		}
		want := expected[crossing.Direction]
		if !bytes.Equal(crossing.Emitted, want) {
			return v8PCMFailure(crossing, want, crossing.Emitted, "CLI output")
		}
		_, deliveredRMS := v8PCMStats(crossing.Delivered)
		if !bytes.Equal(crossing.Delivered, want) || deliveredRMS <= v8VADThreshold {
			return v8PCMFailure(crossing, want, crossing.Delivered, "peer input")
		}

		sender, receiver := "A", "B"
		if crossing.Direction == "B-to-A" {
			sender, receiver = "B", "A"
		}
		outputObservation, err := v8RuntimeObservation(run.harnesses[sender].Runtime, services.SessionRuntimeObservationAudioOutput)
		if err != nil {
			return fmt.Errorf("harness %s output runtime observation: %w", sender, err)
		}
		if outputObservation.Tick != crossing.Tick || !outputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime output timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, outputObservation.Tick, outputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(outputObservation.Payload, crossing.Emitted) {
			return v8PCMFailure(crossing, crossing.Emitted, outputObservation.Payload, "runtime output")
		}
		inputObservation, err := v8RuntimeObservation(run.harnesses[receiver].Runtime, services.SessionRuntimeObservationAudioInput)
		if err != nil {
			return fmt.Errorf("harness %s input runtime observation: %w", receiver, err)
		}
		if inputObservation.Tick != crossing.Tick || !inputObservation.Timestamp.Equal(crossing.Timestamp) {
			return fmt.Errorf("%s runtime input timing differs from crossing: runtime tick=%d timestamp=%s, crossing tick=%d timestamp=%s", crossing.Direction, inputObservation.Tick, inputObservation.Timestamp.Format(time.RFC3339Nano), crossing.Tick, crossing.Timestamp.Format(time.RFC3339Nano))
		}
		if !bytes.Equal(inputObservation.Payload, crossing.Delivered) {
			return v8PCMFailure(crossing, crossing.Delivered, inputObservation.Payload, "runtime input")
		}
	}

	if run.crossings[0].Tick != run.crossings[1].Tick {
		return fmt.Errorf("directional speech windows do not overlap: A-to-B tick %d, B-to-A tick %d", run.crossings[0].Tick, run.crossings[1].Tick)
	}
	parityPairs := [][2]string{{"A/client", "B/agent"}, {"B/client", "A/agent"}}
	for _, pair := range parityPairs {
		left := run.views[pair[0]].snapshot()
		right := run.views[pair[1]].snapshot()
		if len(left) != 1 || len(right) != 1 {
			return fmt.Errorf("recording parity %s vs %s: got %d and %d records, want one each", pair[0], pair[1], len(left), len(right))
		}
		if err := compareV8ViewRecords(pair[0], left[0], pair[1], right[0]); err != nil {
			return err
		}
	}

	for name, terminal := range run.terminal {
		if !terminal.Clean || !terminal.InputEOF || !terminal.OutputFrame {
			return fmt.Errorf("harness %s terminal facts are not clean: %+v", name, terminal)
		}
		if terminal.Turns > run.turnsBound || terminal.FinalTick > v8OverlapTickLimit {
			return fmt.Errorf("harness %s exceeded turn/tick bounds: %+v", name, terminal)
		}
		turnObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTurnCompleted)
		if err != nil {
			return fmt.Errorf("harness %s turn runtime observation: %w", name, err)
		}
		if turnObservation.TurnsCompleted != terminal.Turns {
			return fmt.Errorf("harness %s completed-turn observation = %d, terminal observation = %d", name, turnObservation.TurnsCompleted, terminal.Turns)
		}
		if terminal.Turns == 0 {
			return fmt.Errorf("harness %s terminal observation reported no completed turns", name)
		}
		terminalObservation, err := v8RuntimeObservation(run.harnesses[name].Runtime, services.SessionRuntimeObservationTerminal)
		if err != nil {
			return fmt.Errorf("harness %s terminal runtime observation: %w", name, err)
		}
		if terminalObservation.Tick != terminal.FinalTick || !terminalObservation.Timestamp.Equal(terminal.FinalTimestamp) {
			return fmt.Errorf("harness %s terminal fact differs from runtime observation", name)
		}
		wantTerminalTime := run.base.Add(time.Duration(terminal.FinalTick) * v8TickDuration)
		if !terminal.FinalTimestamp.Equal(wantTerminalTime) {
			return fmt.Errorf("harness %s terminal tick %d timestamp=%s, want deterministic timestamp %s", name, terminal.FinalTick, terminal.FinalTimestamp.Format(time.RFC3339Nano), wantTerminalTime.Format(time.RFC3339Nano))
		}
		if (run.harnesses[name].Err == nil) != terminalObservation.Clean {
			return fmt.Errorf("harness %s runtime clean=%t disagrees with CLI error=%v", name, terminalObservation.Clean, run.harnesses[name].Err)
		}
	}
	aTerminal, aTerminalOK := run.terminal["A"]
	bTerminal, bTerminalOK := run.terminal["B"]
	if !aTerminalOK || !bTerminalOK {
		return fmt.Errorf("terminal facts missing for A or B")
	}
	if aTerminal != bTerminal {
		return fmt.Errorf("terminal parity A vs B differs: A=%+v B=%+v", aTerminal, bTerminal)
	}
	for name, result := range run.harnesses {
		if result.Err != nil {
			return fmt.Errorf("harness %s CLI failed after %s: %w", name, result.Elapsed, result.Err)
		}
		if result.Elapsed > v8CommandMaxDuration+500*time.Millisecond {
			return fmt.Errorf("harness %s exceeded command bound: %s", name, result.Elapsed)
		}
	}
	return verifyV8Artifacts(run)
}

func verifyV8Artifacts(run v8DuplexRun) error {
	viewNames := []string{"A/client", "A/agent", "B/client", "B/agent"}
	if len(run.artifacts) != len(viewNames)*2 {
		return fmt.Errorf("expected JSON and WAV artifacts for four views, observed %d paths", len(run.artifacts))
	}
	for _, viewName := range viewNames {
		jsonPath, jsonOK := run.artifacts[viewName+".json"]
		wavPath, wavOK := run.artifacts[viewName+".wav"]
		if !jsonOK || !wavOK {
			return fmt.Errorf("artifacts missing for %s", viewName)
		}
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			return fmt.Errorf("read %s JSON artifact: %w", viewName, err)
		}
		var artifact v8ViewArtifact
		if err := json.Unmarshal(data, &artifact); err != nil {
			return fmt.Errorf("decode %s JSON artifact: %w", viewName, err)
		}
		view := run.views[viewName]
		if view == nil {
			return fmt.Errorf("recording view %s is missing", viewName)
		}
		if artifact.Harness != view.Harness || artifact.Role != view.Role || artifact.SampleRate != audio.SampleRate {
			return fmt.Errorf("%s artifact metadata is invalid: %+v", viewName, artifact)
		}
		liveRecords := view.snapshot()
		if len(artifact.Records) != len(liveRecords) || len(artifact.Records) != 1 {
			return fmt.Errorf("%s artifact has %d records, live view has %d; want one", viewName, len(artifact.Records), len(liveRecords))
		}
		if err := compareV8ViewRecords(viewName+" artifact", artifact.Records[0], viewName+" live", liveRecords[0]); err != nil {
			return err
		}
		if wantTerminal, ok := run.terminal[view.Harness]; !ok || artifact.Terminal != wantTerminal {
			return fmt.Errorf("%s artifact terminal facts do not match the harness terminal facts", viewName)
		}

		wavData, err := os.ReadFile(wavPath)
		if err != nil {
			return fmt.Errorf("read %s WAV artifact: %w", viewName, err)
		}
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			return fmt.Errorf("decode %s WAV artifact: %w", viewName, err)
		}
		if rate != audio.SampleRate || len(samples) != len(liveRecords[0].Payload)/2 {
			return fmt.Errorf("%s WAV artifact shape is rate=%d samples=%d, want rate=%d samples=%d", viewName, rate, len(samples), audio.SampleRate, len(liveRecords[0].Payload)/2)
		}
		if !bytes.Equal(v8PCM16Bytes(samples), liveRecords[0].Payload) {
			return fmt.Errorf("%s WAV artifact payload differs from the recorded PCM", viewName)
		}
	}
	return nil
}

func v8PCMFailure(crossing v8Crossing, expected, observed []byte, view string) error {
	wantHash, wantRMS := v8PCMStats(expected)
	gotHash, gotRMS := v8PCMStats(observed)
	return fmt.Errorf("%s logical tick %d %s PCM mismatch: expected hash=%s RMS=%.1f (> %.1f); observed hash=%s RMS=%.1f", crossing.Direction, crossing.Tick, view, wantHash, wantRMS, v8VADThreshold, gotHash, gotRMS)
}

func compareV8ViewRecords(leftName string, left v8ViewRecord, rightName string, right v8ViewRecord) error {
	if left.Direction != right.Direction {
		return fmt.Errorf("recording parity %s vs %s direction differs: %s != %s", leftName, rightName, left.Direction, right.Direction)
	}
	if left.Order != right.Order || left.Tick != right.Tick || !left.Timestamp.Equal(right.Timestamp) {
		return fmt.Errorf("recording parity %s vs %s timing/order differs: left order=%d tick=%d timestamp=%s; right order=%d tick=%d timestamp=%s", leftName, rightName, left.Order, left.Tick, left.Timestamp.Format(time.RFC3339Nano), right.Order, right.Tick, right.Timestamp.Format(time.RFC3339Nano))
	}
	if left.SHA256 != right.SHA256 || !bytes.Equal(left.Payload, right.Payload) || left.RMS != right.RMS {
		return fmt.Errorf("recording parity %s vs %s payload differs: hash %s != %s RMS %.1f != %.1f", leftName, rightName, left.SHA256, right.SHA256, left.RMS, right.RMS)
	}
	return nil
}

func assertV8GoroutinesSettled(t *testing.T, baseline int, operation string) {
	t.Helper()
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines after %s = %d, baseline = %d; CLI lifecycle did not settle", operation, runtime.NumGoroutine(), baseline)
}

func TestSessionCLI_DuplexPCMOverlap(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, false)
	if err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA}); err != nil {
		t.Fatalf("positive v8 duplex proof failed: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "positive duplex run")
	t.Logf("v8 positive evidence: shared clock base=%s tick_duration=%s overlap_tick=%d final_tick=%d crossings=%d", run.base.Format(time.RFC3339Nano), v8TickDuration, v8OverlapTick, run.finalTick, len(run.crossings))
}

func TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, true)
	err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA})
	if err == nil {
		t.Fatal("silence negative control passed the positive audio verification")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "A-to-B") || !strings.Contains(diagnostic, fmt.Sprintf("logical tick %d", v8OverlapTick)) || !strings.Contains(diagnostic, "RMS") || !strings.Contains(diagnostic, "hash=") {
		t.Fatalf("negative control diagnostic lacks direction/tick/hash/RMS details: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "silence negative control")
	t.Logf("v8 silence negative control rejected as expected: %v", err)
}
