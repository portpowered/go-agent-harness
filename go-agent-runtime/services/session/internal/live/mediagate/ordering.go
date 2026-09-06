package mediagate

import (
	"context"
	"errors"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// flush waits until every PCM frame admitted through the outbound endpoint
// has completed its provider write. New controls use the admission sequence
// directly, so callers that need a position relative to concurrently arriving
// media should register their control before flushing.
func (g *Gate) Flush(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		return mediaContextRequired()
	}
	return g.outbound.waitDrained(ctx)
}

func (g *Gate) isClosed() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	return closed
}

func (g *Gate) notifyOrderLocked() {
	close(g.orderChanged)
	g.orderChanged = make(chan struct{})
}

func (g *Gate) registerControl(id string) error {
	if g == nil || id == "" {
		return ErrMediaUnavailable
	}
	g.orderMu.Lock()
	defer g.orderMu.Unlock()
	if g.isClosed() {
		return sharedaudio.ErrSessionMediaClosed
	}
	if g.controls == nil {
		g.controls = make(map[string]uint64)
	}
	g.controls[id] = g.mediaNext
	g.notifyOrderLocked()
	return nil
}

func (g *Gate) cancelControl(id string) {
	if g == nil || id == "" {
		return
	}
	g.orderMu.Lock()
	if g.activeControl != id {
		if _, ok := g.controls[id]; ok {
			delete(g.controls, id)
			g.notifyOrderLocked()
		}
	}
	g.orderMu.Unlock()
}

func (g *Gate) releaseControl(id string) {
	if g == nil || id == "" {
		return
	}
	g.orderMu.Lock()
	if g.activeControl == id {
		g.active = false
		g.activeControl = ""
	}
	delete(g.controls, id)
	g.notifyOrderLocked()
	g.orderMu.Unlock()
}

func (g *Gate) beginControl(ctx context.Context, id string, ack *controlAck) (func(), error) {
	if g == nil {
		return nil, ErrMediaUnavailable
	}
	if ctx == nil {
		return nil, mediaContextRequired()
	}
	if ack == nil {
		return nil, sharedaudio.ErrSessionMediaClosed
	}
	for {
		if ack.canceled.Load() {
			g.cancelControl(id)
			return nil, context.Canceled
		}
		changed, err := g.tryControl(id, ack)
		if err != nil {
			g.cancelControl(id)
			return nil, err
		}
		if changed == nil {
			return func() { g.releaseControl(id) }, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			g.cancelControl(id)
			return nil, ctx.Err()
		}
	}
}

// tryControl checks the barrier and reserves its exclusive slot atomically.
// A nil change signal means ownership was acquired; callers otherwise wait.
func (g *Gate) tryControl(id string, ack *controlAck) (<-chan struct{}, error) {
	g.orderMu.Lock()
	defer g.orderMu.Unlock()
	if g.isClosed() {
		return nil, sharedaudio.ErrSessionMediaClosed
	}
	barrier, ok := g.controls[id]
	if !ok || ack.canceled.Load() {
		return nil, context.Canceled
	}
	if !g.active && g.mediaDone >= barrier {
		g.active = true
		g.activeControl = id
		return nil, nil
	}
	return g.orderChanged, nil
}

// beginAutomatic admits provider traffic that was already queued in the
// model runner before a host control reached that runner. It may pass pending
// controls, but remains exclusive with active media/control operations. This
// rule breaks the same-runner cycle where a queued automatic send would
// otherwise wait for a control event that cannot be dispatched until it
// returns.
func (g *Gate) BeginAutomatic(ctx context.Context) (func(), error) {
	return g.beginExclusive(ctx, "")
}

func (g *Gate) beginExclusive(ctx context.Context, controlID string) (func(), error) {
	if g == nil {
		return nil, ErrMediaUnavailable
	}
	if ctx == nil {
		return nil, mediaContextRequired()
	}
	for {
		g.orderMu.Lock()
		if g.isClosed() {
			g.orderMu.Unlock()
			return nil, sharedaudio.ErrSessionMediaClosed
		}
		if !g.active {
			g.active = true
			g.activeControl = controlID
			g.orderMu.Unlock()
			return func() { g.releaseExclusive(controlID) }, nil
		}
		changed := g.orderChanged
		g.orderMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (g *Gate) releaseExclusive(controlID string) {
	if g == nil {
		return
	}
	g.orderMu.Lock()
	if g.active && g.activeControl == controlID {
		g.active = false
		g.activeControl = ""
		g.notifyOrderLocked()
	}
	g.orderMu.Unlock()
}

func (g *Gate) AdmitMedia() (uint64, error) {
	if g == nil {
		return 0, ErrMediaUnavailable
	}
	g.orderMu.Lock()
	defer g.orderMu.Unlock()
	if g.isClosed() {
		return 0, sharedaudio.ErrSessionMediaClosed
	}
	g.mediaNext++
	return g.mediaNext, nil
}

func (g *Gate) markMediaDone(id uint64) {
	if g == nil || id == 0 {
		return
	}
	g.orderMu.Lock()
	if id > g.mediaDone {
		g.mediaFinished[id] = struct{}{}
		for {
			next := g.mediaDone + 1
			if _, ok := g.mediaFinished[next]; !ok {
				break
			}
			delete(g.mediaFinished, next)
			g.mediaDone = next
		}
		g.notifyOrderLocked()
	}
	g.orderMu.Unlock()
}

func (g *Gate) beginMedia(ctx context.Context, id uint64) (func(), error) {
	if g == nil {
		return nil, ErrMediaUnavailable
	}
	if ctx == nil {
		return nil, mediaContextRequired()
	}
	for {
		g.orderMu.Lock()
		if g.isClosed() {
			g.orderMu.Unlock()
			g.markMediaDone(id)
			return nil, sharedaudio.ErrSessionMediaClosed
		}
		allowed := true
		for _, barrier := range g.controls {
			if id > barrier {
				allowed = false
				break
			}
		}
		if !g.active && allowed {
			g.active = true
			g.activeControl = ""
			g.orderMu.Unlock()
			return func() {
				g.releaseMedia(id)
			}, nil
		}
		changed := g.orderChanged
		g.orderMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			g.markMediaDone(id)
			return nil, ctx.Err()
		}
	}
}

func (g *Gate) releaseMedia(id uint64) {
	if g == nil {
		return
	}
	g.orderMu.Lock()
	if g.active && g.activeControl == "" {
		g.active = false
		g.notifyOrderLocked()
	}
	g.orderMu.Unlock()
	g.markMediaDone(id)
}

func closeMediaEndpoints(endpoints sharedaudio.MediaEndpoints) error {
	var closeErr error
	if endpoints.Inbound != nil {
		closeErr = errors.Join(closeErr, endpoints.Inbound.Close())
	}
	if endpoints.Outbound != nil {
		closeErr = errors.Join(closeErr, endpoints.Outbound.Close())
	}
	return closeErr
}
