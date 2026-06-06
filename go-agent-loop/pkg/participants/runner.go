package participants

import (
	"context"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// ParticipantRunner is implemented by active participants that run as background
// goroutines, reading from their inbox and writing results to their outbox.
type ParticipantRunner interface {
	// Run starts the participant's processing loop. It must return when ctx is cancelled.
	Run(ctx context.Context) error
}

// ActiveParticipant wraps a ParticipantRunner with lifecycle management.
type ActiveParticipant struct {
	id     messages.ParticipantID
	runner ParticipantRunner
	cancel context.CancelFunc
	done   chan struct{}
}

// NewActiveParticipant creates a participant backed by a runner goroutine.
func NewActiveParticipant(id messages.ParticipantID, runner ParticipantRunner) *ActiveParticipant {
	return &ActiveParticipant{
		id:     id,
		runner: runner,
	}
}

// ID returns the participant's identifier.
func (p *ActiveParticipant) ID() messages.ParticipantID { return p.id }

// Start launches the runner goroutine. The runner will be cancelled when Stop is called
// or when the parent context is cancelled.
func (p *ActiveParticipant) Start(ctx context.Context) {
	rctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})

	go func() {
		defer close(p.done)
		_ = p.runner.Run(rctx)
	}()
}

// Stop cancels the runner goroutine and waits for it to finish.
func (p *ActiveParticipant) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.done != nil {
		<-p.done
	}
}
