package providerliveness

import "context"

// FailureBridge adapts a stable service wake to the host loop's error channel.
// It is a value type so the public contract exposes no package-owned state.
type FailureBridge struct {
	Events  <-chan struct{}
	Failure func() error
}

func (b FailureBridge) Errors(ctx context.Context) <-chan error {
	if b.Events == nil || b.Failure == nil {
		return nil
	}
	out := make(chan error, 1)
	go func() {
		defer close(out)
		select {
		case <-b.Events:
			if err := b.Failure(); err != nil {
				out <- err
			}
		case <-ctx.Done():
		}
	}()
	return out
}

// ErrorChannels joins two bounded host-loop error channels.
type ErrorChannels struct {
	First  <-chan error
	Second <-chan error
}

func (c ErrorChannels) Merge(ctx context.Context) <-chan error {
	if c.First == nil {
		return c.Second
	}
	if c.Second == nil {
		return c.First
	}
	return mergeErrorChannels(ctx, c.First, c.Second)
}

type mergeResult struct {
	err    error
	closed bool
}

func sendMergeResult(ctx context.Context, results chan<- mergeResult, result mergeResult) bool {
	select {
	case results <- result:
		return true
	case <-ctx.Done():
		return false
	}
}

func forwardError(ctx context.Context, source <-chan error, results chan<- mergeResult) {
	for {
		select {
		case err, ok := <-source:
			if !ok {
				sendMergeResult(ctx, results, mergeResult{closed: true})
				return
			}
			if err == nil {
				continue
			}
			if !sendMergeResult(ctx, results, mergeResult{err: err}) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func mergeErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	mergeContext, cancel := context.WithCancel(ctx)
	results := make(chan mergeResult, 2)
	go forwardError(mergeContext, first, results)
	go forwardError(mergeContext, second, results)
	out := make(chan error, 1)
	go func() {
		defer close(out)
		defer cancel()
		open := 2
		for open > 0 {
			select {
			case result := <-results:
				if result.closed {
					open--
					continue
				}
				select {
				case out <- result.err:
				case <-ctx.Done():
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
