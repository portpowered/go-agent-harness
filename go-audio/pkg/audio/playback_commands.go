package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrUnsupportedPlaybackCommand identifies a control kind with no equivalent
// operation in the playback worker queue.
var ErrUnsupportedPlaybackCommand = errors.New("unsupported playback command")

// ErrStalePlaybackCommand identifies an admitted interrupt superseded by a
// newer playback generation before the worker applied it.
var ErrStalePlaybackCommand = errors.New("stale playback command")

// PlaybackOperation is a control message, independent of the PCM data queue.
type PlaybackOperation uint8

const (
	PlaybackStart PlaybackOperation = iota + 1
	PlaybackInterrupt
	PlaybackInterruptActive
	PlaybackDiscard
	PlaybackResume
)

// PlaybackReceipt records completion at the device worker, rather than treating
// successful command admission as proof that a device applied the operation.
type PlaybackReceipt struct {
	CommandID    uint64
	Epoch        uint64
	Interruption PlaybackInterruption
	Applied      bool
	Err          error
}

// PlaybackReceiptObserver receives the worker's applied result for every
// admitted playback request, including stale or failed requests. It runs once
// after the request's optional waiter is notified and should return promptly.
type PlaybackReceiptObserver func(PlaybackReceipt)

type PlaybackRequest struct {
	ID        uint64
	Epoch     uint64
	Operation PlaybackOperation
	Response  PlaybackResponse
	reply     chan PlaybackReceipt
	observer  PlaybackReceiptObserver
	once      sync.Once
}

func (r *PlaybackRequest) Complete(receipt PlaybackReceipt) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		receipt.CommandID = r.ID
		receipt.Epoch = r.Epoch
		if r.reply != nil {
			r.reply <- receipt
		}
		if r.observer != nil {
			r.observer(receipt)
		}
	})
}

// PlaybackCommands is a bounded in-memory control port. It owns no device and
// executes no callback. The runtime must consume requests on a separate worker.
// Cancellation always releases a waiting sender, even when rendering stalls.
type PlaybackCommands struct {
	requests        chan *PlaybackRequest
	done            chan struct{}
	once            sync.Once
	sequence        atomic.Uint64
	observerMu      sync.RWMutex
	receiptObserver PlaybackReceiptObserver
}

func NewPlaybackCommands(capacity int) (*PlaybackCommands, error) {
	if capacity <= 0 {
		return nil, errors.New("playback command capacity must be positive")
	}
	return &PlaybackCommands{requests: make(chan *PlaybackRequest, capacity), done: make(chan struct{})}, nil
}

func (q *PlaybackCommands) Exchange(ctx context.Context, operation PlaybackOperation, response PlaybackResponse) PlaybackReceipt {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PlaybackReceipt{Err: err}
	}
	req := &PlaybackRequest{ID: q.sequence.Add(1), Operation: operation, Response: response, reply: make(chan PlaybackReceipt, 1), observer: q.receiptObserverSnapshot()}
	select {
	case <-q.done:
		return PlaybackReceipt{CommandID: req.ID, Err: ErrClosed}
	default:
	}
	select {
	case q.requests <- req:
	case <-ctx.Done():
		return PlaybackReceipt{CommandID: req.ID, Err: ctx.Err()}
	case <-q.done:
		return PlaybackReceipt{CommandID: req.ID, Err: ErrClosed}
	}
	select {
	case receipt := <-req.reply:
		return receipt
	case <-ctx.Done():
		return PlaybackReceipt{CommandID: req.ID, Err: ctx.Err()}
	case <-q.done:
		return PlaybackReceipt{CommandID: req.ID, Err: ErrClosed}
	}
}

// TrySubmit admits an interrupt command to the bounded playback control
// queue without waiting for the device worker's receipt. This is the bridge
// used by the loop audio subsystem: admission is local and nonblocking, while
// the worker remains the only code allowed to mutate device playback.
//
// The caller's command identity and epoch travel with the queued request, so
// the worker can reject stale interrupts against the current playback
// generation. A drain has no equivalent operation at this boundary and is rejected
// explicitly. Callers must retain it for a runtime that can provide a drain
// receipt rather than silently turning it into a discard.
func (q *PlaybackCommands) TrySubmit(command Command) error {
	_, err := q.trySubmit(command, false)
	return err
}

// TrySubmitWithReceipt is the observable form of TrySubmit. The command is
// still admitted without waiting for the worker; the returned channel receives
// exactly one applied or stale receipt after the worker handles it.
func (q *PlaybackCommands) TrySubmitWithReceipt(command Command) (<-chan PlaybackReceipt, error) {
	return q.trySubmit(command, true)
}

func (q *PlaybackCommands) trySubmit(command Command, wantReceipt bool) (<-chan PlaybackReceipt, error) {
	if q == nil {
		return nil, ErrClosed
	}
	var operation PlaybackOperation
	switch command.Kind {
	case CommandInterrupt:
		operation = PlaybackDiscard
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPlaybackCommand, command.Kind)
	}
	request := &PlaybackRequest{
		ID:        command.ID,
		Epoch:     command.Epoch,
		Operation: operation,
		observer:  q.receiptObserverSnapshot(),
	}
	if wantReceipt {
		request.reply = make(chan PlaybackReceipt, 1)
	}
	if request.ID == 0 {
		request.ID = q.sequence.Add(1)
	}
	select {
	case <-q.done:
		return nil, ErrClosed
	default:
	}
	select {
	case q.requests <- request:
		return request.reply, nil
	default:
		return nil, ErrControlFull
	}
}

// SetReceiptObserver installs the runtime observation sink for subsequent
// requests. Existing requests retain the observer captured at admission so a
// receipt is delivered exactly once to the owner that accepted the command.
func (q *PlaybackCommands) SetReceiptObserver(observer PlaybackReceiptObserver) {
	if q == nil {
		return
	}
	q.observerMu.Lock()
	q.receiptObserver = observer
	q.observerMu.Unlock()
}

func (q *PlaybackCommands) receiptObserverSnapshot() PlaybackReceiptObserver {
	if q == nil {
		return nil
	}
	q.observerMu.RLock()
	observer := q.receiptObserver
	q.observerMu.RUnlock()
	return observer
}

func (q *PlaybackCommands) Receive(ctx context.Context) (*PlaybackRequest, error) {
	select {
	case <-q.done:
		return nil, ErrClosed
	default:
	}
	select {
	case req := <-q.requests:
		return req, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-q.done:
		return nil, ErrClosed
	}
}
func (q *PlaybackCommands) Close() { q.once.Do(func() { close(q.done) }) }

// BufferedPlaybackController adapts the provider's synchronous truncation
// protocol to explicit queued commands and applied receipts. Its only
// capability is the memory control port; physical I/O stays with its consumer.
type BufferedPlaybackController struct {
	Context  context.Context
	Commands *PlaybackCommands
}

func (c BufferedPlaybackController) StartPlayback(response PlaybackResponse) {
	c.Commands.Exchange(c.Context, PlaybackStart, response)
}
func (c BufferedPlaybackController) InterruptPlayback(response PlaybackResponse) (int, bool) {
	r := c.Commands.Exchange(c.Context, PlaybackInterrupt, response)
	return r.Interruption.AudioEndMS, r.Applied && r.Err == nil
}
func (c BufferedPlaybackController) InterruptActivePlayback() (PlaybackInterruption, bool) {
	r := c.Commands.Exchange(c.Context, PlaybackInterruptActive, PlaybackResponse{})
	return r.Interruption, r.Applied && r.Err == nil
}
