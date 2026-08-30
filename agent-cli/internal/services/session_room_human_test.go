package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunRoom_HumanParticipantRoutesDevicesAndReportsReadiness(t *testing.T) {
	registry := newRoomHumanTestRegistry(t)
	inferencer := &roomTestInferencer{events: []messages.StreamMessage{roomTestSessionOpen("agent")}}
	opts := newRoomHumanRunOptions(registry, inferencer)

	ready := make(chan RoomParticipantReady, 2)
	inputToAgent := make(chan roomAudioFrame, 512)
	fanned := make(chan [2]string, 8)
	opts.OnParticipantReady = func(event RoomParticipantReady) { ready <- event }
	opts.OnAudioInput = func(id string, pcm []byte) error {
		inputToAgent <- roomAudioFrame{id: id, pcm: append([]byte(nil), pcm...)}
		return nil
	}
	opts.onParticipantAudioFanned = func(sourceID, targetID string, _ []byte) {
		fanned <- [2]string{sourceID, targetID}
	}

	resultCh := make(chan roomTestRunOutcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		resultCh <- roomTestRunOutcome{result: result, err: err}
	}()

	readyEvents := make(map[string]RoomParticipantReady, 2)
	for len(readyEvents) < 2 {
		select {
		case event := <-ready:
			readyEvents[event.ParticipantID] = event
		case <-time.After(2 * time.Second):
			t.Fatalf("readiness events = %v, want customer and agent", readyEvents)
		}
	}
	if event := readyEvents["customer"]; event.Kind != room.ParticipantKindHuman || event.InputDevice != string(registry.inputDevice.ID) || event.OutputDevice != string(registry.outputDevice.ID) || event.Provider != "" || event.Model != "" {
		t.Fatalf("customer readiness = %+v, want selected devices without provider metadata", event)
	}
	if event := readyEvents["agent"]; event.Kind != room.ParticipantKindAgent || event.Provider != "test-provider" || event.Model != "test-model" || event.InputDevice != "" || event.OutputDevice != "" {
		t.Fatalf("agent readiness = %+v, want provider metadata without devices", event)
	}
	if registry.inputHandle.closeCallsSnapshot() != 0 || registry.outputHandle.closeCallsSnapshot() != 0 {
		t.Fatal("room closed devices before cancellation")
	}

	session := waitRoomHumanTestSession(t, inferencer)
	const agentValue int16 = 0x5678
	if !session.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	}) || !session.receive.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeAudioStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioStartValue(),
	}) || !session.receive.Write(context.Background(), roomTestAudioEvent(agentValue, audio.FrameSize)) || !session.receive.Write(context.Background(), roomTestAudioEvent(agentValue, audio.FrameSize)) {
		t.Fatal("scripted provider stopped before output audio was accepted")
	}

	if !waitForRoomHumanOutput(t, registry.outputHandle.frames, agentValue) {
		t.Fatal("customer output did not receive scripted agent PCM")
	}

	const customerValue int16 = 0x1234
	customerFrame := make([]int16, audio.FrameSize)
	for index := range customerFrame {
		customerFrame[index] = customerValue
	}
	registry.inputHandle.push(customerFrame)

	gotInput := false
	seenFanned := make(map[[2]string]bool)
	inputDeadline := time.NewTimer(2 * time.Second)
	defer inputDeadline.Stop()
	for !gotInput {
		select {
		case frame := <-inputToAgent:
			if frame.id != "agent" {
				t.Fatalf("audio input delivered to %q, want agent", frame.id)
			}
			if bytes.Equal(frame.pcm, roomPCM16(customerValue, audio.FrameSize)) {
				gotInput = true
			}
			if bytes.Equal(frame.pcm, roomPCM16(agentValue, audio.FrameSize)) {
				t.Fatal("agent received its own output PCM")
			}
		case pair := <-fanned:
			if pair[0] == pair[1] {
				t.Fatalf("participant self-fanout = %v", pair)
			}
			seenFanned[pair] = true
		case <-inputDeadline.C:
			t.Fatalf("agent did not receive customer PCM; fanout=%v", seenFanned)
		}
	}

	if !seenFanned[[2]string{"customer", "agent"}] || !seenFanned[[2]string{"agent", "customer"}] {
		t.Fatalf("N-1 fanout = %v, want customer->agent and agent->customer", seenFanned)
	}
	waitForRoomHumanResponseCancel(t, session, customerValue)

	cancel()
	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			t.Fatalf("room cancellation: %v", outcome.err)
		}
		if outcome.result.Reason != RoomTerminationStopped || len(outcome.result.ActiveParticipants) != 0 {
			t.Fatalf("room result = %+v, want stopped with no active participants", outcome.result)
		}
		if len(outcome.result.Participants) != 2 || !outcome.result.Participants["customer"].Connected || !outcome.result.Participants["agent"].Connected {
			t.Fatalf("participant results = %+v, want connected customer and agent", outcome.result.Participants)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("room did not terminate after cancellation")
	}
	if registry.inputHandle.closeCallsSnapshot() != 1 || registry.outputHandle.closeCallsSnapshot() != 1 {
		t.Fatalf("device close calls = input:%d output:%d, want exactly once", registry.inputHandle.closeCallsSnapshot(), registry.outputHandle.closeCallsSnapshot())
	}
}

func TestRunRoom_HumanDeviceReadFailureFailsWholeRoom(t *testing.T) {
	registry := newRoomHumanTestRegistry(t)
	readErr := errors.New("microphone lost after secret-room-key")
	registry.inputHandle.readErr = readErr
	inferencer := &roomTestInferencer{events: []messages.StreamMessage{roomTestSessionOpen("agent")}}
	opts := newRoomHumanRunOptions(registry, inferencer)

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err == nil || result.Reason != RoomTerminationFailed {
		t.Fatalf("device failure result=%+v err=%v, want failed room", result, err)
	}
	if stringsContainsAny(result.Error, "secret-room-key") || stringsContainsAny(err.Error(), "secret-room-key") {
		t.Fatalf("device failure leaked provider secret: result=%q err=%q", result.Error, err)
	}
	if !stringsContainsAny(result.Error, "microphone lost") || len(result.ActiveParticipants) != 0 {
		t.Fatalf("device failure result = %+v, want redacted microphone failure and no active participants", result)
	}
	calls := inferencer.sessionsSnapshot()
	closeCalls := 0
	if len(calls) == 1 {
		closeCalls = calls[0].closeCallsSnapshot()
	}
	if len(calls) != 1 || closeCalls != 1 {
		t.Fatalf("provider cleanup sessions=%d close_calls=%d, want one closed session; result=%+v err=%v", len(calls), closeCalls, result, err)
	}
	if registry.inputHandle.closeCallsSnapshot() != 1 || registry.outputHandle.closeCallsSnapshot() != 1 {
		t.Fatalf("device close calls = input:%d output:%d, want exactly once", registry.inputHandle.closeCallsSnapshot(), registry.outputHandle.closeCallsSnapshot())
	}
}

func newRoomHumanRunOptions(registry *roomHumanTestRegistry, inferencer *roomTestInferencer) RoomRunOptions {
	return RoomRunOptions{
		Manifest: room.Manifest{
			SchemaVersion: room.SchemaVersion,
			Room:          room.Room{Interactive: true},
			Participants: []room.Participant{
				{
					Kind:         room.ParticipantKindHuman,
					ID:           "customer",
					SystemPrompt: "human customer",
					Tools:        []string{},
				},
				{
					Kind:         room.ParticipantKindAgent,
					ID:           "agent",
					SystemPrompt: "provider agent",
					Provider:     "test-provider",
					Model:        "test-model",
					APIKeyEnv:    "ROOM_AGENT_KEY",
					Tools:        []string{},
				},
			},
		},
		CredentialLookup: func(name string) (string, bool) {
			if name == "ROOM_AGENT_KEY" {
				return "secret-room-key", true
			}
			return "", false
		},
		DeviceRegistry: registry,
		SessionInferencers: map[string]messages.SessionInferencer{
			"agent": inferencer,
		},
	}
}

func waitRoomHumanTestSession(t *testing.T, inferencer *roomTestInferencer) *roomTestSession {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if sessions := inferencer.sessionsSnapshot(); len(sessions) > 0 {
			return sessions[0]
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("scripted provider session did not connect")
			return nil
		}
	}
}

func waitForRoomHumanOutput(t *testing.T, frames <-chan []int16, want int16) bool {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame := <-frames:
			for _, sample := range frame {
				if sample == want {
					return true
				}
			}
		case <-deadline.C:
			return false
		}
	}
}

func waitForRoomHumanResponseCancel(t *testing.T, session *roomTestSession, customerValue int16) {
	t.Helper()
	wantPCM := roomPCM16(customerValue, audio.FrameSize)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cancelIndex := -1
		inputIndex := -1
		session.mu.Lock()
		for index, msg := range session.sent {
			if msg.Type == messages.StreamTypeResponseCancel && cancelIndex < 0 {
				cancelIndex = index
			}
			if msg.Type == messages.StreamTypeAudioDelta {
				if value, ok := msg.Value.(*messages.AudioDeltaValue); ok && value != nil && bytes.Equal(value.Content, wantPCM) {
					inputIndex = index
				}
			}
		}
		session.mu.Unlock()
		if cancelIndex >= 0 && inputIndex > cancelIndex {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("provider messages missing barge-in cancel before customer PCM: cancel=%d input=%d", cancelIndex, inputIndex)
			return
		}
	}
}

func stringsContainsAny(value string, want string) bool {
	return bytes.Contains([]byte(value), []byte(want))
}

type roomHumanTestRegistry struct {
	inputDevice  audio.Device
	outputDevice audio.Device
	inputHandle  *roomHumanInputHandle
	outputHandle *roomHumanOutputHandle
}

func newRoomHumanTestRegistry(t *testing.T) *roomHumanTestRegistry {
	t.Helper()
	inputDevice, err := audio.NewDevice("test", "microphone", "Test Microphone", audio.DirectionInput)
	if err != nil {
		t.Fatalf("new input device: %v", err)
	}
	outputDevice, err := audio.NewDevice("test", "speakers", "Test Speakers", audio.DirectionOutput)
	if err != nil {
		t.Fatalf("new output device: %v", err)
	}
	return &roomHumanTestRegistry{
		inputDevice:  inputDevice,
		outputDevice: outputDevice,
		inputHandle:  newRoomHumanInputHandle(),
		outputHandle: newRoomHumanOutputHandle(),
	}
}

func (r *roomHumanTestRegistry) List() ([]audio.Device, error) {
	return []audio.Device{r.inputDevice, r.outputDevice}, nil
}

func (r *roomHumanTestRegistry) Default(direction audio.Direction) (audio.Device, error) {
	switch direction {
	case audio.DirectionInput:
		return r.inputDevice, nil
	case audio.DirectionOutput:
		return r.outputDevice, nil
	default:
		return audio.Device{}, audio.NewNoDefaultDeviceError(direction)
	}
}

func (r *roomHumanTestRegistry) Open(id audio.DeviceID) (audio.OpenedDevice, error) {
	switch id {
	case r.inputDevice.ID:
		return r.inputHandle, nil
	case r.outputDevice.ID:
		return r.outputHandle, nil
	default:
		return nil, audio.NewDeviceNotFoundError(id)
	}
}

var _ audio.DeviceRegistry = (*roomHumanTestRegistry)(nil)

type roomHumanInputHandle struct {
	frames    chan []int16
	closed    chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	closeCall int
	readErr   error
}

func newRoomHumanInputHandle() *roomHumanInputHandle {
	return &roomHumanInputHandle{frames: make(chan []int16, 4), closed: make(chan struct{})}
}

func (h *roomHumanInputHandle) DeviceDirection() audio.Direction { return audio.DirectionInput }

func (h *roomHumanInputHandle) ReadFrame(ctx context.Context, frame []int16) error {
	h.mu.Lock()
	err := h.readErr
	h.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case samples := <-h.frames:
		copy(frame, samples)
		return nil
	case <-h.closed:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *roomHumanInputHandle) push(frame []int16) { h.frames <- append([]int16(nil), frame...) }

func (h *roomHumanInputHandle) Close() error {
	h.closeOnce.Do(func() {
		close(h.closed)
		h.mu.Lock()
		h.closeCall++
		h.mu.Unlock()
	})
	return nil
}

func (h *roomHumanInputHandle) closeCallsSnapshot() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeCall
}

type roomHumanOutputHandle struct {
	frames chan []int16
	closed chan struct{}
	once   sync.Once

	mu        sync.Mutex
	closeCall int
}

func newRoomHumanOutputHandle() *roomHumanOutputHandle {
	return &roomHumanOutputHandle{frames: make(chan []int16, 512), closed: make(chan struct{})}
}

func (h *roomHumanOutputHandle) DeviceDirection() audio.Direction { return audio.DirectionOutput }

func (h *roomHumanOutputHandle) WriteFrame(ctx context.Context, frame []int16) error {
	copyFrame := append([]int16(nil), frame...)
	select {
	case h.frames <- copyFrame:
		return nil
	case <-h.closed:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *roomHumanOutputHandle) Close() error {
	h.once.Do(func() {
		close(h.closed)
		h.mu.Lock()
		h.closeCall++
		h.mu.Unlock()
	})
	return nil
}

func (h *roomHumanOutputHandle) closeCallsSnapshot() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeCall
}

var _ interface {
	audio.OpenedDevice
	ReadFrame(context.Context, []int16) error
} = (*roomHumanInputHandle)(nil)

var _ interface {
	audio.OpenedDevice
	WriteFrame(context.Context, []int16) error
} = (*roomHumanOutputHandle)(nil)
