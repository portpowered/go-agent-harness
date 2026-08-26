package cli

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// TestS2SV9WebRTCDeviceCaptureProvesRegistryToRemoteTrack exercises the
// device-tier capture boundary with the shared registry and the real RTC Opus
// track. The virtual registry is the deterministic stand-in for a hardware
// host: its input/output pair preserves the same DeviceSource/DeviceSink
// ownership and frame contracts while keeping ordinary CI network-free.
func TestS2SV9WebRTCDeviceCaptureProvesRegistryToRemoteTrack(t *testing.T) {
	registry, err := audio.NewVirtualRegistry(audio.DefaultVirtualBackendConfig())
	if err != nil {
		t.Fatalf("create device registry: %v", err)
	}
	availability, err := audio.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("probe device availability: %v", err)
	}
	if availability.Status != audio.DeviceProbeStatusReady {
		t.Fatalf("virtual device probe status = %s, want ready (reason=%s)", availability.Status, availability.Reason)
	}
	input, err := registry.Default(audio.DirectionInput)
	if err != nil {
		t.Fatalf("select default input device: %v", err)
	}
	output, err := registry.Default(audio.DirectionOutput)
	if err != nil {
		t.Fatalf("select default output device: %v", err)
	}

	source, err := audio.NewDeviceSource(registry, input.ID)
	if err != nil {
		t.Fatalf("open selected input %q: %v", input.ID, err)
	}
	defer func() { _ = source.Close() }()
	sink, err := audio.NewDeviceSink(registry, output.ID)
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

	encoder, err := rtc.NewRTCOpusEncoder()
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
	if got := pcm16ProbeRMS(gotInput); got <= 300 {
		t.Fatalf("captured input RMS = %.2f, want voiced energy above 300", got)
	}

	// The device contract emits 30 ms frames at 16 kHz, while one RTC Opus
	// packet is 20 ms at 48 kHz. Feed one complete Opus-sized prefix from the
	// captured device frame and leave packetization to the outbound track.
	rtcInput := gotInput[:audio.SampleRate*rtc.OpusFrameSamples/rtc.OpusSampleRate]
	if err := track.WriteFrame(context.Background(), rtc.PCMFrame{Samples: rtcInput}); err != nil {
		t.Fatalf("commit captured frame to WebRTC track: %v", err)
	}

	remote, err := peers.readRemoteTrack()
	if err != nil {
		t.Fatalf("receive remote WebRTC track: %v", err)
	}
	decoder, err := rtc.NewRTCOpusDecoder()
	if err != nil {
		t.Fatalf("create RTC Opus decoder: %v", err)
	}
	defer func() { _ = decoder.Close() }()
	decoded, err := decoder.Decode(remote.Payload)
	if err != nil {
		t.Fatalf("decode remote WebRTC packet: %v", err)
	}
	if got := pcm16ProbeRMS(decoded); got <= 300 {
		t.Fatalf("remote decoded PCM RMS = %.2f, want voiced energy above 300", got)
	}
	if peers.remoteTrack == nil || peers.remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
		t.Fatalf("remote track = %#v, want an audio track", peers.remoteTrack)
	}

	observations := registry.Observations()
	if observations.OpenCount != 2 || observations.ReleaseCount != 0 {
		t.Fatalf("registry observations before cleanup = %+v, want two opens and live handles", observations)
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

func (p *deviceProbePeerPair) readRemoteTrack() (*rtp.Packet, error) {
	select {
	case p.remoteTrack = <-p.remoteReady:
	case <-time.After(3 * time.Second):
		return nil, context.DeadlineExceeded
	}
	p.remoteTrack.SetReadDeadline(time.Now().Add(3 * time.Second))
	packet, _, err := p.remoteTrack.ReadRTP()
	return packet, err
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
