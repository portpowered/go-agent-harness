package cli

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	deviceProbeDefaultCaptureDuration = 5 * time.Second
	deviceProbeProviderSampleRate     = wavio.Rate24kHz
	deviceProbeInputSampleRate        = audio.SampleRate
	deviceProbeFrameDuration          = 20 * time.Millisecond
	deviceProbeInputFrameSamples      = deviceProbeInputSampleRate / 50
	deviceProbeProviderFrameSamples   = deviceProbeProviderSampleRate / 50
)

type deviceProbeRuntimeOptions struct {
	Provider    string
	Model       string
	APIKey      string
	BaseURL     string
	ConfigDir   string
	CaptureTime time.Duration

	// SessionInferencer is a hermetic seam for runtime tests. The command
	// constructor leaves it nil, which selects the configured live provider.
	SessionInferencer messages.SessionInferencer
	Instructions      string
	WebSocketDialer   transport.Dialer
}

func runDeviceProbeScenario(ctx context.Context, scenario probe.Scenario, availability audio.DeviceProbeAvailability, registry audio.DeviceRegistry, opts deviceProbeRuntimeOptions) (observation probe.ObservationSnapshot, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if availability.Status != audio.DeviceProbeStatusReady {
		return observation, fmt.Errorf("device probe cannot run with availability status %q", availability.Status)
	}
	if len(availability.InputDevices) == 0 || len(availability.OutputDevices) == 0 {
		return observation, fmt.Errorf("device probe ready snapshot has no input/output device")
	}

	inputDevice := selectLiveDeviceProbeDevice(registry, availability.InputDevices, audio.DirectionInput)
	outputDevice := selectLiveDeviceProbeDevice(registry, availability.OutputDevices, audio.DirectionOutput)
	source, err := audio.NewDeviceSource(registry, inputDevice.ID)
	if err != nil {
		return observation, fmt.Errorf("open selected input device %q (%s): %w", inputDevice.ID, inputDevice.Display(), err)
	}
	defer func() { runErr = errors.Join(runErr, closeDeviceProbeResource("input device", source.Close)) }()
	sink, err := audio.NewDeviceSink(registry, outputDevice.ID)
	if err != nil {
		return observation, fmt.Errorf("open selected output device %q (%s): %w", outputDevice.ID, outputDevice.Display(), err)
	}
	defer func() { runErr = errors.Join(runErr, closeDeviceProbeResource("output device", sink.Close)) }()

	inputLink, err := newLiveDeviceProbeMediaLink()
	if err != nil {
		return observation, fmt.Errorf("create microphone WebRTC path: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, closeDeviceProbeResource("microphone WebRTC path", inputLink.Close))
	}()
	outputLink, err := newLiveDeviceProbeMediaLink()
	if err != nil {
		return observation, fmt.Errorf("create speaker WebRTC path: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, closeDeviceProbeResource("speaker WebRTC path", outputLink.Close))
	}()

	instructions := opts.Instructions
	if strings.TrimSpace(instructions) == "" {
		instructions = deviceProbeInstructions(scenario)
	}
	inferencer := opts.SessionInferencer
	if inferencer == nil {
		var model string
		inferencer, model, err = services.NewLiveSessionInferencer(services.SessionRunOptions{
			Provider:        opts.Provider,
			Model:           opts.Model,
			APIKey:          opts.APIKey,
			BaseURL:         opts.BaseURL,
			ConfigDir:       opts.ConfigDir,
			WebSocketDialer: opts.WebSocketDialer,
		}, instructions)
		if err != nil {
			return observation, fmt.Errorf("create live realtime session (%s): %w", model, err)
		}
	}

	runner := participants.NewSessionModelRunner(inferencer, 128, nil)
	runnerContext, runnerCancel := context.WithCancel(ctx)
	defer runnerCancel()
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runner.Run(runnerContext) }()

	bridge := newLiveDeviceProbeSessionBridge(runner, outputLink, sink)
	bridgeContext, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	go bridge.Run(bridgeContext)

	runnerFinished := make(chan struct{})
	go func() {
		runnerErr := <-runnerDone
		if runnerErr != nil && !errors.Is(runnerErr, context.Canceled) && !errors.Is(runnerErr, context.DeadlineExceeded) {
			bridge.setError(fmt.Errorf("session runner: %w", runnerErr))
		}
		bridge.finishResponse()
		close(runnerFinished)
	}()
	if err := bridge.waitOpened(ctx); err != nil {
		return observation, err
	}

	captureDuration := opts.CaptureTime
	if captureDuration <= 0 {
		captureDuration = deviceProbeDefaultCaptureDuration
	}
	captureContext, captureCancel := context.WithTimeout(ctx, captureDuration)
	inputFrames, inputRMS, err := captureLiveDeviceProbeInput(captureContext, source, inputLink, runner)
	captureCancel()
	if err != nil {
		return observation, err
	}
	if inputFrames == 0 {
		return observation, fmt.Errorf("selected microphone produced no complete 20 ms frames")
	}
	if inputRMS <= audio.DefaultVADConfig.EnergyThreshold {
		return observation, fmt.Errorf("selected microphone RMS = %.2f, want > %.2f (silence threshold)", inputRMS, audio.DefaultVADConfig.EnergyThreshold)
	}
	select {
	case runner.UserEventInbox <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd}:
	case <-ctx.Done():
		return observation, ctx.Err()
	}
	if err := bridge.waitResponse(ctx); err != nil {
		return observation, err
	}

	runnerCancel()
	select {
	case <-runnerFinished:
	case <-time.After(time.Second):
		return observation, errors.New("session runner did not stop after response completion")
	}
	if err := bridge.errorValue(nil); err != nil {
		return observation, err
	}
	return bridge.snapshot(), nil
}

func selectLiveDeviceProbeDevice(registry audio.DeviceRegistry, candidates []audio.Device, direction audio.Direction) audio.Device {
	if registry != nil {
		if defaultDevice, err := registry.Default(direction); err == nil {
			for _, candidate := range candidates {
				if candidate.ID == defaultDevice.ID {
					return candidate
				}
			}
		}
	}
	return candidates[0]
}

func closeDeviceProbeResource(name string, close func() error) error {
	if err := close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

// liveDeviceProbeMediaLink is one direction of the local negotiated WebRTC
// path. It does not expose a direct PCM shortcut: every frame is packetized by
// the real RTC Opus track, received by a Pion TrackRemote, decoded, and only
// then returned to the caller.
type liveDeviceProbeMediaLink struct {
	peers    *liveDeviceProbePeerPair
	outbound *rtc.OutboundTrack
	inbound  *rtc.InboundTrack
	decoder  *rtc.RTCOpusDecoder
}

func newLiveDeviceProbeMediaLink() (*liveDeviceProbeMediaLink, error) {
	peers, err := newLiveDeviceProbePeerPair()
	if err != nil {
		return nil, err
	}
	encoder, err := rtc.NewRTCOpusEncoder()
	if err != nil {
		_ = peers.Close()
		return nil, fmt.Errorf("create RTC Opus encoder: %w", err)
	}
	outbound, err := rtc.NewOutboundTrack(rtc.OutboundTrackConfig{
		SourceRate: deviceProbeInputSampleRate,
		Encoder:    encoder,
		Writer:     liveDeviceProbeRTPWriter{track: peers.localTrack},
		Pacer:      rtc.PacerFunc(func(context.Context, uint64) error { return nil }),
	})
	if err != nil {
		_ = peers.Close()
		_ = encoder.Close()
		return nil, fmt.Errorf("create outbound RTC track: %w", err)
	}
	return &liveDeviceProbeMediaLink{peers: peers, outbound: outbound}, nil
}

func (l *liveDeviceProbeMediaLink) RoundTrip(ctx context.Context, samples []int16) ([]int16, error) {
	if len(samples) != deviceProbeInputFrameSamples {
		return nil, fmt.Errorf("RTC media frame has %d samples, want %d", len(samples), deviceProbeInputFrameSamples)
	}
	if err := l.outbound.WriteFrame(ctx, rtc.PCMFrame{Samples: samples}); err != nil {
		return nil, err
	}
	if l.inbound == nil {
		remote, err := l.peers.waitRemoteTrack(ctx)
		if err != nil {
			return nil, fmt.Errorf("receive negotiated remote RTC track: %w", err)
		}
		l.decoder, err = rtc.NewRTCOpusDecoder()
		if err != nil {
			return nil, fmt.Errorf("create RTC Opus decoder: %w", err)
		}
		l.inbound, err = rtc.NewInboundTrack(liveDeviceProbeRTPPacketSource{track: remote}, l.decoder, rtc.InboundTrackConfig{
			SampleRate:    deviceProbeInputSampleRate,
			FrameDuration: deviceProbeFrameDuration,
			JitterDepth:   deviceProbeFrameDuration,
		})
		if err != nil {
			_ = l.decoder.Close()
			l.decoder = nil
			return nil, fmt.Errorf("bind negotiated remote RTC track: %w", err)
		}
	}
	frame, err := l.inbound.ReadFrame(ctx)
	if err != nil {
		return nil, err
	}
	return frame.Samples, nil
}

func (l *liveDeviceProbeMediaLink) Close() error {
	if l == nil {
		return nil
	}
	var closeErr error
	if l.inbound != nil {
		closeErr = errors.Join(closeErr, l.inbound.Close())
	}
	if l.decoder != nil {
		closeErr = errors.Join(closeErr, l.decoder.Close())
	}
	if l.outbound != nil {
		closeErr = errors.Join(closeErr, l.outbound.Close())
	}
	if l.peers != nil {
		closeErr = errors.Join(closeErr, l.peers.Close())
	}
	return closeErr
}

type liveDeviceProbeSessionBridge struct {
	runner *participants.ModelRunner
	output *liveDeviceProbeMediaLink
	sink   audio.AudioSink

	opened       chan struct{}
	responseDone chan struct{}
	done         chan struct{}
	openOnce     sync.Once
	responseOnce sync.Once

	mu             sync.Mutex
	transcript     strings.Builder
	fullTranscript string
	outputSamples  []int16
	outputFrames   int
	err            error
	audioBuffer    []int16
	sinkBuffer     []int16
}

func newLiveDeviceProbeSessionBridge(runner *participants.ModelRunner, output *liveDeviceProbeMediaLink, sink audio.AudioSink) *liveDeviceProbeSessionBridge {
	return &liveDeviceProbeSessionBridge{
		runner:       runner,
		output:       output,
		sink:         sink,
		opened:       make(chan struct{}),
		responseDone: make(chan struct{}),
		done:         make(chan struct{}),
	}
}

func (b *liveDeviceProbeSessionBridge) Run(ctx context.Context) {
	defer close(b.done)
	for {
		message, ok := b.runner.DeltaOutbox.ReadBlockingContext(ctx)
		if !ok {
			if ctx.Err() == nil {
				b.setError(errors.New("session output ended before MESSAGE.END"))
			}
			b.finishResponse()
			return
		}
		switch message.Type {
		case messages.StreamTypeSessionOpen:
			b.openOnce.Do(func() { close(b.opened) })
		case messages.StreamTypeTranscriptDelta:
			value, ok := message.Value.(*messages.TranscriptDeltaValue)
			if !ok || value == nil {
				b.setError(fmt.Errorf("session transcript delta value = %T", message.Value))
				b.finishResponse()
				return
			}
			b.mu.Lock()
			b.transcript.WriteString(value.Text)
			b.mu.Unlock()
		case messages.StreamTypeTranscriptEnd:
			value, ok := message.Value.(*messages.TranscriptEndValue)
			if !ok || value == nil {
				b.setError(fmt.Errorf("session transcript end value = %T", message.Value))
				b.finishResponse()
				return
			}
			b.mu.Lock()
			b.fullTranscript = value.FullText
			b.mu.Unlock()
		case messages.StreamTypeAudioDelta:
			value, ok := message.Value.(*messages.AudioDeltaValue)
			if !ok || value == nil {
				b.setError(fmt.Errorf("session audio delta value = %T", message.Value))
				b.finishResponse()
				return
			}
			if err := b.writeAudioDelta(ctx, value); err != nil {
				b.setError(err)
				b.finishResponse()
				return
			}
		case messages.StreamTypeAudioEnd:
			if err := b.flushAudio(ctx, true); err != nil {
				b.setError(err)
				b.finishResponse()
				return
			}
		case messages.StreamTypeError:
			b.setError(liveDeviceProbeSessionError(message))
			b.finishResponse()
			return
		case messages.StreamTypeMessageEnd:
			if err := b.flushAudio(ctx, true); err != nil {
				b.setError(err)
			}
			b.finishResponse()
			return
		case messages.StreamTypeSessionClose:
			b.setError(errors.New("session closed before response completion"))
			b.finishResponse()
			return
		}
	}
}

func (b *liveDeviceProbeSessionBridge) writeAudioDelta(ctx context.Context, value *messages.AudioDeltaValue) error {
	if value.MediaType != "" && !strings.Contains(strings.ToLower(value.MediaType), "pcm") {
		return fmt.Errorf("session output audio format %q is not PCM16", value.MediaType)
	}
	if len(value.Content)%2 != 0 {
		return fmt.Errorf("session output audio has odd PCM16 byte length %d", len(value.Content))
	}
	samples := make([]int16, len(value.Content)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(value.Content[i*2:]))
	}
	b.mu.Lock()
	b.audioBuffer = append(b.audioBuffer, samples...)
	b.mu.Unlock()
	return b.flushAudio(ctx, false)
}

func (b *liveDeviceProbeSessionBridge) flushAudio(ctx context.Context, final bool) error {
	for {
		b.mu.Lock()
		if len(b.audioBuffer) == 0 || (!final && len(b.audioBuffer) < deviceProbeProviderFrameSamples) {
			b.mu.Unlock()
			break
		}
		frameLength := deviceProbeProviderFrameSamples
		if len(b.audioBuffer) < frameLength {
			frameLength = len(b.audioBuffer)
		}
		providerFrame := make([]int16, deviceProbeProviderFrameSamples)
		copy(providerFrame, b.audioBuffer[:frameLength])
		b.audioBuffer = b.audioBuffer[frameLength:]
		b.mu.Unlock()
		if err := b.writeOutputFrame(ctx, providerFrame); err != nil {
			return err
		}
	}
	if final {
		return b.flushSink(ctx, true)
	}
	return nil
}

func (b *liveDeviceProbeSessionBridge) writeOutputFrame(ctx context.Context, providerFrame []int16) error {
	outputFrame, err := wavio.Resample(providerFrame, deviceProbeProviderSampleRate, deviceProbeInputSampleRate)
	if err != nil {
		return fmt.Errorf("resample session output: %w", err)
	}
	if len(outputFrame) != deviceProbeInputFrameSamples {
		return fmt.Errorf("resampled session output has %d samples, want %d", len(outputFrame), deviceProbeInputFrameSamples)
	}
	emitted, err := b.output.RoundTrip(ctx, outputFrame)
	if err != nil {
		return fmt.Errorf("round-trip session output over WebRTC: %w", err)
	}
	b.mu.Lock()
	b.sinkBuffer = append(b.sinkBuffer, emitted...)
	b.mu.Unlock()
	return b.flushSink(ctx, false)
}

func (b *liveDeviceProbeSessionBridge) flushSink(ctx context.Context, final bool) error {
	for {
		b.mu.Lock()
		if len(b.sinkBuffer) == 0 || (!final && len(b.sinkBuffer) < audio.FrameSize) {
			b.mu.Unlock()
			return nil
		}
		frameLength := audio.FrameSize
		if len(b.sinkBuffer) < frameLength {
			frameLength = len(b.sinkBuffer)
		}
		frame := make([]int16, audio.FrameSize)
		copy(frame, b.sinkBuffer[:frameLength])
		b.sinkBuffer = b.sinkBuffer[frameLength:]
		b.mu.Unlock()
		if err := b.sink.WriteFrame(ctx, frame); err != nil {
			return fmt.Errorf("write session output to selected speaker: %w", err)
		}
		b.mu.Lock()
		b.outputSamples = append(b.outputSamples, frame...)
		b.outputFrames++
		b.mu.Unlock()
	}
}

func (b *liveDeviceProbeSessionBridge) waitOpened(ctx context.Context) error {
	select {
	case <-b.opened:
		return nil
	case <-b.responseDone:
		return b.errorValue(errors.New("session ended before SESSION.OPEN"))
	case <-b.done:
		return b.errorValue(errors.New("session ended before SESSION.OPEN"))
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *liveDeviceProbeSessionBridge) waitResponse(ctx context.Context) error {
	select {
	case <-b.responseDone:
		return b.errorValue(nil)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *liveDeviceProbeSessionBridge) finishResponse() {
	b.responseOnce.Do(func() { close(b.responseDone) })
}

func (b *liveDeviceProbeSessionBridge) setError(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.mu.Unlock()
}

func (b *liveDeviceProbeSessionBridge) errorValue(fallback error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	return fallback
}

func (b *liveDeviceProbeSessionBridge) snapshot() probe.ObservationSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	transcript := b.fullTranscript
	if strings.TrimSpace(transcript) == "" {
		transcript = b.transcript.String()
	}
	return probe.ObservationSnapshot{
		PCM16Samples:       append([]int16(nil), b.outputSamples...),
		Transcript:         transcript,
		FrameCount:         b.outputFrames,
		TerminalReason:     "message_end",
		TerminalProvenance: "provider",
		OutputState:        "complete",
	}
}

func liveDeviceProbeSessionError(message messages.StreamMessage) error {
	if value, ok := message.Value.(*messages.ErrorValue); ok && value != nil {
		return fmt.Errorf("realtime provider session error: %s", value.Message)
	}
	return fmt.Errorf("realtime provider session error: %T", message.Value)
}

func scenarioDeviceProbeTranscript(scenario probe.Scenario) string {
	for _, expectation := range scenario.Expectations {
		kind := expectation.Type
		if kind == "" {
			kind = expectation.Kind
		}
		if kind != probe.ExpectTranscriptContains {
			continue
		}
		if strings.TrimSpace(expectation.Text) != "" {
			return strings.TrimSpace(expectation.Text)
		}
		if strings.TrimSpace(expectation.Value) != "" {
			return strings.TrimSpace(expectation.Value)
		}
	}
	return "device round trip"
}

func deviceProbeInstructions(scenario probe.Scenario) string {
	return fmt.Sprintf("This is an automated audio device probe. Listen to the microphone and respond by saying exactly %q. Keep the response short.", scenarioDeviceProbeTranscript(scenario))
}

func captureLiveDeviceProbeInput(ctx context.Context, source *audio.DeviceSource, link *liveDeviceProbeMediaLink, runner *participants.ModelRunner) (int, float64, error) {
	pending := make([]int16, 0, audio.FrameSize*2)
	readFrame := make([]int16, audio.FrameSize)
	frameCount := 0
	var maxRMS float64
	for {
		if err := source.ReadFrame(ctx, readFrame); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == context.DeadlineExceeded {
				break
			}
			return frameCount, maxRMS, fmt.Errorf("read selected microphone: %w", err)
		}
		pending = append(pending, readFrame...)
		if rms := liveDeviceProbeRMS(readFrame); rms > maxRMS {
			maxRMS = rms
		}
		for len(pending) >= deviceProbeInputFrameSamples {
			inputFrame := append([]int16(nil), pending[:deviceProbeInputFrameSamples]...)
			pending = pending[deviceProbeInputFrameSamples:]
			trackFrame, err := link.RoundTrip(ctx, inputFrame)
			if err != nil {
				return frameCount, maxRMS, fmt.Errorf("round-trip microphone frame over WebRTC: %w", err)
			}
			providerFrame, err := wavio.Resample(trackFrame, deviceProbeInputSampleRate, deviceProbeProviderSampleRate)
			if err != nil {
				return frameCount, maxRMS, fmt.Errorf("resample microphone frame for session: %w", err)
			}
			pcm := liveDeviceProbePCMBytes(providerFrame)
			select {
			case runner.UserAudioInbox <- pcm:
				frameCount++
			case <-ctx.Done():
				return frameCount, maxRMS, ctx.Err()
			}
		}
	}
	return frameCount, maxRMS, nil
}

func liveDeviceProbeRMS(samples []int16) float64 {
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

func liveDeviceProbePCMBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

type liveDeviceProbeRTPWriter struct{ track *webrtc.TrackLocalStaticRTP }

func (w liveDeviceProbeRTPWriter) WriteRTP(ctx context.Context, packet *rtp.Packet) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return w.track.WriteRTP(packet)
	}
}

type liveDeviceProbeRTPPacketSource struct{ track *webrtc.TrackRemote }

func (s liveDeviceProbeRTPPacketSource) ReadRTP() (*rtp.Packet, error) {
	packet, _, err := s.track.ReadRTP()
	return packet, err
}

type liveDeviceProbePeerPair struct {
	sender      *webrtc.PeerConnection
	receiver    *webrtc.PeerConnection
	localTrack  *webrtc.TrackLocalStaticRTP
	remoteReady chan *webrtc.TrackRemote
	peerErrors  chan error
	errorOnce   sync.Once
}

func newLiveDeviceProbePeerPair() (*liveDeviceProbePeerPair, error) {
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
	pair := &liveDeviceProbePeerPair{
		sender:      sender,
		receiver:    receiver,
		remoteReady: make(chan *webrtc.TrackRemote, 1),
		peerErrors:  make(chan error, 1),
	}
	connected := make(chan struct{})
	var connectedOnce sync.Once
	cleanup := func(err error) (*liveDeviceProbePeerPair, error) {
		_ = pair.Close()
		return nil, err
	}
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			pair.errorOnce.Do(func() { pair.peerErrors <- fmt.Errorf("WebRTC sender connection state %s", state) })
		}
	})
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case pair.remoteReady <- track:
		default:
		}
	})
	localTrack, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, "audio", "s2s-v9-device")
	if err != nil {
		return cleanup(err)
	}
	pair.localTrack = localTrack
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
	if err := waitLiveDeviceProbeGathering(sender); err != nil {
		return cleanup(err)
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
	if err := waitLiveDeviceProbeGathering(receiver); err != nil {
		return cleanup(err)
	}
	if err := sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
		return cleanup(err)
	}
	select {
	case <-connected:
	case err := <-pair.peerErrors:
		return cleanup(err)
	case <-time.After(3 * time.Second):
		return cleanup(context.DeadlineExceeded)
	}
	return pair, nil
}

func waitLiveDeviceProbeGathering(peer *webrtc.PeerConnection) error {
	select {
	case <-webrtc.GatheringCompletePromise(peer):
		return nil
	case <-time.After(3 * time.Second):
		return context.DeadlineExceeded
	}
}

func (p *liveDeviceProbePeerPair) waitRemoteTrack(ctx context.Context) (*webrtc.TrackRemote, error) {
	select {
	case track := <-p.remoteReady:
		return track, nil
	case err := <-p.peerErrors:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *liveDeviceProbePeerPair) Close() error {
	if p == nil {
		return nil
	}
	return errors.Join(p.sender.Close(), p.receiver.Close())
}
