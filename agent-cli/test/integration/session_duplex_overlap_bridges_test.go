package integration

import runtimecontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"

import (
	"bytes"
	"context"
	"fmt"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"io"
	"sync"
)

type v8MultiTurnBridgePacket struct {
	eof      bool
	crossing v8Crossing
	ack      chan struct{}
}

type v8RuntimeInputEvent struct {
	observation runtimecontract.SessionRuntimeObservation
	// release is closed only after the bridge has recorded the accepted input
	// and completed its scheduled crossing, keeping a subsequent replay event
	// from observing the shared clock too early.
	release chan struct{}
}

type v8MultiTurnBridge struct {
	coordinator *v8MultiTurnCoordinator
	direction   string
	sender      *v8RecordingView
	receiver    *v8RecordingView
	eofReady    <-chan struct{}
	runtimeOut  chan runtimecontract.SessionRuntimeObservation
	runtimeIn   chan v8RuntimeInputEvent

	packets chan v8MultiTurnBridgePacket
	mu      sync.Mutex
	writes  int
	eofRead bool
	eofSeen chan struct{}
	eofOnce sync.Once
}

func newV8MultiTurnBridge(coordinator *v8MultiTurnCoordinator, direction string, sender, receiver *v8RecordingView, eofReady <-chan struct{}) *v8MultiTurnBridge {
	return &v8MultiTurnBridge{
		coordinator: coordinator,
		direction:   direction,
		sender:      sender,
		receiver:    receiver,
		eofReady:    eofReady,
		runtimeOut:  make(chan runtimecontract.SessionRuntimeObservation, 1),
		runtimeIn:   make(chan v8RuntimeInputEvent),
		packets:     make(chan v8MultiTurnBridgePacket, 2),
		eofSeen:     make(chan struct{}),
	}
}

func (b *v8MultiTurnBridge) acceptRuntimeOutput(observation runtimecontract.SessionRuntimeObservation) {
	select {
	case b.runtimeOut <- observation:
	case <-b.coordinator.abort:
	}
}

func (b *v8MultiTurnBridge) nextRuntimeOutput() (runtimecontract.SessionRuntimeObservation, error) {
	select {
	case observation := <-b.runtimeOut:
		return observation, nil
	case <-b.coordinator.abort:
		return runtimecontract.SessionRuntimeObservation{}, context.Canceled
	}
}

func (b *v8MultiTurnBridge) acceptRuntimeInput(observation runtimecontract.SessionRuntimeObservation) {
	event := v8RuntimeInputEvent{
		observation: observation,
		release:     make(chan struct{}),
	}
	select {
	case b.runtimeIn <- event:
		select {
		case <-event.release:
		case <-b.coordinator.abort:
		}
	case <-b.coordinator.abort:
	}
}

func (b *v8MultiTurnBridge) waitForRuntimeInput(crossing v8Crossing) error {
	var event v8RuntimeInputEvent
	select {
	case event = <-b.runtimeIn:
	case <-b.coordinator.abort:
		return context.Canceled
	}
	observation := event.observation
	if observation.Tick != crossing.Tick || !observation.Timestamp.Equal(crossing.Timestamp) || !bytes.Equal(observation.Payload, crossing.Emitted) {
		close(event.release)
		return fmt.Errorf("%s runtime input observation does not match turn %d at tick %d", b.direction, crossing.Turn, crossing.Tick)
	}
	err := b.coordinator.complete(crossing)
	close(event.release)
	return err
}

func (b *v8MultiTurnBridge) write(data []byte) (int, error) {
	if len(data) != v8PCMFrameBytes {
		return 0, fmt.Errorf("%s emitted %d PCM bytes, want one %d-byte frame", b.direction, len(data), v8PCMFrameBytes)
	}
	outputObservation, err := b.nextRuntimeOutput()
	if err != nil {
		return 0, err
	}
	if outputObservation.Kind != runtimecontract.SessionRuntimeObservationAudioOutput {
		return 0, fmt.Errorf("%s runtime observation kind = %q, want %q", b.direction, outputObservation.Kind, runtimecontract.SessionRuntimeObservationAudioOutput)
	}
	emitted := append([]byte(nil), data...)
	if !bytes.Equal(outputObservation.Payload, emitted) {
		return 0, fmt.Errorf("%s runtime audio output payload differs from the CLI writer payload: runtime hash=%s writer hash=%s", b.direction, v8PCMHash(outputObservation.Payload), v8PCMHash(emitted))
	}
	crossing, err := b.coordinator.record(b.direction, outputObservation.Tick, outputObservation.Timestamp, emitted, emitted)
	if err != nil {
		return 0, err
	}
	if crossing.Direction == "A-to-B" && crossing.Schedule < len(v8MultiTurnSchedule())-2 && v8MultiTurnSchedule()[crossing.Schedule].Overlapping {
		// Each overlapping replay records both server output intervals before
		// either client sends the peer AUDIO.DELTA. This preserves the strict
		// capture order while retaining the equal-tick overlap.
		if err := b.coordinator.waitForRecord(crossing.Schedule + 1); err != nil {
			return 0, err
		}
	}
	b.sender.record(crossing, emitted)
	ack := make(chan struct{})
	select {
	case b.packets <- v8MultiTurnBridgePacket{crossing: crossing, ack: ack}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	// Holding the output boundary until the peer has consumed the packet makes
	// the overlap observable: the next user's PCM starts while this response
	// is still in the bridge, without sending RESPONSE.CANCEL. The final
	// response also waits for peer input acceptance; its EOF is released only
	// after the peer has completed its second turn so the raw input commit
	// cannot preempt the queued response events.
	finalSchedule := len(v8MultiTurnSchedule()) - 1
	if crossing.Schedule == finalSchedule {
		select {
		case <-ack:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
		if err := b.waitForRuntimeInput(crossing); err != nil {
			return 0, err
		}
		b.mu.Lock()
		b.writes++
		writes := b.writes
		b.mu.Unlock()
		if writes == v8MultiTurnCount {
			if err := b.waitForEOF(); err != nil {
				return 0, err
			}
			select {
			case b.packets <- v8MultiTurnBridgePacket{eof: true}:
			case <-b.coordinator.abort:
				return 0, context.Canceled
			}
			select {
			case <-b.eofSeen:
			case <-b.coordinator.abort:
				return 0, context.Canceled
			}
		}
		return len(data), nil
	}
	select {
	case <-ack:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	if err := b.waitForRuntimeInput(crossing); err != nil {
		return 0, err
	}
	// Do not let A's third server response take its runtime clock snapshot
	// before B's second directional interval has completed. The replay streams
	// intentionally expose those independent boundaries concurrently, so this
	// release keeps the shared deterministic clock at the scheduled tick rather
	// than asking the bridge to repair a stale observation after the fact.
	switch crossing.Schedule {
	case 0:
		if err := b.coordinator.waitForCompletion(1); err != nil {
			return 0, err
		}
	case 1:
		if err := b.coordinator.waitForCompletion(0); err != nil {
			return 0, err
		}
	case 2:
		if err := b.coordinator.waitForCompletion(3); err != nil {
			return 0, err
		}
	case 3:
		if err := b.coordinator.waitForCompletion(2); err != nil {
			return 0, err
		}
	}
	b.mu.Lock()
	b.writes++
	writes := b.writes
	b.mu.Unlock()
	if writes == v8MultiTurnCount {
		if err := b.waitForEOF(); err != nil {
			return 0, err
		}
		select {
		case b.packets <- v8MultiTurnBridgePacket{eof: true}:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
		select {
		case <-b.eofSeen:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
	}
	return len(data), nil
}

func (b *v8MultiTurnBridge) waitForEOF() error {
	if b.eofReady == nil {
		return nil
	}
	select {
	case <-b.eofReady:
		return nil
	case <-b.coordinator.abort:
		return context.Canceled
	}
}

func (b *v8MultiTurnBridge) read(ctx context.Context, destination []byte) (int, error) {
	if len(destination) < v8PCMFrameBytes {
		return 0, fmt.Errorf("%s receiver requested %d PCM bytes, want at least %d", b.direction, len(destination), v8PCMFrameBytes)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.coordinator.abort:
		return 0, context.Canceled
	case packet := <-b.packets:
		if packet.eof {
			b.mu.Lock()
			b.eofRead = true
			b.mu.Unlock()
			b.eofOnce.Do(func() { close(b.eofSeen) })
			return 0, io.EOF
		}
		copy(destination, packet.crossing.Emitted)
		b.receiver.record(packet.crossing, packet.crossing.Emitted)
		close(packet.ack)
		return len(packet.crossing.Emitted), nil
	}
}

func (b *v8MultiTurnBridge) wroteFrames() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes
}

func (b *v8MultiTurnBridge) observedEOF() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eofRead
}

type v8MultiTurnPCMWriter struct{ bridge *v8MultiTurnBridge }

func (w v8MultiTurnPCMWriter) Write(data []byte) (int, error) { return w.bridge.write(data) }

type v8MultiTurnPCMReader struct {
	bridge          *v8MultiTurnBridge
	boundaryPending bool
}

func (r *v8MultiTurnPCMReader) Read(data []byte) (int, error) {
	return r.ReadContext(context.Background(), data)
}

func (r *v8MultiTurnPCMReader) ReadContext(ctx context.Context, data []byte) (int, error) {
	if r.boundaryPending {
		r.boundaryPending = false
		return 0, audio.ErrEndOfTurn
	}
	count, err := r.bridge.read(ctx, data)
	if err == nil && count > 0 {
		r.boundaryPending = true
	}
	return count, err
}

type v8BridgePacket struct {
	eof      bool
	crossing v8Crossing
}

type v8PCMBridge struct {
	coordinator *v8CrossingCoordinator
	direction   string
	sender      *v8RecordingView
	receiver    *v8RecordingView
	silence     []byte
	mutateFirst bool
	runtimeOut  chan runtimecontract.SessionRuntimeObservation

	packets chan v8BridgePacket
	mu      sync.Mutex
	written bool
	eofRead bool
}

func newV8PCMBridge(coordinator *v8CrossingCoordinator, direction string, sender, receiver *v8RecordingView, silence []byte, mutateFirst bool) *v8PCMBridge {
	return &v8PCMBridge{
		coordinator: coordinator,
		direction:   direction,
		sender:      sender,
		receiver:    receiver,
		silence:     append([]byte(nil), silence...),
		mutateFirst: mutateFirst,
		packets:     make(chan v8BridgePacket, 2),
		runtimeOut:  make(chan runtimecontract.SessionRuntimeObservation, 1),
	}
}

func (b *v8PCMBridge) acceptRuntimeOutput(observation runtimecontract.SessionRuntimeObservation) {
	select {
	case b.runtimeOut <- observation:
	case <-b.coordinator.abort:
	}
}

func (b *v8PCMBridge) nextRuntimeOutput() (runtimecontract.SessionRuntimeObservation, error) {
	select {
	case observation := <-b.runtimeOut:
		return observation, nil
	case <-b.coordinator.abort:
		return runtimecontract.SessionRuntimeObservation{}, context.Canceled
	}
}

func (b *v8PCMBridge) write(data []byte) (int, error) {
	if len(data) != v8PCMFrameBytes {
		return 0, fmt.Errorf("%s emitted %d PCM bytes, want one %d-byte frame", b.direction, len(data), v8PCMFrameBytes)
	}

	b.mu.Lock()
	if b.written {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	b.written = true
	b.mu.Unlock()

	emitted := append([]byte(nil), data...)
	delivered := append([]byte(nil), emitted...)
	if b.mutateFirst {
		if len(b.silence) != len(delivered) {
			return 0, fmt.Errorf("%s silence mutation is %d bytes, want %d", b.direction, len(b.silence), len(delivered))
		}
		delivered = append([]byte(nil), b.silence...)
	}
	outputObservation, err := b.nextRuntimeOutput()
	if err != nil {
		return 0, err
	}
	if outputObservation.Kind != runtimecontract.SessionRuntimeObservationAudioOutput {
		return 0, fmt.Errorf("%s runtime observation kind = %q, want %q", b.direction, outputObservation.Kind, runtimecontract.SessionRuntimeObservationAudioOutput)
	}
	if !bytes.Equal(outputObservation.Payload, emitted) {
		return 0, fmt.Errorf("%s runtime audio output payload differs from the CLI writer payload: runtime hash=%s writer hash=%s", b.direction, v8PCMHash(outputObservation.Payload), v8PCMHash(emitted))
	}
	crossing, err := b.coordinator.record(b.direction, outputObservation.Tick, outputObservation.Timestamp, emitted, delivered)
	if err != nil {
		return 0, err
	}
	b.sender.record(crossing, emitted)
	if b.direction == "A-to-B" {
		select {
		case <-b.coordinator.deliveryReady:
		case <-b.coordinator.abort:
			return 0, context.Canceled
		}
	} else {
		// Do not release A-to-B to B until B has emitted its own response. This
		// preserves the replayed server-output-before-client-input order on
		// both CLIs while retaining one equal-tick overlap window.
		b.coordinator.releaseDelivery()
	}

	select {
	case b.packets <- v8BridgePacket{crossing: crossing}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	// The one-frame input turn is intentionally closed after the first
	// crossing. That EOF is what makes the peer CLI send its MESSAGE.END and
	// exercises the shipped audio-input commit path.
	select {
	case b.packets <- v8BridgePacket{eof: true}:
	case <-b.coordinator.abort:
		return 0, context.Canceled
	}
	return len(data), nil
}

func (b *v8PCMBridge) read(ctx context.Context, destination []byte) (int, error) {
	if len(destination) < v8PCMFrameBytes {
		return 0, fmt.Errorf("%s receiver requested %d PCM bytes, want at least %d", b.direction, len(destination), v8PCMFrameBytes)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-b.coordinator.abort:
		return 0, context.Canceled
	case packet := <-b.packets:
		if packet.eof {
			b.mu.Lock()
			b.eofRead = true
			b.mu.Unlock()
			return 0, io.EOF
		}
		copy(destination, packet.crossing.Delivered)
		b.receiver.record(packet.crossing, packet.crossing.Delivered)
		return len(packet.crossing.Delivered), nil
	}
}

func (b *v8PCMBridge) wroteFrame() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written
}

func (b *v8PCMBridge) observedEOF() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.eofRead
}
