package mediagate

import (
	"context"
	"fmt"
	"strings"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// registerAck creates a private provider-dispatch acknowledgement for one
// explicit live control. AgentLoop's public enqueue methods acknowledge only
// admission into their runner queue; the live boundary needs to wait until the
// wrapped provider Session.Send has accepted the control so a subsequent PCM
// frame cannot overtake it on a separate provider media queue.
func (g *Gate) RegisterAck() (string, <-chan bool, error) {
	if g == nil {
		return "", nil, ErrMediaUnavailable
	}
	if g.isClosed() {
		return "", nil, sharedaudio.ErrSessionMediaClosed
	}
	g.ackMu.Lock()
	g.nextAckID++
	id := fmt.Sprintf("%s%d", liveControlAckPrefix, g.nextAckID)
	ack := make(chan bool, 1)
	control := &controlAck{accepted: ack}
	g.acks[id] = control
	g.ackMu.Unlock()
	if err := g.registerControl(id); err != nil {
		g.ackMu.Lock()
		delete(g.acks, id)
		g.ackMu.Unlock()
		control.canceled.Store(true)
		select {
		case ack <- false:
		default:
		}
		return "", nil, err
	}
	return id, ack, nil
}

func (g *Gate) lookupControl(id string) (*controlAck, bool) {
	if g == nil || id == "" {
		return nil, false
	}
	g.ackMu.Lock()
	ack := g.acks[id]
	canceled := ack != nil && ack.canceled.Load()
	g.ackMu.Unlock()
	return ack, canceled
}

// ControlState reports whether id is a live control reservation and whether
// that reservation has been canceled. The acknowledgement object remains
// private to this package so callers cannot mutate admission state directly.
func (g *Gate) ControlState(id string) (present, canceled bool) {
	ack, canceled := g.lookupControl(id)
	return ack != nil, canceled
}

// BeginControl waits for the reservation to reach the provider ingress turn.
// The returned release function must be called after the provider send.
func (g *Gate) BeginControl(ctx context.Context, id string) (func(), error) {
	ack, _ := g.lookupControl(id)
	return g.beginControl(ctx, id, ack)
}

// IsControlID recognizes an admission marker that belongs to this gate.
// Markers are removed before a provider receives the message.
func IsControlID(id string) bool {
	return strings.HasPrefix(id, liveControlAckPrefix)
}

func (g *Gate) Acknowledge(id string, accepted bool) {
	if g == nil || id == "" {
		return
	}
	g.ackMu.Lock()
	ack, ok := g.acks[id]
	canceled := ok && ack.canceled.Load()
	if ok {
		delete(g.acks, id)
	}
	g.ackMu.Unlock()
	if ok && !canceled {
		ack.accepted <- accepted
	}
}

func (g *Gate) CancelAck(id string) {
	if g == nil || id == "" {
		return
	}
	g.ackMu.Lock()
	ack, ok := g.acks[id]
	if ok {
		ack.canceled.Store(true)
	}
	g.ackMu.Unlock()
	if ok {
		g.cancelControl(id)
		select {
		case ack.accepted <- false:
		default:
		}
	}
}

// abortAck releases a control admission that was never delivered to the
// provider (for example, because AgentLoop rejected the event). Unlike
// cancelAck it removes the marker immediately because no late provider call
// can consume it.
func (g *Gate) AbortAck(id string) {
	if g == nil || id == "" {
		return
	}
	g.ackMu.Lock()
	ack, ok := g.acks[id]
	if ok {
		delete(g.acks, id)
		ack.canceled.Store(true)
	}
	g.ackMu.Unlock()
	if ok {
		g.cancelControl(id)
		select {
		case ack.accepted <- false:
		default:
		}
	}
}

func (g *Gate) cancelAcks() {
	if g == nil {
		return
	}
	g.ackMu.Lock()
	acks := make([]*controlAck, 0, len(g.acks))
	ids := make([]string, 0, len(g.acks))
	for id, ack := range g.acks {
		delete(g.acks, id)
		ack.canceled.Store(true)
		ids = append(ids, id)
		acks = append(acks, ack)
	}
	g.ackMu.Unlock()
	for index, ack := range acks {
		g.cancelControl(ids[index])
		select {
		case ack.accepted <- false:
		default:
		}
	}
}
