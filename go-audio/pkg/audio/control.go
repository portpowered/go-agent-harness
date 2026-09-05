package audio

import (
	"context"
	"errors"
	"io"
	"sync"
)

type CommandKind string

const (
	CommandInterrupt CommandKind = "interrupt"
	CommandDrain     CommandKind = "drain"
)

// Command identifies an operation, rather than a function to execute. It is
// safe to record/replay and never carries a device handle or callback.
type Command struct {
	ID    uint64
	Epoch uint64
	Kind  CommandKind
}

var ErrControlFull = errors.New("audio control buffer capacity exhausted")

// CommandProducer and CommandConsumer are concrete memory-only capabilities.
// Their separate queue means saturated media cannot consume control capacity.
type CommandProducer struct{ q *commandBuffer }
type CommandConsumer struct{ q *commandBuffer }
type commandBuffer struct {
	mu     sync.Mutex
	queue  chan Command
	closed bool
	done   chan struct{}
}

func NewCommandBuffer(capacity int) (CommandProducer, CommandConsumer, error) {
	if capacity <= 0 {
		return CommandProducer{}, CommandConsumer{}, ErrInvalidBuffer
	}
	q := &commandBuffer{queue: make(chan Command, capacity), done: make(chan struct{})}
	return CommandProducer{q}, CommandConsumer{q}, nil
}

func (p CommandProducer) TrySubmit(command Command) error {
	if p.q == nil {
		return ErrBufferClosed
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if p.q.closed {
		return ErrBufferClosed
	}
	select {
	case p.q.queue <- command:
		return nil
	default:
		return ErrControlFull
	}
}

func (c CommandConsumer) Receive(ctx context.Context) (Command, error) {
	if c.q == nil {
		return Command{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return Command{}, err
	}
	select {
	case cmd := <-c.q.queue:
		return cmd, nil
	default:
	}
	select {
	case cmd := <-c.q.queue:
		return cmd, nil
	case <-ctx.Done():
		return Command{}, ctx.Err()
	case <-c.q.done:
		select {
		case cmd := <-c.q.queue:
			return cmd, nil
		default:
			return Command{}, io.EOF
		}
	}
}

func (p CommandProducer) Close() {
	if p.q == nil {
		return
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if !p.q.closed {
		p.q.closed = true
		close(p.q.done)
	}
}
