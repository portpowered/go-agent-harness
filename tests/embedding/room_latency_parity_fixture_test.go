package embedding_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type publicRoomLatencyResponseStart struct {
	participantID string
	responseID    string
	tick          uint64
	at            time.Time
}

type publicRoomLatencyAudio struct {
	participantID string
	responseID    string
	pcm           []byte
	tick          uint64
}

type publicRoomLatencyFanout struct {
	sourceID string
	targetID string
	pcm      []byte
}

type publicRoomLatencyInput struct {
	participantID string
	pcm           []byte
}

type publicRoomLatencyRunOutcome struct {
	result runtimeRooms.RoomResult
	err    error
}

type publicRoomLatencyLiveService struct {
	provider *publicRoomLatencyProvider
}

func (s *publicRoomLatencyLiveService) OpenLive(ctx context.Context, request session.LiveRequest) (session.LiveHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.provider == nil {
		return nil, errors.New("public latency provider is unavailable")
	}
	participant := s.provider.participant(request.ParticipantID)
	if participant == nil {
		return nil, fmt.Errorf("unknown latency participant %q", request.ParticipantID)
	}
	handle := newPublicRoomLatencyLiveHandle(s.provider, participant, request.Replay.OutputCapturePath)
	participant.mu.Lock()
	if participant.handle != nil {
		participant.mu.Unlock()
		return nil, fmt.Errorf("latency participant %q connected twice", participant.id)
	}
	participant.handle = handle
	participant.mu.Unlock()
	return handle, nil
}

type publicRoomLatencyProvider struct {
	clock        *platformclock.Deterministic
	inputEvents  chan publicRoomLatencyInput
	participants map[string]*publicRoomLatencyParticipant
	audioEvents  chan<- publicRoomLatencyAudio
	fanouts      chan<- publicRoomLatencyFanout
}

type publicRoomLatencyParticipant struct {
	id       string
	provider *publicRoomLatencyProvider
	handle   *publicRoomLatencyLiveHandle

	mu              sync.Mutex
	pendingInput    []byte
	responseActive  bool
	responseID      string
	queuedOutput    bool
	turnsCompleted  int
	responseCreates []string
	commits         [][]byte
	audioInputs     [][]byte
	controls        []session.LiveControl
}

func newPublicRoomLatencyProvider(participantIDs []string, clock *platformclock.Deterministic) *publicRoomLatencyProvider {
	provider := &publicRoomLatencyProvider{
		clock:        clock,
		inputEvents:  make(chan publicRoomLatencyInput, 8),
		participants: make(map[string]*publicRoomLatencyParticipant, len(participantIDs)),
	}
	for _, participantID := range participantIDs {
		provider.participants[participantID] = &publicRoomLatencyParticipant{id: participantID, provider: provider}
	}
	return provider
}

func (p *publicRoomLatencyProvider) participant(id string) *publicRoomLatencyParticipant {
	if p == nil {
		return nil
	}
	return p.participants[id]
}

func (p *publicRoomLatencyProvider) startOpening(participantID string, pcm []byte) (string, error) {
	return p.startResponse(participantID, pcm)
}

func (p *publicRoomLatencyProvider) startResponse(participantID string, pcm []byte) (string, error) {
	participant := p.participant(participantID)
	if participant == nil {
		return "", fmt.Errorf("unknown latency participant %q", participantID)
	}
	participant.mu.Lock()
	participant.commits = append(participant.commits, append([]byte(nil), pcm...))
	participant.mu.Unlock()
	return p.enqueueResponseBoundaries(participantID)
}

func (p *publicRoomLatencyProvider) stopSpeech(participantID string) error {
	participant := p.participant(participantID)
	if participant == nil {
		return fmt.Errorf("unknown latency participant %q", participantID)
	}
	participant.mu.Lock()
	pcm := append([]byte(nil), participant.pendingInput...)
	participant.pendingInput = nil
	participant.mu.Unlock()
	if len(pcm) == 0 {
		return fmt.Errorf("participant %q has no pending non-empty input", participantID)
	}
	participant.handle.emit(session.LiveEvent{Kind: "speech_stopped"})
	participant.mu.Lock()
	participant.commits = append(participant.commits, pcm)
	participant.mu.Unlock()
	_, err := p.enqueueResponseBoundaries(participantID)
	return err
}

func (p *publicRoomLatencyProvider) enqueueResponseBoundaries(participantID string) (string, error) {
	participant := p.participant(participantID)
	if participant == nil {
		return "", fmt.Errorf("unknown latency participant %q", participantID)
	}
	participant.mu.Lock()
	responseID := fmt.Sprintf("response-%s-%02d", participantID, len(participant.responseCreates)+1)
	participant.responseCreates = append(participant.responseCreates, responseID)
	participant.responseID = responseID
	participant.responseActive = true
	handle := participant.handle
	participant.mu.Unlock()
	if handle == nil {
		return "", fmt.Errorf("participant %q has no live handle", participantID)
	}
	handle.emit(session.LiveEvent{Kind: "input_commit"})
	handle.emit(session.LiveEvent{Kind: "response_create", ResponseID: responseID})
	handle.emit(session.LiveEvent{Kind: "message_start", ResponseID: responseID, Role: messages.RoleAssistant})
	return responseID, nil
}

func (p *publicRoomLatencyProvider) releaseResponse(participantID, responseID string, pcm []byte) error {
	participant := p.participant(participantID)
	if participant == nil {
		return fmt.Errorf("unknown latency participant %q", participantID)
	}
	participant.mu.Lock()
	if !participant.responseActive || participant.responseID != responseID {
		participant.mu.Unlock()
		return fmt.Errorf("response %s is not active for %s", responseID, participantID)
	}
	participant.responseActive = false
	participant.queuedOutput = true
	handle := participant.handle
	participant.mu.Unlock()
	if handle == nil {
		return fmt.Errorf("participant %q has no live handle", participantID)
	}
	// Publish the provider landmark before the media frame. The room recorder
	// can therefore correlate this response even if the graph worker is
	// scheduled immediately after the inbound frame is admitted.
	handle.emit(session.LiveEvent{Kind: "audio_delta", ResponseID: responseID, Role: messages.RoleAssistant})
	handle.emit(session.LiveEvent{Kind: "message_end", ResponseID: responseID, Role: messages.RoleAssistant})
	// Keep the provider landmark causally before the mixed peer emission even
	// when both are observed during one logical scheduler tick.
	p.clock.AdvanceBy(time.Millisecond)
	return handle.inbound.push(publicRoomLatencyPCMFrame(pcm))
}

func publicRoomLatencyPCMFrame(pcm []byte) audio.PCMFrame {
	samples := make([]int16, len(pcm)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(pcm[index*2:]))
	}
	return audio.PCMFrame{Samples: samples, Format: audio.PCM16DeviceFormat(1000), EndOfResponse: true}
}

func (p *publicRoomLatencyProvider) completeTurn(participantID string) {
	if participant := p.participant(participantID); participant != nil && participant.handle != nil {
		participant.mu.Lock()
		participant.turnsCompleted++
		participant.mu.Unlock()
		participant.handle.emit(session.LiveEvent{Kind: "turn_completed"})
	}
}

func (p *publicRoomLatencyProvider) acceptAudio(targetID string, frame audio.PCMFrame) {
	if p == nil || len(frame.Samples) == 0 || publicRoomLatencySamplesSilent(frame.Samples) {
		return
	}
	target := p.participant(targetID)
	if target == nil {
		return
	}
	sourceID := ""
	for candidateID, candidate := range p.participants {
		if candidateID == targetID {
			continue
		}
		candidate.mu.Lock()
		if candidate.queuedOutput {
			candidate.queuedOutput = false
			sourceID = candidateID
			candidate.mu.Unlock()
			break
		}
		candidate.mu.Unlock()
	}
	pcm := publicRoomLatencyPCMBytes(frame.Samples)
	target.mu.Lock()
	acceptInput := target.turnsCompleted < 2
	if acceptInput && len(target.pendingInput) == 0 && !target.responseActive {
		target.pendingInput = append([]byte(nil), pcm...)
		target.audioInputs = append(target.audioInputs, append([]byte(nil), pcm...))
	}
	target.mu.Unlock()
	if sourceID == "" {
		return
	}
	if acceptInput {
		select {
		case p.inputEvents <- publicRoomLatencyInput{participantID: targetID, pcm: append([]byte(nil), pcm...)}:
		default:
		}
	}
	if p.fanouts != nil {
		p.fanouts <- publicRoomLatencyFanout{sourceID: sourceID, targetID: targetID, pcm: append([]byte(nil), pcm...)}
	}
}

func publicRoomLatencyPCMBytes(samples []int16) []byte {
	pcm := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[index*2:], uint16(sample))
	}
	return pcm
}

func publicRoomLatencySamplesSilent(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func (p *publicRoomLatencyProvider) currentResponseID(participantID string) string {
	participant := p.participant(participantID)
	if participant == nil {
		return ""
	}
	participant.mu.Lock()
	defer participant.mu.Unlock()
	return participant.responseID
}

func (p *publicRoomLatencyProvider) assertScriptedTurns(t *testing.T, participantID string, wantResponses int, wantCommits [][]byte) {
	t.Helper()
	participant := p.participant(participantID)
	participant.mu.Lock()
	responses := append([]string(nil), participant.responseCreates...)
	commits := make([][]byte, len(participant.commits))
	for index, pcm := range participant.commits {
		commits[index] = append([]byte(nil), pcm...)
	}
	participant.mu.Unlock()
	if len(responses) != wantResponses {
		t.Fatalf("%s response.create count = %d, want %d (%v)", participantID, len(responses), wantResponses, responses)
	}
	if len(commits) != len(wantCommits) {
		t.Fatalf("%s commit count = %d, want %d", participantID, len(commits), len(wantCommits))
	}
	for index, want := range wantCommits {
		if len(commits[index]) == 0 || !bytesEqual(commits[index], want) {
			t.Fatalf("%s commit %d = %v, want exact fixture %v", participantID, index+1, commits[index], want)
		}
	}
}

func (p *publicRoomLatencyProvider) assertNoOutboundResponseControls(t *testing.T) {
	t.Helper()
	for participantID, participant := range p.participants {
		participant.mu.Lock()
		controls := append([]session.LiveControl(nil), participant.controls...)
		participant.mu.Unlock()
		for _, control := range controls {
			if control.Kind == session.LiveControlResponseCreate || control.Kind == session.LiveControlClose {
				t.Fatalf("scripted provider %s observed unexpected outbound %s", participantID, control.Kind)
			}
		}
	}
}

func (p *publicRoomLatencyProvider) assertInputFrames(t *testing.T, wantCounts map[string]int, wantPCM []byte) {
	t.Helper()
	for participantID, participant := range p.participants {
		participant.mu.Lock()
		received := make([][]byte, len(participant.audioInputs))
		for index, pcm := range participant.audioInputs {
			received[index] = append([]byte(nil), pcm...)
		}
		participant.mu.Unlock()
		if len(received) != wantCounts[participantID] {
			t.Fatalf("%s provider audio input frames = %d, want %d", participantID, len(received), wantCounts[participantID])
		}
		for index, pcm := range received {
			if !bytesEqual(pcm, wantPCM) {
				t.Fatalf("%s received frame %d = %v, want exact fixture %v", participantID, index+1, pcm, wantPCM)
			}
		}
	}
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type publicRoomLatencyLiveHandle struct {
	provider    *publicRoomLatencyProvider
	participant *publicRoomLatencyParticipant
	inbound     *publicRoomLatencyInbound
	outbound    *publicRoomLatencyOutbound
	events      chan session.LiveEvent
	done        chan struct{}
	capturePath string
	clock       *platformclock.Deterministic

	mu         sync.Mutex
	sequence   uint64
	cancelErr  error
	closeErr   error
	started    bool
	closed     bool
	doneOnce   sync.Once
	eventsOnce sync.Once
	mediaOnce  sync.Once
}

func newPublicRoomLatencyLiveHandle(provider *publicRoomLatencyProvider, participant *publicRoomLatencyParticipant, capturePath string) *publicRoomLatencyLiveHandle {
	handle := &publicRoomLatencyLiveHandle{
		provider: provider, participant: participant, events: make(chan session.LiveEvent, 256), done: make(chan struct{}), capturePath: capturePath, clock: provider.clock,
	}
	handle.inbound = &publicRoomLatencyInbound{clock: provider.clock, participantID: participant.id, provider: provider, frames: make(chan audio.PCMFrame, 8), done: make(chan struct{})}
	handle.outbound = &publicRoomLatencyOutbound{provider: provider, targetID: participant.id, done: make(chan struct{})}
	handle.inbound.onRead = func(frame audio.PCMFrame) {
		if len(frame.Samples) == 0 || provider.audioEvents == nil {
			return
		}
		provider.audioEvents <- publicRoomLatencyAudio{participantID: participant.id, responseID: provider.currentResponseID(participant.id), pcm: publicRoomLatencyPCMBytes(frame.Samples), tick: provider.clock.Tick()}
	}
	return handle
}

func (h *publicRoomLatencyLiveHandle) Media() audio.MediaEndpoints {
	if h == nil {
		return audio.MediaEndpoints{}
	}
	return audio.MediaEndpoints{Inbound: h.inbound, Outbound: h.outbound}
}

func (h *publicRoomLatencyLiveHandle) Events() <-chan session.LiveEvent {
	if h == nil {
		return nil
	}
	return h.events
}

func (h *publicRoomLatencyLiveHandle) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("latency live handle is closed")
	}
	h.started = true
	h.mu.Unlock()
	return nil
}

func (h *publicRoomLatencyLiveHandle) Send(ctx context.Context, control session.LiveControl) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("latency live handle is closed")
	}
	h.participant.mu.Lock()
	h.participant.controls = append(h.participant.controls, control)
	h.participant.mu.Unlock()
	h.mu.Unlock()
	return nil
}

func (h *publicRoomLatencyLiveHandle) Cancel(err error) {
	if h == nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	h.mu.Lock()
	if h.cancelErr == nil {
		h.cancelErr = err
	}
	h.mu.Unlock()
	h.doneOnce.Do(func() { close(h.done) })
}

func (h *publicRoomLatencyLiveHandle) Wait() error {
	if h == nil {
		return errors.New("latency live handle is nil")
	}
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelErr
}

func (h *publicRoomLatencyLiveHandle) Close() error {
	if h == nil {
		return nil
	}
	h.mediaOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		capturePath := h.capturePath
		h.mu.Unlock()
		h.Cancel(context.Canceled)
		if err := h.inbound.Close(); err != nil {
			h.recordCloseError(err)
		}
		if err := h.outbound.Close(); err != nil {
			h.recordCloseError(err)
		}
		h.eventsOnce.Do(func() { close(h.events) })
		if capturePath != "" {
			if err := os.WriteFile(capturePath, []byte("[]\n"), 0o600); err != nil {
				h.recordCloseError(err)
			}
		}
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeErr
}

func (h *publicRoomLatencyLiveHandle) recordCloseError(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closeErr == nil {
		h.closeErr = err
	}
}

func (h *publicRoomLatencyLiveHandle) emit(event session.LiveEvent) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.sequence++
	event.Sequence = h.sequence
	if event.SessionID == "" {
		event.SessionID = "session-" + h.participant.id
	}
	if event.ParticipantID == "" {
		event.ParticipantID = h.participant.id
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = h.clock.Now()
	}
	h.mu.Unlock()
	select {
	case h.events <- event:
	case <-h.done:
	}
}

type publicRoomLatencyInbound struct {
	provider      *publicRoomLatencyProvider
	participantID string
	clock         *platformclock.Deterministic
	frames        chan audio.PCMFrame
	done          chan struct{}
	onRead        func(audio.PCMFrame)
	closeOnce     sync.Once
}

func (i *publicRoomLatencyInbound) ReadFrame(ctx context.Context) (audio.PCMFrame, error) {
	select {
	case frame := <-i.frames:
		if i.onRead != nil {
			i.onRead(frame)
		}
		return frame, nil
	case <-i.done:
		return audio.PCMFrame{}, io.EOF
	case <-ctx.Done():
		return audio.PCMFrame{}, ctx.Err()
	}
}

func (i *publicRoomLatencyInbound) push(frame audio.PCMFrame) error {
	select {
	case i.frames <- frame:
		return nil
	case <-i.done:
		return io.EOF
	}
}

func (i *publicRoomLatencyInbound) Close() error {
	i.closeOnce.Do(func() { close(i.done) })
	return nil
}

type publicRoomLatencyOutbound struct {
	provider *publicRoomLatencyProvider
	targetID string
	done     chan struct{}
	once     sync.Once
}

func (o *publicRoomLatencyOutbound) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	select {
	case <-o.done:
		return io.EOF
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	o.provider.acceptAudio(o.targetID, frame)
	return nil
}

func (o *publicRoomLatencyOutbound) Close() error {
	o.once.Do(func() { close(o.done) })
	return nil
}
