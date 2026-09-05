package cli

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const deviceProbeExpectedTranscript = "device round trip"

const deviceProbeScenarioPath = "testdata/probe-scenarios/s2s-v9-webrtc-device-roundtrip.scenario.json"

// TestS2SV9WebRTCDeviceProbeIsReachableThroughPublicCLI proves the device-tier
// entry point through the production router. The default test registry is
// intentionally headless, so this exercises the documented SKIP contract
// without opening hardware or requiring provider credentials.
func TestS2SV9WebRTCDeviceProbeIsReachableThroughPublicCLI(t *testing.T) {
	run := executeCLI("probe", "run", filepath.Clean(deviceProbeScenarioPath), "--devices", "real", "--json")
	if run.exitCode != 0 {
		t.Fatalf("device-tier probe exit code = %d, want successful skip; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.stdout)), &result); err != nil {
		t.Fatalf("decode device-tier result %q: %v", run.stdout, err)
	}
	if result["name"] != "s2s-v9-webrtc-device-roundtrip" || result["status"] != string(devicegw.DeviceProbeStatusSkip) {
		t.Fatalf("device-tier result = %v, want v9 scenario SKIP", result)
	}
	if result["reason_code"] != string(devicegw.DeviceProbeSkipNoDevices) || result["reason"] != "no audio input or output device" {
		t.Fatalf("device-tier skip = %v, want stable no-device code and reason", result)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.stderr)), &summary); err != nil {
		t.Fatalf("decode device-tier summary %q: %v", run.stderr, err)
	}
	if summary["status"] != string(devicegw.DeviceProbeStatusSkip) || summary["skipped"] != float64(1) || summary["failed"] != float64(0) {
		t.Fatalf("device-tier summary = %v, want one skipped scenario and no failures", summary)
	}
}

func TestS2SV9ScenarioLoadsThroughProbeSchema(t *testing.T) {
	data, err := os.ReadFile(deviceProbeScenarioPath)
	if err != nil {
		t.Fatalf("read committed v9 scenario: %v", err)
	}
	scenario, err := loadProbeScenario(data)
	if err != nil {
		t.Fatalf("load committed v9 scenario: %v", err)
	}
	if scenario.ID != "s2s-v9-webrtc-device-roundtrip" || len(scenario.Steps) != 2 || len(scenario.Expectations) != 2 {
		t.Fatalf("loaded v9 scenario = %#v, want one audio step, one close, and two expectations", scenario)
	}
	if scenario.Steps[0].Type != probe.StepSendAudio || scenario.Steps[1].Type != probe.StepClose {
		t.Fatalf("v9 steps = %#v, want send_audio followed by close", scenario.Steps)
	}
	input, err := serviceDevices.ProbeInputPlan(scenario)
	if err != nil {
		t.Fatalf("device input contract: %v", err)
	}
	if input.CorpusID != "utterance-hello-there" || input.Utterance != "The timer is ready for the next step." {
		t.Fatalf("device input contract = %#v, want authored corpus and utterance", input)
	}
	if scenario.Expectations[0].Type != probe.ExpectAudioEnergy || scenario.Expectations[1].Type != probe.ExpectTranscriptContains {
		t.Fatalf("v9 expectations = %#v, want audio energy and transcript assertions", scenario.Expectations)
	}
}

// TestS2SV9WebRTCDeviceCaptureProvesRegistryToSession exercises the device-tier
// capture boundary with the shared registry, the real RTC Opus tracks, and the
// session model runner. The virtual registry is the deterministic stand-in for
// a hardware host: its input/output pair preserves the same DeviceSource/
// DeviceSink ownership and frame contracts while keeping ordinary CI
// network-free.
func TestS2SV9WebRTCDeviceCaptureProvesRegistryToSession(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("create device registry: %v", err)
	}
	availability, err := devicegw.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("probe device availability: %v", err)
	}
	if availability.Status != devicegw.DeviceProbeStatusReady {
		t.Fatalf("virtual device probe status = %s, want ready (reason=%s)", availability.Status, availability.Reason)
	}
	input, err := registry.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("select default input device: %v", err)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("select default output device: %v", err)
	}

	source, err := devicegw.NewDeviceSource(registry, input.ID)
	if err != nil {
		t.Fatalf("open selected input %q: %v", input.ID, err)
	}
	defer func() { _ = source.Close() }()
	sink, err := devicegw.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatalf("open selected output %q: %v", output.ID, err)
	}
	defer func() { _ = sink.Close() }()

	peers, err := newDeviceProbePeerPair(t)
	if err != nil {
		t.Fatalf("negotiate local WebRTC peers: %v", err)
	}
	defer func() {
		_ = peers.sender.Close()
		_ = peers.receiver.Close()
	}()

	encoder, err := codec.NewOpusEncoder()
	if err != nil {
		t.Fatalf("create RTC Opus encoder: %v", err)
	}
	track, err := rtc.NewOutboundTrack(rtc.OutboundTrackConfig{
		SourceRate: audio.SampleRate,
		Encoder:    encoder,
		Writer:     deviceProbeRTPWriter{track: peers.localTrack},
		Pacer:      rtc.PacerFunc(func(context.Context, uint64) error { return nil }),
	})
	if err != nil {
		t.Fatalf("create outbound RTC track: %v", err)
	}
	defer func() { _ = track.Close() }()

	// The session runner is the same production boundary used by the live
	// session loop. It records the outbound messages so this proof observes
	// audio and the end-of-turn event after the media track, rather than merely
	// decoding a packet in the test.
	session := newDeviceProbeSession()
	runner := participants.NewSessionModelRunner(&deviceProbeSessionInferencer{session: session}, 8, nil)
	sessionContext, sessionCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sessionCancel()
	participant := participants.NewActiveParticipant(messages.Model, runner)
	participant.Start(sessionContext)
	defer participant.Stop()

	wantInput := voicedDeviceProbeFrame()
	if err := sink.WriteFrame(context.Background(), wantInput); err != nil {
		t.Fatalf("scripted device frame write: %v", err)
	}
	gotInput := make([]int16, audio.FrameSize)
	readContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := source.ReadFrame(readContext, gotInput); err != nil {
		t.Fatalf("selected device capture read: %v", err)
	}
	if err := assertDeviceProbeEnergy("captured input", gotInput); err != nil {
		t.Fatal(err)
	}

	// The device contract emits 30 ms frames at 16 kHz, while one RTC Opus
	// packet is 20 ms at 48 kHz. Feed one complete Opus-sized prefix from the
	// captured device frame and leave packetization to the outbound track.
	rtcInput := gotInput[:audio.SampleRate*codec.OpusFrameSamples/codec.OpusSampleRate]
	if err := track.WriteFrame(context.Background(), audio.PCMFrame{Samples: rtcInput}); err != nil {
		t.Fatalf("commit captured frame to WebRTC track: %v", err)
	}

	remote, err := peers.waitRemoteTrack()
	if err != nil {
		t.Fatalf("receive remote WebRTC track: %v", err)
	}
	if remote.Kind() != webrtc.RTPCodecTypeAudio {
		t.Fatalf("remote track kind = %s, want audio", remote.Kind())
	}

	decoder, err := codec.NewOpusDecoder()
	if err != nil {
		t.Fatalf("create RTC Opus decoder: %v", err)
	}
	defer func() { _ = decoder.Close() }()
	// Pion exposes RTP attributes alongside the packet; the harness seam
	// deliberately keeps those protocol details out of InboundTrack.
	// deviceProbeRTPPacketSource performs that boundary adaptation.
	inbound, err := rtc.NewInboundTrack(deviceProbeRTPPacketSource{track: remote}, decoder, rtc.InboundTrackConfig{
		SampleRate:    audio.SampleRate,
		FrameDuration: codec.OpusFrameDuration,
		JitterDepth:   codec.OpusFrameDuration,
	})
	if err != nil {
		t.Fatalf("bind remote track to session input: %v", err)
	}
	defer func() { _ = inbound.Close() }()

	gotSessionInput, err := inbound.ReadFrame(sessionContext)
	if err != nil {
		t.Fatalf("read active session input track: %v", err)
	}
	if err := assertDeviceProbeEnergy("session input", gotSessionInput.Samples); err != nil {
		t.Fatal(err)
	}
	wantSessionSamples := int(int64(audio.SampleRate) * int64(codec.OpusFrameDuration) / int64(time.Second))
	if len(gotSessionInput.Samples) != wantSessionSamples {
		t.Fatalf("session input samples = %d, want one 20 ms 16 kHz frame", len(gotSessionInput.Samples))
	}

	pcm := pcm16ProbeBytes(gotSessionInput.Samples)
	select {
	case runner.UserAudioInbox <- pcm:
	case <-sessionContext.Done():
		t.Fatalf("send captured audio to session: %v", sessionContext.Err())
	}
	audioMessage := readDeviceProbeSessionMessage(t, sessionContext, session.sent)
	if audioMessage.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("session audio message type = %s, want %s", audioMessage.Type, messages.StreamTypeAudioDelta)
	}
	audioValue, ok := audioMessage.Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("session audio message value = %T, want *messages.AudioDeltaValue", audioMessage.Value)
	}
	if !bytes.Equal(audioValue.Content, pcm) {
		t.Fatalf("session audio bytes differ from active input track: got %d bytes, want %d", len(audioValue.Content), len(pcm))
	}

	select {
	case runner.UserEventInbox <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd}:
	case <-sessionContext.Done():
		t.Fatalf("send captured turn boundary to session: %v", sessionContext.Err())
	}
	turnMessage := readDeviceProbeSessionMessage(t, sessionContext, session.sent)
	if turnMessage.Type != messages.StreamTypeMessageEnd {
		t.Fatalf("session turn message type = %s, want %s after audio", turnMessage.Type, messages.StreamTypeMessageEnd)
	}

	// The provider-side response is delivered through the same production
	// session runner boundary as a live session. Route its raw PCM delta to the
	// selected output device, while the tap records the exact frame accepted by
	// the device sink. The virtual registry loops that output back to the
	// selected input, allowing this CI-safe proof to observe the emitted frame
	// through the same registry binding surface.
	responseSamples := voicedDeviceProbeFrame()
	responsePCM := pcm16ProbeBytes(responseSamples)
	for _, responseMessage := range []messages.StreamMessage{
		{Type: messages.StreamTypeAudioStart, Value: messages.NewAudioStartValue()},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("device ")},
		{Type: messages.StreamTypeTranscriptDelta, Value: messages.NewTranscriptDeltaValue("round trip")},
		{Type: messages.StreamTypeTranscriptEnd, Value: messages.NewTranscriptEndValue(deviceProbeExpectedTranscript)},
		{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(responsePCM)},
		{Type: messages.StreamTypeAudioEnd, Value: messages.NewAudioEndValue()},
		{Type: messages.StreamTypeMessageEnd},
	} {
		if !session.receive.Write(sessionContext, responseMessage) {
			t.Fatalf("queue session response event %s: %v", responseMessage.Type, sessionContext.Err())
		}
	}
	recognizedTranscript, err := readDeviceProbeTranscript(t, sessionContext, runner.DeltaOutbox)
	if err != nil {
		t.Fatal(err)
	}
	if violation := assertDeviceProbeTranscript(recognizedTranscript, deviceProbeExpectedTranscript); violation != nil {
		t.Fatal(violation)
	}
	responseDelta := readDeviceProbeDelta(t, sessionContext, runner.DeltaOutbox, messages.StreamTypeAudioDelta)
	responseValue, ok := responseDelta.Value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("response audio delta value = %T, want *messages.AudioDeltaValue", responseDelta.Value)
	}
	if !bytes.Equal(responseValue.Content, responsePCM) {
		t.Fatalf("response audio bytes changed before speaker emission: got %d bytes, want %d", len(responseValue.Content), len(responsePCM))
	}

	outputTap := &deviceProbeOutputTap{sink: sink}
	if err := outputTap.WriteFrame(sessionContext, responseSamples); err != nil {
		t.Fatalf("write session response to selected output device: %v", err)
	}
	emitted := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(sessionContext, emitted); err != nil {
		t.Fatalf("tap selected output device emission: %v", err)
	}
	if !bytes.Equal(pcm16ProbeBytes(emitted), responsePCM) {
		t.Fatalf("emitted speaker frame changed: got %d bytes, want %d", len(pcm16ProbeBytes(emitted)), len(responsePCM))
	}
	if err := assertDeviceProbeEnergy("speaker output", emitted); err != nil {
		t.Fatal(err)
	}
	if got := outputTap.LastRMS(); got != pcm16ProbeRMS(emitted) {
		t.Fatalf("speaker tap RMS = %.2f, loopback RMS = %.2f, want equal measurements", got, pcm16ProbeRMS(emitted))
	}

	observations := registry.Observations()
	if observations.OpenCount != 2 || observations.ReleaseCount != 0 {
		t.Fatalf("registry observations before cleanup = %+v, want two opens and live handles", observations)
	}
}

// TestS2SV9WebRTCDeviceTranscriptAssertionReportsActualText is the negative
// control for transcript validation. Empty and unrelated recognition results
// must fail with the observed text so a device-tier failure remains actionable.
func TestS2SV9WebRTCDeviceTranscriptAssertionReportsActualText(t *testing.T) {
	tests := []struct {
		name   string
		actual string
	}{
		{name: "empty", actual: ""},
		{name: "mismatched", actual: "unrelated recognition"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			violation := assertDeviceProbeTranscript(tc.actual, deviceProbeExpectedTranscript)
			if violation == nil {
				t.Fatalf("transcript %q passed validation for expected %q", tc.actual, deviceProbeExpectedTranscript)
			}
			if !strings.Contains(violation.Error(), fmt.Sprintf("recognized transcript = %q", tc.actual)) {
				t.Fatalf("transcript violation = %q, want actual transcript %q", violation, tc.actual)
			}
		})
	}
}

// TestS2SV9WebRTCDeviceOutputSilenceFailsEnergyAssertion is the negative
// control for the output proof. A silent frame still reaches the selected
// output sink and loopback, but the exact device-tier assertion must reject it
// and include the measured RMS and established VAD threshold in its error.
func TestS2SV9WebRTCDeviceOutputSilenceFailsEnergyAssertion(t *testing.T) {
	registry, err := devicegw.NewVirtualRegistry(devicegw.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("create device registry: %v", err)
	}
	input, err := registry.Default(devicegw.DirectionInput)
	if err != nil {
		t.Fatalf("select default input device: %v", err)
	}
	output, err := registry.Default(devicegw.DirectionOutput)
	if err != nil {
		t.Fatalf("select default output device: %v", err)
	}
	source, err := devicegw.NewDeviceSource(registry, input.ID)
	if err != nil {
		t.Fatalf("open selected input %q: %v", input.ID, err)
	}
	defer func() { _ = source.Close() }()
	sink, err := devicegw.NewDeviceSink(registry, output.ID)
	if err != nil {
		t.Fatalf("open selected output %q: %v", output.ID, err)
	}
	defer func() { _ = sink.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	silence := make([]int16, audio.FrameSize)
	if err := sink.WriteFrame(ctx, silence); err != nil {
		t.Fatalf("write silent output frame: %v", err)
	}
	emitted := make([]int16, audio.FrameSize)
	if err := source.ReadFrame(ctx, emitted); err != nil {
		t.Fatalf("read silent output loopback: %v", err)
	}
	violation := assertDeviceProbeEnergy("speaker output", emitted)
	if violation == nil {
		t.Fatal("silent speaker output passed the energy assertion")
	}
	if !strings.Contains(violation.Error(), "speaker output RMS = 0.00") ||
		!strings.Contains(violation.Error(), fmt.Sprintf("want > %.2f", audio.DefaultVADConfig.EnergyThreshold)) {
		t.Fatalf("silence assertion error = %q, want measured RMS and threshold", violation)
	}
}

type deviceProbeOutputTap struct {
	sink audio.AudioSink
	rms  []float64
}

func (t *deviceProbeOutputTap) WriteFrame(ctx context.Context, frame []int16) error {
	if err := t.sink.WriteFrame(ctx, frame); err != nil {
		return err
	}
	t.rms = append(t.rms, pcm16ProbeRMS(frame))
	return nil
}

func (t *deviceProbeOutputTap) LastRMS() float64 {
	if len(t.rms) == 0 {
		return 0
	}
	return t.rms[len(t.rms)-1]
}

func assertDeviceProbeEnergy(label string, samples []int16) error {
	rms := pcm16ProbeRMS(samples)
	threshold := audio.DefaultVADConfig.EnergyThreshold
	if rms <= threshold {
		return fmt.Errorf("%s RMS = %.2f, want > %.2f (silence threshold)", label, rms, threshold)
	}
	return nil
}

func assertDeviceProbeTranscript(actual, expected string) error {
	actual = strings.TrimSpace(actual)
	expected = strings.TrimSpace(expected)
	if actual == "" {
		return fmt.Errorf("recognized transcript = %q, want non-empty text matching scripted utterance %q", actual, expected)
	}
	if expected == "" || !strings.Contains(strings.ToLower(actual), strings.ToLower(expected)) {
		return fmt.Errorf("recognized transcript = %q, want text matching scripted utterance %q", actual, expected)
	}
	return nil
}

func readDeviceProbeTranscript(t *testing.T, ctx context.Context, out *messages.TypedBuffer[messages.StreamMessage]) (string, error) {
	t.Helper()
	var deltas strings.Builder
	for {
		message, ok := out.ReadBlockingContext(ctx)
		if !ok {
			return "", fmt.Errorf("read recognized transcript: %w", ctx.Err())
		}
		switch message.Type {
		case messages.StreamTypeTranscriptDelta:
			value, ok := message.Value.(*messages.TranscriptDeltaValue)
			if !ok {
				return "", fmt.Errorf("recognized transcript delta value = %T, want *messages.TranscriptDeltaValue", message.Value)
			}
			deltas.WriteString(value.Text)
		case messages.StreamTypeTranscriptEnd:
			value, ok := message.Value.(*messages.TranscriptEndValue)
			if !ok {
				return "", fmt.Errorf("recognized transcript end value = %T, want *messages.TranscriptEndValue", message.Value)
			}
			if value.FullText != "" {
				return value.FullText, nil
			}
			return deltas.String(), nil
		case messages.StreamTypeMessageEnd:
			return deltas.String(), fmt.Errorf("recognized transcript missing TRANSCRIPT.END before MESSAGE.END; actual transcript = %q", deltas.String())
		}
	}
}

func readDeviceProbeDelta(t *testing.T, ctx context.Context, out *messages.TypedBuffer[messages.StreamMessage], want messages.StreamMessageType) messages.StreamMessage {
	t.Helper()
	for {
		message, ok := out.ReadBlockingContext(ctx)
		if !ok {
			t.Fatalf("read session delta %s: %v", want, ctx.Err())
			return messages.StreamMessage{}
		}
		if message.Type == want {
			return message
		}
	}
}

type deviceProbeRTPWriter struct {
	track *webrtc.TrackLocalStaticRTP
}

func (w deviceProbeRTPWriter) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return w.track.WriteRTP(packet)
}

type deviceProbeRTPPacketSource struct {
	track *webrtc.TrackRemote
}

func (s deviceProbeRTPPacketSource) ReadRTP() (*rtp.Packet, error) {
	packet, _, err := s.track.ReadRTP()
	return packet, err
}

type deviceProbePeerPair struct {
	sender      *webrtc.PeerConnection
	receiver    *webrtc.PeerConnection
	localTrack  *webrtc.TrackLocalStaticRTP
	remoteTrack *webrtc.TrackRemote
	remoteReady <-chan *webrtc.TrackRemote
}

func newDeviceProbePeerPair(t *testing.T) (*deviceProbePeerPair, error) {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   rtc.OutboundRTPClockRate,
			Channels:    1,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}
	if err := mediaEngine.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	sender, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = sender.Close()
		return nil, err
	}
	cleanup := func(err error) (*deviceProbePeerPair, error) {
		_ = sender.Close()
		_ = receiver.Close()
		return nil, err
	}

	connected := make(chan struct{})
	var connectedOnce sync.Once
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() {
				close(connected)
			})
		}
	})
	remoteReady := make(chan *webrtc.TrackRemote, 1)
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case remoteReady <- track:
		default:
		}
	})

	localTrack, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, "audio", "s2s-v9-device")
	if err != nil {
		return cleanup(err)
	}
	if _, err := sender.AddTrack(localTrack); err != nil {
		return cleanup(err)
	}
	offer, err := sender.CreateOffer(nil)
	if err != nil {
		return cleanup(err)
	}
	if err := sender.SetLocalDescription(offer); err != nil {
		return cleanup(err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(sender):
	case <-time.After(2 * time.Second):
		return cleanup(context.DeadlineExceeded)
	}
	if err := receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
		return cleanup(err)
	}
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		return cleanup(err)
	}
	if err := receiver.SetLocalDescription(answer); err != nil {
		return cleanup(err)
	}
	select {
	case <-webrtc.GatheringCompletePromise(receiver):
	case <-time.After(2 * time.Second):
		return cleanup(context.DeadlineExceeded)
	}
	if err := sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		return cleanup(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		return cleanup(context.DeadlineExceeded)
	}

	return &deviceProbePeerPair{
		sender: sender, receiver: receiver, localTrack: localTrack,
		remoteReady: remoteReady,
	}, nil
}

func (p *deviceProbePeerPair) waitRemoteTrack() (*webrtc.TrackRemote, error) {
	select {
	case p.remoteTrack = <-p.remoteReady:
	case <-time.After(3 * time.Second):
		return nil, context.DeadlineExceeded
	}
	return p.remoteTrack, nil
}

func voicedDeviceProbeFrame() []int16 {
	frame := make([]int16, audio.FrameSize)
	for index := range frame {
		frame[index] = int16(1400 * math.Sin(2*math.Pi*440*float64(index)/audio.SampleRate))
	}
	return frame
}

func pcm16ProbeRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		value := float64(sample)
		sum += value * value
	}
	return math.Sqrt(sum / float64(len(samples)))
}

func pcm16ProbeBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

type deviceProbeSessionInferencer struct {
	session messages.Session
}

func (i *deviceProbeSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type deviceProbeSession struct {
	sent    chan messages.StreamMessage
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newDeviceProbeSession() *deviceProbeSession {
	return &deviceProbeSession{
		sent:    make(chan messages.StreamMessage, 8),
		receive: messages.NewTypedBuffer[messages.StreamMessage](16),
		done:    make(chan struct{}),
	}
}

func (s *deviceProbeSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	select {
	case s.sent <- message:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *deviceProbeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *deviceProbeSession) Done() <-chan struct{} { return s.done }

func (s *deviceProbeSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func readDeviceProbeSessionMessage(t *testing.T, ctx context.Context, messagesCh <-chan messages.StreamMessage) messages.StreamMessage {
	t.Helper()
	select {
	case message := <-messagesCh:
		return message
	case <-ctx.Done():
		t.Fatalf("read session message: %v", ctx.Err())
		return messages.StreamMessage{}
	}
}
