package runner

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func startAudioInterruptionPump(ctx context.Context, port sessionduration.AudioInterruptionPort) (<-chan audioio.ScheduledAudioInput, <-chan struct{}, chan struct{}) {
	if port.Source == nil || port.Dispatch == nil {
		return nil, nil, nil
	}
	pending := make(chan audioio.ScheduledAudioInput, 1)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go forwardInterruptions(ctx, port.Source, pending, wake, done)
	return pending, wake, done
}

func forwardInterruptions(ctx context.Context, source <-chan audioio.ScheduledAudioInput, pending chan<- audioio.ScheduledAudioInput, wake chan<- struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case input, ok := <-source:
			if !ok || !queueInterruption(ctx, input, pending, wake) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func queueInterruption(ctx context.Context, input audioio.ScheduledAudioInput, pending chan<- audioio.ScheduledAudioInput, wake chan<- struct{}) bool {
	select {
	case pending <- input:
		select {
		case wake <- struct{}{}:
		default:
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func mergeRunErrors(ctx context.Context, first <-chan error, rest ...<-chan error) <-chan error {
	sources := errorSources(first, rest)
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	merged := make(chan error, len(sources))
	for _, source := range sources {
		go forwardErrors(ctx, source, merged)
	}
	return merged
}

func errorSources(first <-chan error, rest []<-chan error) []<-chan error {
	sources := make([]<-chan error, 0, len(rest)+1)
	if first != nil {
		sources = append(sources, first)
	}
	for _, source := range rest {
		if source != nil {
			sources = append(sources, source)
		}
	}
	return sources
}

func forwardErrors(ctx context.Context, source <-chan error, merged chan<- error) {
	for {
		err, ok, stopped := nextError(ctx, source)
		if stopped || !ok {
			return
		}
		if err == nil {
			continue
		}
		select {
		case merged <- err:
		case <-ctx.Done():
			return
		}
	}
}

func nextError(ctx context.Context, source <-chan error) (error, bool, bool) {
	select {
	case err, ok := <-source:
		return err, ok, false
	case <-ctx.Done():
		return nil, false, true
	}
}

func mergeRunWakes(ctx context.Context, first <-chan struct{}, rest ...<-chan struct{}) <-chan struct{} {
	sources := wakeSources(first, rest)
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	wake := make(chan struct{}, 1)
	for _, source := range sources {
		go forwardWakes(ctx, source, wake)
	}
	return wake
}

func wakeSources(first <-chan struct{}, rest []<-chan struct{}) []<-chan struct{} {
	sources := make([]<-chan struct{}, 0, len(rest)+1)
	if first != nil {
		sources = append(sources, first)
	}
	for _, source := range rest {
		if source != nil {
			sources = append(sources, source)
		}
	}
	return sources
}

func forwardWakes(ctx context.Context, source <-chan struct{}, wake chan<- struct{}) {
	for {
		select {
		case _, ok := <-source:
			if !ok {
				return
			}
			select {
			case wake <- struct{}{}:
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func mergeRunDone(ctx context.Context, first <-chan struct{}, rest ...<-chan struct{}) <-chan struct{} {
	sources := wakeSources(first, rest)
	if len(sources) == 0 {
		return nil
	}
	if len(sources) == 1 {
		return sources[0]
	}
	done := make(chan struct{})
	var once sync.Once
	for _, source := range sources {
		go func(source <-chan struct{}) {
			select {
			case <-source:
				once.Do(func() { close(done) })
			case <-ctx.Done():
			}
		}(source)
	}
	return done
}
