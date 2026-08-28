package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

const (
	productionRTCSignalingTimeout = 2 * time.Second
	productionRTCConnectTimeout   = 5 * time.Second
	productionRTCDataChannelLabel = "agent-cli-session"
	productionRTCMediaTrackID     = "agent-cli-inbound-audio"
)

// productionRTCComposition owns the concrete protocol implementations used
// by the generated CLI graph. The service only receives the narrow component
// functions, so provider packages never need to import Pion types.
type productionRTCComposition struct {
	mu        sync.Mutex
	answerers map[*rtc.LoopbackEndpoint]*rtc.LoopbackEndpoint
}

func defaultSessionRTCComponents() services.SessionRTCComponents {
	return newProductionRTCComposition().components()
}

func newProductionRTCComposition() *productionRTCComposition {
	return &productionRTCComposition{
		answerers: make(map[*rtc.LoopbackEndpoint]*rtc.LoopbackEndpoint),
	}
}

func (c *productionRTCComposition) components() services.SessionRTCComponents {
	return services.SessionRTCComponents{
		ResolveSignaling: c.resolveSignaling,
		NewDataPlane:     c.newDataPlane,
		OpenMediaSource:  openProductionRTCMediaSource,
	}
}

// resolveSignaling supports the in-process loopback signaling endpoint used by
// the shipped hermetic path. Other endpoint schemes fail as a typed signaling
// error until an application-specific resolver is supplied through
// WithSessionRTCComponents.
func (c *productionRTCComposition) resolveSignaling(ctx context.Context, raw string) (rtc.Signaling, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	endpoint := strings.TrimSpace(raw)
	if !isLoopbackRTCSignalingEndpoint(endpoint) {
		return nil, fmt.Errorf("%w: production RTC signaling endpoint is unavailable", rtc.ErrSignalingUnreachable)
	}
	offerer, answerer, err := rtc.NewLoopbackSignalingPair(rtc.SignalingConfig{
		ICEGatheringTimeout: productionRTCSignalingTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create RTC signaling pair: %w", err)
	}
	c.mu.Lock()
	c.answerers[offerer] = answerer
	c.mu.Unlock()
	return offerer, nil
}

func isLoopbackRTCSignalingEndpoint(raw string) bool {
	if strings.EqualFold(raw, "loopback") {
		return true
	}
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Scheme, "loopback")
}

func (c *productionRTCComposition) newDataPlane(ctx context.Context, signaling rtc.Signaling) (services.SessionRTCDataPlane, error) {
	offerer, ok := signaling.(*rtc.LoopbackEndpoint)
	if !ok {
		return nil, fmt.Errorf("%w: production RTC data plane requires loopback signaling", rtc.ErrSignalingUnreachable)
	}
	c.mu.Lock()
	answerer := c.answerers[offerer]
	delete(c.answerers, offerer)
	c.mu.Unlock()
	if answerer == nil {
		return nil, fmt.Errorf("%w: RTC signaling endpoint was not created by the production resolver", rtc.ErrSignalingUnreachable)
	}
	dataPlane, err := newProductionRTCDataPlane(ctx, offerer, answerer)
	if err != nil {
		_ = answerer.Close()
		return nil, err
	}
	return dataPlane, nil
}

func openProductionRTCMediaSource(ctx context.Context, raw string) (rtc.InboundMedia, error) {
	stream, err := rtc.OpenMediaSource(ctx, raw)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

type productionRTCDataPlane struct {
	offerer  *rtc.LoopbackEndpoint
	answerer *rtc.LoopbackEndpoint

	clientPeer *webrtc.PeerConnection
	serverPeer *webrtc.PeerConnection
	data       *productionRTCConn

	clientConnected     chan struct{}
	serverConnected     chan struct{}
	serverDataSeen      chan struct{}
	serverDataOpen      chan struct{}
	clientDataOpen      chan struct{}
	connectedOnce       sync.Once
	serverConnectedOnce sync.Once
	serverDataSeenOnce  sync.Once
	serverDataOpenOnce  sync.Once
	clientDataOpenOnce  sync.Once

	failureMu   sync.Mutex
	failure     error
	failureDone chan struct{}
	failureOnce sync.Once
	closed      atomic.Bool

	attachMu     sync.Mutex
	attached     bool
	media        *rtc.OutboundTrack
	mediaCancel  context.CancelFunc
	mediaDone    chan struct{}
	mediaErrMu   sync.Mutex
	mediaErr     error
	mediaFrames  atomic.Uint64
	mediaPackets atomic.Uint64

	closeOnce sync.Once
	closeErr  error
}

var _ services.SessionRTCDataPlane = (*productionRTCDataPlane)(nil)

func newProductionRTCDataPlane(ctx context.Context, offerer, answerer *rtc.LoopbackEndpoint) (*productionRTCDataPlane, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
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
		return nil, fmt.Errorf("register RTC Opus codec: %w", err)
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(
		webrtc.WithSettingEngine(settings),
		webrtc.WithMediaEngine(mediaEngine),
	)
	clientPeer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("create RTC client peer: %w", err)
	}
	serverPeer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = clientPeer.Close()
		return nil, fmt.Errorf("create RTC server peer: %w", err)
	}

	dataPlane := &productionRTCDataPlane{
		offerer:         offerer,
		answerer:        answerer,
		clientPeer:      clientPeer,
		serverPeer:      serverPeer,
		clientConnected: make(chan struct{}),
		serverConnected: make(chan struct{}),
		serverDataSeen:  make(chan struct{}),
		serverDataOpen:  make(chan struct{}),
		clientDataOpen:  make(chan struct{}),
		failureDone:     make(chan struct{}),
	}
	cleanup := func(err error) (*productionRTCDataPlane, error) {
		_ = dataPlane.Close()
		return nil, err
	}
	clientPeer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			dataPlane.connectedOnce.Do(func() { close(dataPlane.clientConnected) })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			dataPlane.fail(fmt.Errorf("RTC client peer reached %s", state))
		}
	})
	serverPeer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			dataPlane.serverConnectedOnce.Do(func() { close(dataPlane.serverConnected) })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			dataPlane.fail(fmt.Errorf("RTC server peer reached %s", state))
		}
	})
	serverPeer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		go func() {
			for {
				if _, _, err := track.ReadRTP(); err != nil {
					return
				}
				dataPlane.mediaPackets.Add(1)
			}
		}()
	})
	serverPeer.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != productionRTCDataChannelLabel {
			dataPlane.fail(fmt.Errorf("RTC server received unexpected data channel %q", channel.Label()))
			return
		}
		dataPlane.serverDataSeenOnce.Do(func() { close(dataPlane.serverDataSeen) })
		channel.OnOpen(func() {
			dataPlane.serverDataOpenOnce.Do(func() { close(dataPlane.serverDataOpen) })
		})
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			var err error
			if message.IsString {
				err = channel.SendText(string(message.Data))
			} else {
				err = channel.Send(message.Data)
			}
			if err != nil {
				dataPlane.fail(fmt.Errorf("RTC server data channel echo: %w", err))
			}
		})
		channel.OnError(func(err error) {
			dataPlane.fail(fmt.Errorf("RTC server data channel: %w", err))
		})
		channel.OnClose(func() {
			if !dataPlane.closed.Load() {
				dataPlane.fail(errors.New("RTC server data channel closed"))
			}
		})
	})
	clientDataChannel, err := clientPeer.CreateDataChannel(productionRTCDataChannelLabel, nil)
	if err != nil {
		return cleanup(fmt.Errorf("create RTC data channel: %w", err))
	}
	dataPlane.data = newProductionRTCConn(clientDataChannel, dataPlane)
	clientDataChannel.OnOpen(func() {
		dataPlane.clientDataOpenOnce.Do(func() { close(dataPlane.clientDataOpen) })
	})
	clientDataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		dataPlane.data.push(message)
	})
	clientDataChannel.OnError(func(err error) {
		dataPlane.fail(fmt.Errorf("RTC client data channel: %w", err))
	})
	clientDataChannel.OnClose(func() {
		if !dataPlane.closed.Load() {
			dataPlane.fail(errors.New("RTC client data channel closed"))
		}
	})
	return dataPlane, nil
}

func (p *productionRTCDataPlane) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if p == nil || p.data == nil {
		return nil, services.ErrSessionRTCDataPlaneUnavailable
	}
	if p.closed.Load() {
		return nil, io.ErrClosedPipe
	}
	return p.data, nil
}

func (p *productionRTCDataPlane) AttachInboundMedia(ctx context.Context, source rtc.InboundMedia) error {
	if p == nil {
		return services.ErrSessionRTCDataPlaneUnavailable
	}
	if source == nil {
		return errors.New("RTC inbound media source is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.attachMu.Lock()
	defer p.attachMu.Unlock()
	if p.attached {
		return errors.New("RTC inbound media is already attached")
	}
	if p.closed.Load() {
		return io.ErrClosedPipe
	}
	codec := webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeOpus,
		ClockRate:   rtc.OutboundRTPClockRate,
		Channels:    1,
		SDPFmtpLine: "minptime=10;useinbandfec=1",
	}
	localTrack, err := webrtc.NewTrackLocalStaticRTP(codec, productionRTCMediaTrackID, "agent-cli")
	if err != nil {
		return fmt.Errorf("create RTC inbound media track: %w", err)
	}
	if _, err := p.clientPeer.AddTrack(localTrack); err != nil {
		return fmt.Errorf("attach RTC inbound media track: %w", err)
	}
	connectCtx, cancel := context.WithTimeout(ctx, productionRTCConnectTimeout)
	defer cancel()
	if err := p.negotiate(connectCtx); err != nil {
		return err
	}
	encoder, err := rtc.NewRTCOpusEncoder()
	if err != nil {
		return fmt.Errorf("create RTC inbound Opus encoder: %w", err)
	}
	outbound, err := rtc.NewOutboundTrack(rtc.OutboundTrackConfig{
		SourceRate: rtc.OpusSampleRate,
		Encoder:    encoder,
		Writer: rtc.RTPWriterFunc(func(writeCtx context.Context, packet *rtp.Packet) error {
			select {
			case <-writeCtx.Done():
				return writeCtx.Err()
			default:
			}
			return localTrack.WriteRTP(packet)
		}),
	})
	if err != nil {
		_ = encoder.Close()
		return fmt.Errorf("create RTC outbound media track: %w", err)
	}
	mediaCtx, mediaCancel := context.WithCancel(ctx)
	p.media = outbound
	p.mediaCancel = mediaCancel
	p.mediaDone = make(chan struct{})
	p.attached = true
	go p.pumpInboundMedia(mediaCtx, source, outbound)
	return nil
}

func (p *productionRTCDataPlane) negotiate(ctx context.Context) error {
	offer, err := p.clientPeer.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create RTC offer: %w", err)
	}
	offer, err = setProductionRTCLocalAndGather(ctx, p.clientPeer, offer)
	if err != nil {
		return fmt.Errorf("gather RTC offer: %w", err)
	}
	if err := p.offerer.SendOffer(ctx, rtc.SessionDescription{Type: offer.Type.String(), SDP: offer.SDP}); err != nil {
		return fmt.Errorf("send RTC offer: %w", err)
	}
	receivedOffer, err := p.answerer.ReceiveOffer(ctx)
	if err != nil {
		return fmt.Errorf("receive RTC offer: %w", err)
	}
	if err := p.serverPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: receivedOffer.SDP}); err != nil {
		return fmt.Errorf("set RTC server remote offer: %w", err)
	}
	if err := p.offerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "pion-sdp-candidate"}); err != nil {
		return fmt.Errorf("send RTC offer candidate: %w", err)
	}
	if err := p.offerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete RTC offer gathering: %w", err)
	}
	if _, err := p.answerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive RTC offer candidate: %w", err)
	}
	answer, err := p.serverPeer.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create RTC answer: %w", err)
	}
	answer, err = setProductionRTCLocalAndGather(ctx, p.serverPeer, answer)
	if err != nil {
		return fmt.Errorf("gather RTC answer: %w", err)
	}
	if err := p.answerer.SendAnswer(ctx, rtc.SessionDescription{Type: answer.Type.String(), SDP: answer.SDP}); err != nil {
		return fmt.Errorf("send RTC answer: %w", err)
	}
	receivedAnswer, err := p.offerer.ReceiveAnswer(ctx)
	if err != nil {
		return fmt.Errorf("receive RTC answer: %w", err)
	}
	if err := p.clientPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: receivedAnswer.SDP}); err != nil {
		return fmt.Errorf("set RTC client remote answer: %w", err)
	}
	if err := p.answerer.SendCandidate(ctx, rtc.ICECandidate{Candidate: "pion-sdp-candidate"}); err != nil {
		return fmt.Errorf("send RTC answer candidate: %w", err)
	}
	if err := p.answerer.CompleteCandidateGathering(ctx); err != nil {
		return fmt.Errorf("complete RTC answer gathering: %w", err)
	}
	if _, err := p.offerer.ReceiveCandidate(ctx); err != nil {
		return fmt.Errorf("receive RTC answer candidate: %w", err)
	}
	if err := p.answerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait for RTC answer gathering: %w", err)
	}
	if err := p.offerer.WaitCandidateGathering(ctx); err != nil {
		return fmt.Errorf("wait for RTC offer gathering: %w", err)
	}
	for _, ready := range []struct {
		name string
		ch   <-chan struct{}
	}{
		{name: "RTC client peer", ch: p.clientConnected},
		{name: "RTC server peer", ch: p.serverConnected},
		{name: "RTC server data channel", ch: p.serverDataSeen},
		{name: "RTC server data channel open", ch: p.serverDataOpen},
		{name: "RTC client data channel open", ch: p.clientDataOpen},
	} {
		if err := p.waitReady(ctx, ready.ch, ready.name); err != nil {
			return err
		}
	}
	return nil
}

func setProductionRTCLocalAndGather(ctx context.Context, peer *webrtc.PeerConnection, description webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := peer.SetLocalDescription(description); err != nil {
		return webrtc.SessionDescription{}, err
	}
	select {
	case <-webrtc.GatheringCompletePromise(peer):
	case <-ctx.Done():
		return webrtc.SessionDescription{}, ctx.Err()
	}
	local := peer.LocalDescription()
	if local == nil {
		return webrtc.SessionDescription{}, errors.New("RTC peer has no local description after gathering")
	}
	return *local, nil
}

func (p *productionRTCDataPlane) waitReady(ctx context.Context, ready <-chan struct{}, name string) error {
	select {
	case <-ready:
		return nil
	case <-p.failureDone:
		return fmt.Errorf("wait for %s: %w", name, p.failureValue())
	case <-ctx.Done():
		return fmt.Errorf("wait for %s: %w", name, ctx.Err())
	}
}

func (p *productionRTCDataPlane) fail(err error) {
	if err == nil || p.closed.Load() {
		return
	}
	p.failureOnce.Do(func() {
		p.failureMu.Lock()
		p.failure = err
		p.failureMu.Unlock()
		close(p.failureDone)
	})
}

func (p *productionRTCDataPlane) failureValue() error {
	p.failureMu.Lock()
	defer p.failureMu.Unlock()
	if p.failure != nil {
		return p.failure
	}
	return errors.New("RTC data plane failed")
}

func (p *productionRTCDataPlane) pumpInboundMedia(ctx context.Context, source rtc.InboundMedia, outbound *rtc.OutboundTrack) {
	defer close(p.mediaDone)
	pending := make([]int16, 0, rtc.OpusFrameSamples)
	write := func(samples []int16) error {
		if len(samples) == 0 {
			return nil
		}
		frame := make([]int16, rtc.OpusFrameSamples)
		copy(frame, samples)
		if err := outbound.WriteFrame(ctx, rtc.PCMFrame{Samples: frame}); err != nil {
			return err
		}
		p.mediaFrames.Add(1)
		return nil
	}
	for {
		frame, err := source.ReadFrame(ctx)
		if len(frame.Samples) > 0 {
			pending = append(pending, frame.Samples...)
			for len(pending) >= rtc.OpusFrameSamples {
				if writeErr := write(pending[:rtc.OpusFrameSamples]); writeErr != nil {
					p.recordMediaError(writeErr)
					return
				}
				pending = pending[rtc.OpusFrameSamples:]
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				p.recordMediaError(err)
				return
			}
			if len(pending) > 0 && ctx.Err() == nil {
				if writeErr := write(pending); writeErr != nil {
					p.recordMediaError(writeErr)
				}
			}
			return
		}
	}
}

func (p *productionRTCDataPlane) recordMediaError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	p.mediaErrMu.Lock()
	if p.mediaErr == nil {
		p.mediaErr = err
	}
	p.mediaErrMu.Unlock()
	p.fail(fmt.Errorf("RTC inbound media: %w", err))
}

func (p *productionRTCDataPlane) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.attachMu.Lock()
		if p.mediaCancel != nil {
			p.mediaCancel()
		}
		mediaDone := p.mediaDone
		media := p.media
		p.attachMu.Unlock()
		if mediaDone != nil {
			<-mediaDone
		}
		var errs []error
		if media != nil {
			errs = append(errs, media.Close())
		}
		if p.data != nil {
			errs = append(errs, p.data.Close())
		}
		if p.clientPeer != nil {
			errs = append(errs, p.clientPeer.Close())
		}
		if p.serverPeer != nil {
			errs = append(errs, p.serverPeer.Close())
		}
		if p.answerer != nil {
			errs = append(errs, p.answerer.Close())
		}
		p.mediaErrMu.Lock()
		if p.mediaErr != nil {
			errs = append(errs, p.mediaErr)
		}
		p.mediaErrMu.Unlock()
		p.closeErr = errors.Join(errs...)
	})
	return p.closeErr
}

type productionRTCMessage struct {
	messageType int
	payload     []byte
}

type productionRTCConn struct {
	channel *webrtc.DataChannel
	owner   *productionRTCDataPlane
	inbound chan productionRTCMessage
	done    chan struct{}

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

var _ transport.Conn = (*productionRTCConn)(nil)

func newProductionRTCConn(channel *webrtc.DataChannel, owner *productionRTCDataPlane) *productionRTCConn {
	return &productionRTCConn{
		channel: channel,
		owner:   owner,
		inbound: make(chan productionRTCMessage, 128),
		done:    make(chan struct{}),
	}
}

func (c *productionRTCConn) push(message webrtc.DataChannelMessage) {
	messageType := 2
	if message.IsString {
		messageType = 1
	}
	select {
	case c.inbound <- productionRTCMessage{messageType: messageType, payload: append([]byte(nil), message.Data...)}:
	case <-c.done:
	case <-c.owner.failureDone:
	}
}

func (c *productionRTCConn) ReadMessage() (int, []byte, error) {
	select {
	case message := <-c.inbound:
		return message.messageType, message.payload, nil
	case <-c.done:
		return 0, nil, io.EOF
	case <-c.owner.failureDone:
		return 0, nil, fmt.Errorf("RTC data channel: %w", c.owner.failureValue())
	}
}

func (c *productionRTCConn) WriteMessage(messageType int, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	if messageType != 1 && messageType != 2 {
		return fmt.Errorf("RTC data channel does not support message type %d", messageType)
	}
	var err error
	if messageType == 1 {
		err = c.channel.SendText(string(payload))
	} else {
		err = c.channel.Send(payload)
	}
	if err != nil {
		wrapped := fmt.Errorf("RTC data channel write: %w", err)
		c.owner.fail(wrapped)
		return wrapped
	}
	return nil
}

func (c *productionRTCConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.done)
		if c.channel != nil {
			c.closeErr = c.channel.Close()
		}
	})
	return c.closeErr
}
