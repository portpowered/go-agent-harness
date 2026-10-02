package codexrtc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// Media constants. The peer exchanges 48 kHz mono PCM16 in 20 ms frames;
// Opus is negotiated as opus/48000/2 (RFC 7587) on payload type 111, the
// type OpenClaw's peer offers.
const (
	SampleRate        = codec.OpusSampleRate
	FrameSamples      = codec.OpusFrameSamples
	FrameDuration     = codec.OpusFrameDuration
	OpusPayloadType   = 111
	opusSDPChannels   = 2
	opusFmtp          = "minptime=10;useinbandfec=1"
	inboundQueueDepth = 50 // one second of frames
	// MaxConcealedFrames bounds the frames synthesized for one sequence gap:
	// 100 ms. A longer gap is a stall, not packet loss; concealing all of it
	// would replay stale history and delay live audio.
	MaxConcealedFrames = 5
)

// Peer errors.
var (
	// ErrPeerClosed reports use of a peer after Close.
	ErrPeerClosed = errors.New("codexrtc: peer is closed")
	// ErrPeerFailed reports a connection that reached the failed state.
	ErrPeerFailed = errors.New("codexrtc: peer connection failed")
)

// PeerConfig configures a Peer. The zero value is the production setup.
type PeerConfig struct {
	// SettingEngine overrides pion's network settings, for example a
	// virtual network in tests.
	SettingEngine *webrtc.SettingEngine
	// ICEServers are STUN or TURN servers; none are needed when the remote
	// side is publicly reachable.
	ICEServers []webrtc.ICEServer
	// Opus overrides the encoder settings; zero fields take go-audio's
	// defaults.
	Opus codec.OpusCodecConfig
}

// Peer is one WebRTC connection with a single send-and-receive Opus audio
// track. WriteFrame and ReadFrame each have one caller at a time.
type Peer struct {
	pc      *webrtc.PeerConnection
	track   *webrtc.TrackLocalStaticSample
	encoder *codec.OpusEncoder
	decoder *codec.OpusDecoder

	inbound   chan []int16
	connected chan struct{}
	failed    chan struct{}
	done      chan struct{}

	stateOnce  sync.Once
	failOnce   sync.Once
	closeOnce  sync.Once
	readerDone sync.WaitGroup
	closeErr   error

	mu        sync.Mutex // guards closed and trackRead, ordering reader starts before Close waits
	closed    bool
	trackRead bool

	// seq tracks the inbound RTP sequence for loss concealment; only the
	// track reader touches it. concealed and late count its decisions.
	seq       sequenceTracker
	concealed atomic.Int64
	late      atomic.Int64
}

func opusCapability() webrtc.RTPCodecCapability {
	return webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: SampleRate, Channels: opusSDPChannels, SDPFmtpLine: opusFmtp,
	}
}

// NewPeer builds the connection, its Opus codecs and its audio transceiver.
func NewPeer(config PeerConfig) (*Peer, error) {
	encoder, err := codec.NewOpusEncoder(config.Opus)
	if err != nil {
		return nil, fmt.Errorf("codexrtc: opus encoder: %w", err)
	}
	decoder, err := codec.NewOpusDecoder()
	if err != nil {
		return nil, fmt.Errorf("codexrtc: opus decoder: %w", err)
	}
	pc, err := newPeerConnection(config)
	if err != nil {
		return nil, err
	}
	track, err := webrtc.NewTrackLocalStaticSample(opusCapability(), "audio", "codexrtc")
	if err != nil {
		return nil, closeAfter(pc, fmt.Errorf("codexrtc: audio track: %w", err))
	}
	transceiver, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv})
	if err != nil {
		return nil, closeAfter(pc, fmt.Errorf("codexrtc: audio transceiver: %w", err))
	}
	peer := &Peer{
		pc: pc, track: track, encoder: encoder, decoder: decoder,
		inbound:   make(chan []int16, inboundQueueDepth),
		connected: make(chan struct{}),
		failed:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	peer.readerDone.Add(1)
	go peer.drainRTCP(transceiver.Sender())
	pc.OnConnectionStateChange(peer.onState)
	pc.OnTrack(peer.onTrack)
	return peer, nil
}

func newPeerConnection(config PeerConfig) (*webrtc.PeerConnection, error) {
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: opusCapability(), PayloadType: OpusPayloadType,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("codexrtc: register opus: %w", err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
		return nil, fmt.Errorf("codexrtc: interceptors: %w", err)
	}
	options := []func(*webrtc.API){webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry)}
	if config.SettingEngine != nil {
		options = append(options, webrtc.WithSettingEngine(*config.SettingEngine))
	}
	pc, err := webrtc.NewAPI(options...).NewPeerConnection(webrtc.Configuration{ICEServers: config.ICEServers})
	if err != nil {
		return nil, fmt.Errorf("codexrtc: peer connection: %w", err)
	}
	return pc, nil
}

func closeAfter(pc *webrtc.PeerConnection, err error) error {
	return errors.Join(err, pc.Close())
}

// CreateOffer sets and returns the local offer after ICE gathering
// completes, so the single SDP sent at call creation carries every
// candidate.
func (p *Peer) CreateOffer(ctx context.Context) (string, error) {
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("codexrtc: create offer: %w", err)
	}
	return p.setLocal(ctx, offer)
}

// Answer applies a remote offer and returns the gathered local answer. It is
// the backend's side of the exchange, used by in-process fakes.
func (p *Peer) Answer(ctx context.Context, offer string) (string, error) {
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", fmt.Errorf("codexrtc: apply offer: %w", err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("codexrtc: create answer: %w", err)
	}
	return p.setLocal(ctx, answer)
}

func (p *Peer) setLocal(ctx context.Context, description webrtc.SessionDescription) (string, error) {
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(description); err != nil {
		return "", fmt.Errorf("codexrtc: set local %s: %w", description.Type, err)
	}
	select {
	case <-gathered:
	case <-p.done:
		return "", ErrPeerClosed
	case <-ctx.Done():
		return "", fmt.Errorf("codexrtc: ICE gathering: %w", context.Cause(ctx))
	}
	return p.pc.LocalDescription().SDP, nil
}

// ApplyAnswer sets the remote answer from call creation.
func (p *Peer) ApplyAnswer(answer string) error {
	if !strings.Contains(answer, "m=audio") {
		return fmt.Errorf("%w: the SDP answer has no audio section", ErrBadCallResponse)
	}
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return fmt.Errorf("codexrtc: apply answer: %w", err)
	}
	return nil
}

// WaitConnected blocks until the connection is up, fails, is closed, or ctx
// ends.
func (p *Peer) WaitConnected(ctx context.Context) error {
	select {
	case <-p.connected:
		return nil
	case <-p.failed:
		return ErrPeerFailed
	case <-p.done:
		return ErrPeerClosed
	case <-ctx.Done():
		return fmt.Errorf("codexrtc: wait for connection: %w", context.Cause(ctx))
	}
}

// Failed is closed when the connection reaches the failed state, for
// example when ICE consent is lost after the call was up.
func (p *Peer) Failed() <-chan struct{} { return p.failed }

// WriteFrame encodes one 20 ms frame of 48 kHz mono PCM16 (FrameSamples
// samples) and sends it. The caller paces the frames.
func (p *Peer) WriteFrame(ctx context.Context, samples []int16) error {
	select {
	case <-p.done:
		return ErrPeerClosed
	default:
	}
	packet, err := p.encoder.Encode(ctx, samples)
	if err != nil {
		return fmt.Errorf("codexrtc: encode frame: %w", err)
	}
	if err := p.track.WriteSample(media.Sample{Data: packet, Duration: FrameDuration}); err != nil {
		return fmt.Errorf("codexrtc: send frame: %w", err)
	}
	return nil
}

// ReadFrame returns the next decoded 20 ms frame of remote audio as 48 kHz
// mono PCM16. Undecodable packets are skipped. Frames are queued for one
// second; a slower reader makes the RTP reader wait.
func (p *Peer) ReadFrame(ctx context.Context) ([]int16, error) {
	select {
	case frame := <-p.inbound:
		return frame, nil
	case <-p.done:
		return nil, ErrPeerClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("codexrtc: read frame: %w", context.Cause(ctx))
	}
}

// Close ends the connection and releases the codecs. It is idempotent.
func (p *Peer) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		close(p.done)
		p.mu.Unlock()
		p.closeErr = p.pc.Close()
		p.readerDone.Wait()
		p.closeErr = errors.Join(p.closeErr, p.encoder.Close(), p.decoder.Close())
	})
	return p.closeErr
}

func (p *Peer) onState(state webrtc.PeerConnectionState) {
	switch state {
	case webrtc.PeerConnectionStateConnected:
		p.stateOnce.Do(func() { close(p.connected) })
	case webrtc.PeerConnectionStateFailed:
		p.failOnce.Do(func() { close(p.failed) })
	case webrtc.PeerConnectionStateUnknown, webrtc.PeerConnectionStateNew, webrtc.PeerConnectionStateConnecting,
		webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateClosed:
	}
}

// drainRTCP reads the sender's RTCP so the interceptors keep running.
func (p *Peer) drainRTCP(sender *webrtc.RTPSender) {
	defer p.readerDone.Done()
	for {
		if _, _, err := sender.ReadRTCP(); err != nil {
			return
		}
	}
}

func (p *Peer) onTrack(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	if !strings.EqualFold(remote.Codec().MimeType, webrtc.MimeTypeOpus) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.trackRead {
		return
	}
	p.trackRead = true
	p.readerDone.Add(1)
	go p.readTrack(remote)
}

func (p *Peer) readTrack(remote *webrtc.TrackRemote) {
	defer p.readerDone.Done()
	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			return
		}
		if !p.receive(packet) {
			return
		}
	}
}

// receive decodes one RTP packet onto the inbound queue. A sequence gap is
// packet loss: up to MaxConcealedFrames frames are synthesized by Opus
// packet-loss concealment first, so playback keeps its timing. A late or
// repeated packet is dropped, because its slot was already played or
// concealed. It reports false once the peer is closed.
func (p *Peer) receive(packet *rtp.Packet) bool {
	missing, fresh := p.seq.next(packet.SequenceNumber)
	if !fresh {
		p.late.Add(1)
		return true
	}
	for range min(missing, MaxConcealedFrames) {
		frame, err := p.decoder.DecodePLC()
		if err != nil {
			break // no history yet: nothing to conceal from
		}
		p.concealed.Add(1)
		if !p.enqueue(frame) {
			return false
		}
	}
	frame, err := p.decoder.Decode(packet.Payload)
	if err != nil {
		return true
	}
	return p.enqueue(frame)
}

func (p *Peer) enqueue(frame []int16) bool {
	select {
	case p.inbound <- frame:
		return true
	case <-p.done:
		return false
	}
}

// ConcealedFrames reports how many frames packet-loss concealment
// synthesized for inbound sequence gaps.
func (p *Peer) ConcealedFrames() int64 { return p.concealed.Load() }

// LatePackets reports how many inbound packets arrived after their slot and
// were dropped.
func (p *Peer) LatePackets() int64 { return p.late.Load() }

// sequenceTracker follows a 16-bit RTP sequence with wraparound.
type sequenceTracker struct {
	started bool
	last    uint16
}

// next reports how many packets are missing before seq, and whether seq is
// new: a packet at or behind the last one (within half the sequence space)
// is late or repeated.
func (t *sequenceTracker) next(seq uint16) (missing int, fresh bool) {
	if !t.started {
		t.started, t.last = true, seq
		return 0, true
	}
	delta := seq - t.last
	if delta == 0 || delta >= 1<<15 {
		return 0, false
	}
	t.last = seq
	return int(delta) - 1, true
}
