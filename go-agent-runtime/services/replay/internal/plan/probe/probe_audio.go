package probe

import (
	"encoding/json"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	replaycapture "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/capture"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func injectProbeAudio(capture gatewaytesting.SessionCapture, request replay.CaptureProbeRequest) (gatewaytesting.SessionCapture, error) {
	if err := validateProbeAudioRequest(request); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	frames := probePCMFrames(request.AudioSamples)
	appendSlots, hasCancel := probeAudioLayout(capture.Records)
	if appendSlots == 0 {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture has no input_audio_buffer.append slot for corpus %q", request.CorpusID)
	}
	if hasCancel && len(frames) < appendSlots {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("audio corpus %q has %d frames but replay fixture reserves %d append slots before response.cancel", request.CorpusID, len(frames), appendSlots)
	}

	injector := newProbeAudioInjector(capture.Records, frames, appendSlots, hasCancel)
	for _, record := range capture.Records {
		if err := injector.consume(record); err != nil {
			return gatewaytesting.SessionCapture{}, err
		}
	}
	if injector.frameIndex != len(frames) {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("replay fixture did not receive all %q PCM frames: injected %d of %d", request.CorpusID, injector.frameIndex, len(frames))
	}
	for index := range injector.records {
		injector.records[index].Sequence = index + 1
	}
	injected := capture
	injected.Records = injector.records
	if err := validateProbeAudio(injected, frames); err != nil {
		return gatewaytesting.SessionCapture{}, err
	}
	sealed, err := replaycapture.SealReplayCapture(injected)
	if err != nil {
		return gatewaytesting.SessionCapture{}, fmt.Errorf("seal injected replay capture: %w", err)
	}
	return sealed, nil
}

func validateProbeAudioRequest(request replay.CaptureProbeRequest) error {
	if request.SampleRateHz <= 0 || request.ExpectedSampleRateHz <= 0 || request.SampleRateHz != request.ExpectedSampleRateHz {
		return fmt.Errorf("audio corpus %q sample rate = %d, want %d", request.CorpusID, request.SampleRateHz, request.ExpectedSampleRateHz)
	}
	if len(request.AudioSamples) == 0 {
		return fmt.Errorf("audio corpus %q contains no PCM16 samples", request.CorpusID)
	}
	return nil
}

func probeAudioLayout(records []gatewaytesting.CapturedSessionEvent) (appendSlots int, hasCancel bool) {
	for _, record := range records {
		if record.Direction != gatewaytesting.DirectionClientToServer {
			continue
		}
		if record.Type == probeAudioAppend {
			appendSlots++
		}
		if isProbeResponseCancel(record.Type) {
			hasCancel = true
		}
	}
	return appendSlots, hasCancel
}

type probeAudioInjector struct {
	records        []gatewaytesting.CapturedSessionEvent
	frames         [][]byte
	frameIndex     int
	hasCancel      bool
	firstAppend    bool
	suffixInserted bool
}

func newProbeAudioInjector(records []gatewaytesting.CapturedSessionEvent, frames [][]byte, appendSlots int, hasCancel bool) *probeAudioInjector {
	return &probeAudioInjector{
		records:     make([]gatewaytesting.CapturedSessionEvent, 0, len(records)+len(frames)-appendSlots),
		frames:      frames,
		hasCancel:   hasCancel,
		firstAppend: true,
	}
}

func (injector *probeAudioInjector) consume(record gatewaytesting.CapturedSessionEvent) error {
	if record.Direction == gatewaytesting.DirectionClientToServer && record.Type == probeAudioAppend {
		return injector.replaceAppend(record)
	}
	injector.records = append(injector.records, record)
	if injector.shouldInsertSuffix(record) {
		return injector.insertSuffix(record)
	}
	return nil
}

func (injector *probeAudioInjector) replaceAppend(record gatewaytesting.CapturedSessionEvent) error {
	if injector.hasCancel {
		if injector.suffixInserted {
			return nil
		}
		injector.firstAppend = false
		return injector.appendFrame(record)
	}
	if !injector.firstAppend {
		return nil
	}
	for range injector.frames {
		if err := injector.appendFrame(record); err != nil {
			return err
		}
	}
	injector.firstAppend = false
	return nil
}

func (injector *probeAudioInjector) shouldInsertSuffix(record gatewaytesting.CapturedSessionEvent) bool {
	return injector.hasCancel && !injector.suffixInserted &&
		record.Direction == gatewaytesting.DirectionClientToServer && isProbeResponseCancel(record.Type)
}

func (injector *probeAudioInjector) insertSuffix(record gatewaytesting.CapturedSessionEvent) error {
	for injector.frameIndex < len(injector.frames) {
		if err := injector.appendFrame(record); err != nil {
			return err
		}
	}
	injector.suffixInserted = true
	return nil
}

func (injector *probeAudioInjector) appendFrame(template gatewaytesting.CapturedSessionEvent) error {
	appendRecord, err := probeAudioAppendRecord(template, injector.frames[injector.frameIndex])
	if err != nil {
		return err
	}
	injector.records = append(injector.records, appendRecord)
	injector.frameIndex++
	return nil
}

func probePCMFrames(samples []int16) [][]byte {
	frames := make([][]byte, 0, (len(samples)+audio.FrameSize-1)/audio.FrameSize)
	for start := 0; start < len(samples); start += audio.FrameSize {
		frame := make([]int16, audio.FrameSize)
		copy(frame, samples[start:])
		frames = append(frames, codec.EncodePCM16(frame))
	}
	return frames
}

func probeAudioAppendRecord(template gatewaytesting.CapturedSessionEvent, pcm []byte) (gatewaytesting.CapturedSessionEvent, error) {
	payload, err := json.Marshal(struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}{Type: probeAudioAppend, Audio: codec.EncodeBase64(pcm)})
	if err != nil {
		return gatewaytesting.CapturedSessionEvent{}, fmt.Errorf("encode replay audio append: %w", err)
	}
	template.PayloadType = gatewaytesting.SessionPayloadTypeWebSocketMessage
	template.Payload = payload
	template.Data = nil
	template.Type = probeAudioAppend
	return template, nil
}

func validateProbeAudio(capture gatewaytesting.SessionCapture, frames [][]byte) error {
	actual := make([]byte, 0, len(frames)*audio.FrameSize*2)
	appendCount := 0
	for _, record := range capture.Records {
		if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != probeAudioAppend {
			continue
		}
		appendCount++
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
		}
		if err := json.Unmarshal(probeRecordPayload(record), &event); err != nil {
			return fmt.Errorf("decode injected input_audio_buffer.append payload: %w", err)
		}
		if event.Type != probeAudioAppend || event.Audio == "" {
			return fmt.Errorf("injected input_audio_buffer.append payload is missing its audio field")
		}
		pcm, err := codec.DecodeBase64(event.Audio)
		if err != nil {
			return fmt.Errorf("decode injected input audio: %w", err)
		}
		actual = append(actual, pcm...)
	}
	expected := make([]byte, 0, len(frames)*audio.FrameSize*2)
	for _, frame := range frames {
		expected = append(expected, frame...)
	}
	if appendCount != len(frames) {
		return fmt.Errorf("injected replay has %d append payloads for %d PCM frames", appendCount, len(frames))
	}
	if !bytesEqual(actual, expected) {
		return fmt.Errorf("injected replay append payloads do not equal the resolved corpus PCM")
	}
	return nil
}
