package contract

import "context"

// ContextOrBackground returns ctx, or an uncancelled root context when ctx is
// nil. Public audio and device operations document that a nil context means
// "never cancelled"; this is the single place that contract is honoured.
func ContextOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background() //nolint:forbidigo // the documented nil-context contract of public audio APIs needs an uncancelled root; callers that need cancellation pass their own context
}
