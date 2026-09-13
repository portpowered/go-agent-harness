package deviceprobe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const (
	deviceProbePeerConnectTimeout = 3 * time.Second
	deviceProbePayloadType        = 111
)

type liveDeviceProbeMediaLink struct {
	peers    *liveDeviceProbePeerPair
	outbound *rtc.OutboundTrack
	inbound  *rtc.InboundTrack
	decoder  *codec.OpusDecoder
}

func newLiveDeviceProbeMediaLink() (*liveDeviceProbeMediaLink, error) {
	peers, err := newLiveDeviceProbePeerPair()
	if err != nil {
		return nil, err
	}
	encoder, err := codec.NewOpusEncoder()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create RTC Opus encoder: %w", err), peers.Close())
	}
	outbound, err := rtc.NewOutboundTrack(rtc.OutboundTrackConfig{
		SourceRate: deviceProbeInputSampleRate,
		Encoder:    encoder,
		Writer:     liveDeviceProbeRTPWriter{track: peers.localTrack},
		Pacer:      rtc.PacerFunc(func(context.Context, uint64) error { return nil }),
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create outbound RTC track: %w", err), peers.Close(), encoder.Close())
	}
	return &liveDeviceProbeMediaLink{peers: peers, outbound: outbound}, nil
}

func (l *liveDeviceProbeMediaLink) RoundTrip(ctx context.Context, samples []int16) ([]int16, error) {
	if len(samples) != deviceProbeInputFrameSamples {
		return nil, fmt.Errorf("RTC media frame has %d samples, want %d", len(samples), deviceProbeInputFrameSamples)
	}
	if err := l.outbound.WriteFrame(ctx, audio.PCMFrame{Samples: samples}); err != nil {
		return nil, err
	}
	if l.inbound == nil {
		remote, err := l.peers.waitRemoteTrack(ctx)
		if err != nil {
			return nil, fmt.Errorf("receive negotiated remote RTC track: %w", err)
		}
		l.decoder, err = codec.NewOpusDecoder()
		if err != nil {
			return nil, fmt.Errorf("create RTC Opus decoder: %w", err)
		}
		l.inbound, err = rtc.NewInboundTrack(liveDeviceProbeRTPPacketSource{track: remote}, l.decoder, rtc.InboundTrackConfig{
			SampleRate:    deviceProbeInputSampleRate,
			FrameDuration: deviceProbeFrameDuration,
			JitterDepth:   deviceProbeFrameDuration,
		})
		if err != nil {
			closeErr := l.decoder.Close()
			l.decoder = nil
			return nil, errors.Join(fmt.Errorf("bind negotiated remote RTC track: %w", err), closeErr)
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
	codec := liveDeviceProbeCodec()
	if err := mediaEngine.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	sender, receiver, err := newDeviceProbePeerConnections(mediaEngine)
	if err != nil {
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
	configureDeviceProbePeerCallbacks(pair, connected, &connectedOnce)
	localTrack, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, "audio", "s2s-v9-device")
	if err != nil {
		return closeDeviceProbePeerPair(pair, err)
	}
	pair.localTrack = localTrack
	if err := negotiateDeviceProbePeers(pair); err != nil {
		return closeDeviceProbePeerPair(pair, err)
	}
	select {
	case <-connected:
		return pair, nil
	case err := <-pair.peerErrors:
		return closeDeviceProbePeerPair(pair, err)
	case <-time.After(deviceProbePeerConnectTimeout):
		return closeDeviceProbePeerPair(pair, context.DeadlineExceeded)
	}
}

func liveDeviceProbeCodec() webrtc.RTPCodecParameters {
	return webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus, ClockRate: rtc.OutboundRTPClockRate,
			Channels: 1, SDPFmtpLine: "minptime=10;useinbandfec=1",
		}, PayloadType: deviceProbePayloadType,
	}
}

func newDeviceProbePeerConnections(mediaEngine *webrtc.MediaEngine) (*webrtc.PeerConnection, *webrtc.PeerConnection, error) {
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	sender, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, nil, err
	}
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, nil, errors.Join(err, sender.Close())
	}
	return sender, receiver, nil
}

func configureDeviceProbePeerCallbacks(pair *liveDeviceProbePeerPair, connected chan struct{}, connectedOnce *sync.Once) {
	pair.sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			pair.errorOnce.Do(func() { pair.peerErrors <- fmt.Errorf("WebRTC sender connection state %s", state) })
		}
	})
	pair.receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		select {
		case pair.remoteReady <- track:
		default:
		}
	})
}

func negotiateDeviceProbePeers(pair *liveDeviceProbePeerPair) error {
	if _, err := pair.sender.AddTrack(pair.localTrack); err != nil {
		return err
	}
	offer, err := pair.sender.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := pair.sender.SetLocalDescription(offer); err != nil {
		return err
	}
	if err := waitLiveDeviceProbeGathering(pair.sender); err != nil {
		return err
	}
	if err := pair.receiver.SetRemoteDescription(*pair.sender.LocalDescription()); err != nil {
		return err
	}
	answer, err := pair.receiver.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := pair.receiver.SetLocalDescription(answer); err != nil {
		return err
	}
	if err := waitLiveDeviceProbeGathering(pair.receiver); err != nil {
		return err
	}
	return pair.sender.SetRemoteDescription(*pair.receiver.LocalDescription())
}

func closeDeviceProbePeerPair(pair *liveDeviceProbePeerPair, err error) (*liveDeviceProbePeerPair, error) {
	return nil, errors.Join(err, pair.Close())
}

func waitLiveDeviceProbeGathering(peer *webrtc.PeerConnection) error {
	select {
	case <-webrtc.GatheringCompletePromise(peer):
		return nil
	case <-time.After(deviceProbePeerConnectTimeout):
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
