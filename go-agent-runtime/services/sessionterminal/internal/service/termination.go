package service

import (
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

type terminationBoundary struct {
	options sessionterminal.TerminationOptions
	once    sync.Once
	result  error
}

func NewTerminationBoundary(options sessionterminal.TerminationOptions) sessionterminal.TerminationBoundary {
	return &terminationBoundary{options: options}
}

func (b *terminationBoundary) Terminate(primary error) error {
	if b == nil {
		return primary
	}
	b.once.Do(func() {
		var quiesceErr, waitErr, stopErr, flushErr error
		if b.options.QuiesceUpstream != nil {
			quiesceErr = b.options.QuiesceUpstream()
		}
		if b.options.WaitForStragglers == nil {
			waitErr = errors.New("session termination requires a straggler drain")
		} else {
			waitErr = b.options.WaitForStragglers()
		}
		if b.options.StopOwnedResources != nil {
			stopErr = b.options.StopOwnedResources()
		}
		if b.options.FlushBuffered != nil {
			flushErr = b.options.FlushBuffered()
		}
		var contextErr error
		if b.options.Context != nil {
			contextErr = b.options.Context.Err()
		}
		b.result = errors.Join(primary, quiesceErr, waitErr, stopErr, flushErr, contextErr)
	})
	return b.result
}
