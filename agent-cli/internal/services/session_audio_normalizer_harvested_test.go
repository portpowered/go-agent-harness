package services

import (
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	harvestedVoiceSampleRate        = 24000
	harvestedVoiceTargetToleranceDB = 1.5
)

// The binary fixtures are embedded so this regression cannot accidentally read
// an operator scratchpad or a mutable working-directory capture.
//
//go:embed testdata/audio-normalizer/*.pcm
var harvestedVoiceFixtures embed.FS

type harvestedVoiceFixture struct {
	voice string
	file  string
}

func TestHarvestedVoiceFixturesTraverseProductionSessionNormalizer(t *testing.T) {
	fixtures := []harvestedVoiceFixture{
		{voice: "alloy", file: "alloy-turn-1.pcm"},
		{voice: "verse", file: "verse-turn-1.pcm"},
	}

	outputs := make(map[string][]byte, len(fixtures))
	rawRMS := make(map[string]float64, len(fixtures))
	for _, fixture := range fixtures {
		t.Run(fixture.voice, func(t *testing.T) {
			raw := readHarvestedVoiceFixture(t, fixture.file)
			rawStats, err := harvestedPCMStats(raw, harvestedVoiceSampleRate)
			if err != nil {
				t.Fatal(err)
			}
			rawRMS[fixture.voice] = rawStats.rmsDBFS

			normalized, boundaries := normalizeHarvestedVoiceThroughSessionBoundary(t, raw)
			assertHarvestedNormalizedVoicePCM(t, fixture.voice, raw, normalized, boundaries)
			outputs[fixture.voice] = normalized
		})
	}

	if gap := math.Abs(rawRMS["alloy"] - rawRMS["verse"]); gap <= 3 {
		t.Fatalf("pre-normalization voice RMS gap = %.3f dB, want the harvested >3 dB failure", gap)
	}
	if gap := harvestedPCMDBDelta(outputs["alloy"], outputs["verse"]); gap > 3 {
		t.Fatalf("post-normalization voice RMS gap = %.3f dB, want <= 3 dB", gap)
	}
}

func TestHarvestedVoiceFixturesTraverseProductionRoomBoundary(t *testing.T) {
	ids := []string{"alloy", "verse"}
	rawByID := make(map[string][]byte, len(ids))
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for _, id := range ids {
		file := id + "-turn-1.pcm"
		raw := readHarvestedVoiceFixture(t, file)
		rawByID[id] = raw
		inferencers[id] = &roomTestInferencer{events: harvestedVoiceRoomEvents(t, id, raw)}
	}

	outputs := make(chan roomAudioFrame, 512)
	fanouts := make(chan roomFanoutFrame, 512)
	inputs := make(chan roomAudioFrame, 4096)
	outputDir := filepath.Join(t.TempDir(), "room-run")
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = 15 * time.Second
	opts.MixerConfig = room.PCM16MixerConfig{
		Format:            room.PCM16Format{SampleRate: harvestedVoiceSampleRate, Channels: 1, FrameDuration: 20 * time.Millisecond},
		InputQueueFrames:  64,
		OutputQueueFrames: 256,
	}
	opts.OutputDir = outputDir
	opts.OnAudioOutput = func(id string, pcm []byte) error {
		outputs <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.OnAudioInput = func(id string, pcm []byte) error {
		inputs <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.onParticipantAudioFanned = func(sourceID, targetID string, pcm []byte) {
		fanouts <- roomFanoutFrame{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		runDone <- roomTestRunOutcome{result: result, err: err}
	}()

	outputByID := make(map[string][]byte, len(ids))
	outputBoundaries := make(map[string][]int, len(ids))
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	roomOutputsComplete := func() bool {
		for _, id := range ids {
			if len(outputByID[id]) < len(rawByID[id]) {
				return false
			}
		}
		return true
	}
	for !roomOutputsComplete() {
		select {
		case output := <-outputs:
			want := len(rawByID[output.id])
			outputByID[output.id] = append(outputByID[output.id], output.pcm...)
			outputBoundaries[output.id] = append(outputBoundaries[output.id], len(outputByID[output.id])/2)
			if len(outputByID[output.id]) > want {
				t.Fatalf("room output for %s = %d bytes, want unchanged %d", output.id, len(outputByID[output.id]), want)
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for harvested room outputs: %v", outputByID)
		}
	}
	for _, id := range ids {
		assertHarvestedNormalizedVoicePCM(t, id, rawByID[id], outputByID[id], outputBoundaries[id])
	}
	if gap := harvestedPCMDBDelta(outputByID[ids[0]], outputByID[ids[1]]); gap > 3 {
		t.Fatalf("room normalized voice RMS gap = %.3f dB, want <= 3 dB", gap)
	}

	wantPairs := map[string]struct{}{
		"alloy->verse": {},
		"verse->alloy": {},
	}
	fanoutByPair := make(map[string][]byte, len(wantPairs))
	for len(fanoutByPair) < len(wantPairs) {
		select {
		case fanout := <-fanouts:
			key := fanout.sourceID + "->" + fanout.targetID
			if _, wanted := wantPairs[key]; !wanted {
				t.Fatalf("unexpected harvested room fan-out %s", key)
			}
			fanoutByPair[key] = append(fanoutByPair[key], fanout.pcm...)
			if len(fanoutByPair[key]) > len(rawByID[fanout.sourceID]) {
				t.Fatalf("room fan-out %s = %d bytes, want unchanged %d", key, len(fanoutByPair[key]), len(rawByID[fanout.sourceID]))
			}
			if len(fanoutByPair[key]) == len(rawByID[fanout.sourceID]) && !bytes.Equal(fanoutByPair[key], outputByID[fanout.sourceID]) {
				t.Fatalf("room fan-out %s differs from its normalized output", key)
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for harvested room fan-outs: %v", fanoutByPair)
		}
	}

	// The mixer is the second room boundary. Wait for real signal on both
	// recipients, then assert that the mixed streams retain the normalized
	// loudness and safety properties rather than merely observing a callback.
	receivedByID := make(map[string][]byte, len(ids))
	readyInputs := make(map[string]struct{}, len(ids))
	for len(readyInputs) < len(ids) {
		select {
		case input := <-inputs:
			receivedByID[input.id] = append(receivedByID[input.id], input.pcm...)
			stats, err := harvestedPCMStats(receivedByID[input.id], harvestedVoiceSampleRate)
			if err == nil && stats.activeFrameCount >= 20 {
				readyInputs[input.id] = struct{}{}
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for harvested mixer delivery: %v", receivedByID)
		}
	}
	for _, id := range ids {
		stats, err := harvestedPCMStats(receivedByID[id], harvestedVoiceSampleRate)
		if err != nil {
			t.Fatalf("mixer output for %s: %v", id, err)
		}
		assertHarvestedNormalizationStats(t, "mixer output for "+id, stats)
	}

	cancel()
	select {
	case outcome := <-runDone:
		if outcome.err != nil {
			t.Fatalf("harvested room normalization run: %v", outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("harvested room normalization run did not terminate")
	}

	for _, id := range ids {
		sent, err := os.ReadFile(filepath.Join(outputDir, "participants", id, "sent.pcm"))
		if err != nil {
			t.Fatalf("read %s sent PCM: %v", id, err)
		}
		if !bytes.Equal(sent, outputByID[id]) {
			t.Fatalf("%s sent evidence differs from normalized room output", id)
		}
		wavData, err := os.ReadFile(filepath.Join(outputDir, "agent-"+id+".wav"))
		if err != nil {
			t.Fatalf("read %s WAV evidence: %v", id, err)
		}
		rate, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			t.Fatalf("decode %s WAV evidence: %v", id, err)
		}
		if rate != harvestedVoiceSampleRate || !bytes.Equal(pcm16Bytes(samples), outputByID[id]) {
			t.Fatalf("%s WAV evidence rate/samples = %d/%d, want %d/%d normalized samples", id, rate, len(samples), harvestedVoiceSampleRate, len(outputByID[id])/2)
		}
	}
}

func normalizeHarvestedVoiceThroughSessionBoundary(t *testing.T, pcm []byte) ([]byte, []int) {
	t.Helper()
	events := []messages.StreamMessage{{
		Type:  messages.StreamTypeAudioStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioStartValue(),
	}}
	events = append(events, harvestedVoiceAudioDeltas(t, pcm)...)
	events = append(events,
		messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)

	inferencer := &scriptedSessionInferencer{events: events}
	config := audio.DefaultPCM16NormalizerConfig
	config.SampleRate = harvestedVoiceSampleRate
	normalizer, err := newSessionAudioNormalizerInferencerWithConfig(inferencer, nil, config)
	if err != nil {
		t.Fatalf("construct production session normalizer: %v", err)
	}
	readContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := normalizer.ConnectSession(readContext)
	if err != nil {
		t.Fatalf("connect production session normalizer: %v", err)
	}

	var output []byte
	var boundaries []int
	for {
		msg, err := session.Receive().ReadContext(readContext)
		if err != nil {
			t.Fatalf("read normalized session before MESSAGE.END: %v", err)
		}
		if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
			value, ok := msg.Value.(*messages.AudioDeltaValue)
			if !ok || value == nil {
				t.Fatalf("normalized AUDIO.DELTA value = %T, want audio value", msg.Value)
			}
			if len(value.Content)%2 != 0 {
				t.Fatalf("normalized AUDIO.DELTA has odd byte length %d", len(value.Content))
			}
			output = append(output, value.Content...)
			boundaries = append(boundaries, len(output)/2)
		}
		if msg.Type == messages.StreamTypeMessageEnd {
			break
		}
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close normalized session: %v", err)
	}
	normalizer.wait()
	if err := normalizer.err(); err != nil {
		t.Fatalf("production session normalizer: %v", err)
	}
	return output, boundaries
}

func harvestedVoiceRoomEvents(t *testing.T, id string, pcm []byte) []messages.StreamMessage {
	t.Helper()
	events := []messages.StreamMessage{
		roomTestSessionOpen(id),
		{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()},
	}
	events = append(events, harvestedVoiceAudioDeltas(t, pcm)...)
	events = append(events,
		messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()},
		roomTestMessageEnd(),
	)
	return events
}

func harvestedVoiceAudioDeltas(t *testing.T, pcm []byte) []messages.StreamMessage {
	t.Helper()
	events := make([]messages.StreamMessage, 0, len(pcm)/1024)
	for offset, chunkIndex := 0, 0; offset < len(pcm); chunkIndex++ {
		chunkSize := []int{226, 2048, 634, 4096, 178}[chunkIndex%5]
		if remaining := len(pcm) - offset; chunkSize > remaining {
			chunkSize = remaining
		}
		if chunkSize%2 != 0 {
			chunkSize--
		}
		if chunkSize <= 0 {
			t.Fatalf("invalid provider chunk at offset %d", offset)
		}
		events = append(events, messages.StreamMessage{
			Type:  messages.StreamTypeAudioDelta,
			Role:  messages.RoleAssistant,
			Value: messages.NewAudioDeltaValue(append([]byte(nil), pcm[offset:offset+chunkSize]...)),
		})
		offset += chunkSize
	}
	return events
}

func assertHarvestedNormalizedVoicePCM(t *testing.T, label string, raw, normalized []byte, boundaries []int) {
	t.Helper()
	if len(normalized) != len(raw) {
		t.Fatalf("%s normalized bytes = %d, want unchanged %d", label, len(normalized), len(raw))
	}
	stats, err := harvestedPCMStats(normalized, harvestedVoiceSampleRate)
	if err != nil {
		t.Fatalf("%s normalized PCM: %v", label, err)
	}
	assertHarvestedNormalizationStats(t, label, stats)
	assertHarvestedOutputBoundaries(t, normalized, boundaries)
}

func assertHarvestedNormalizationStats(t *testing.T, label string, stats harvestedPCMStatistics) {
	t.Helper()
	if stats.activeFrameCount < 20 {
		t.Fatalf("%s active frames = %d, want a real speech excerpt", label, stats.activeFrameCount)
	}
	if delta := math.Abs(stats.activeRMSDBFS - audio.PCM16NormalizerTargetRMSDBFS); delta > harvestedVoiceTargetToleranceDB {
		t.Fatalf("%s active speech RMS = %.3f dBFS, target %.1f +/- %.1f dB", label, stats.activeRMSDBFS, audio.PCM16NormalizerTargetRMSDBFS, harvestedVoiceTargetToleranceDB)
	}
	peakCeiling := dbfsLinear(audio.PCM16NormalizerPeakCeilingDBFS)
	if stats.peak > peakCeiling {
		t.Fatalf("%s peak = %d, want <= -1 dBFS sample ceiling %d", label, stats.peak, peakCeiling)
	}
	if stats.clipCount != 0 {
		t.Fatalf("%s clipped samples = %d, want zero", label, stats.clipCount)
	}
	if stats.dcOffsetFS > 0.001 {
		t.Fatalf("%s absolute DC offset = %.6f full scale, want <= 0.001", label, stats.dcOffsetFS)
	}
}

type harvestedPCMStatistics struct {
	rmsDBFS          float64
	activeRMSDBFS    float64
	activeFrameCount int
	peak             int
	clipCount        int
	dcOffsetFS       float64
}

func harvestedPCMStats(pcm []byte, sampleRate int) (harvestedPCMStatistics, error) {
	if len(pcm)%2 != 0 {
		return harvestedPCMStatistics{}, fmt.Errorf("PCM16 fixture has odd byte length %d", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	if len(samples) == 0 {
		return harvestedPCMStatistics{}, fmt.Errorf("PCM16 fixture is empty")
	}
	var energy float64
	var sum int64
	peak := 0
	clipCount := 0
	for _, sample := range samples {
		value := float64(sample)
		energy += value * value
		sum += int64(sample)
		magnitude := int(sample)
		if magnitude < 0 {
			magnitude = -magnitude
		}
		if magnitude > peak {
			peak = magnitude
		}
		if magnitude >= audio.PCM16NormalizerClipSampleThreshold {
			clipCount++
		}
	}

	frameSamples := sampleRate / 50
	var activeEnergy float64
	activeSamples := 0
	activeFrames := 0
	silenceFloor := float64(dbfsLinear(audio.PCM16NormalizerSilenceFloorDBFS))
	for offset := 0; offset+frameSamples <= len(samples); offset += frameSamples {
		var frameEnergy float64
		for _, sample := range samples[offset : offset+frameSamples] {
			value := float64(sample)
			frameEnergy += value * value
		}
		frameRMS := math.Sqrt(frameEnergy / float64(frameSamples))
		if frameRMS > silenceFloor {
			activeEnergy += frameEnergy
			activeSamples += frameSamples
			activeFrames++
		}
	}
	if activeSamples == 0 {
		return harvestedPCMStatistics{}, fmt.Errorf("PCM16 fixture has no active speech frames")
	}
	return harvestedPCMStatistics{
		rmsDBFS:          pcmLinearDBFS(math.Sqrt(energy / float64(len(samples)))),
		activeRMSDBFS:    pcmLinearDBFS(math.Sqrt(activeEnergy / float64(activeSamples))),
		activeFrameCount: activeFrames,
		peak:             peak,
		clipCount:        clipCount,
		dcOffsetFS:       math.Abs(float64(sum)/float64(len(samples))) / float64(1<<15),
	}, nil
}

func readHarvestedVoiceFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := harvestedVoiceFixtures.ReadFile("testdata/audio-normalizer/" + name)
	if err != nil {
		t.Fatalf("read harvested voice fixture %q: %v", name, err)
	}
	return data
}

func assertHarvestedOutputBoundaries(t *testing.T, pcm []byte, boundaries []int) {
	t.Helper()
	if len(boundaries) < 2 {
		return
	}
	if len(pcm)%2 != 0 {
		t.Fatalf("normalized PCM has odd byte length %d", len(pcm))
	}
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	for _, boundary := range boundaries[:len(boundaries)-1] {
		if boundary <= 0 || boundary >= len(samples) {
			continue
		}
		delta := int(samples[boundary]) - int(samples[boundary-1])
		if delta < 0 {
			delta = -delta
		}
		if delta > 6000 {
			t.Fatalf("normalized output boundary at sample %d delta = %d, want <= 6000", boundary, delta)
		}
	}
}

func harvestedPCMDBDelta(first, second []byte) float64 {
	firstStats, firstErr := harvestedPCMStats(first, harvestedVoiceSampleRate)
	secondStats, secondErr := harvestedPCMStats(second, harvestedVoiceSampleRate)
	if firstErr != nil || secondErr != nil {
		return math.Inf(1)
	}
	return math.Abs(firstStats.activeRMSDBFS - secondStats.activeRMSDBFS)
}

func dbfsLinear(dbfs float64) int {
	return int(math.Floor(float64(1<<15) * math.Pow(10, dbfs/20)))
}

func pcmLinearDBFS(linear float64) float64 {
	if linear <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(linear/float64(1<<15))
}
