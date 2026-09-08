package webmcp

import "context"

func (b *StatefulBroker) AcquirePageFocus(ctx context.Context) (func(context.Context) error, error) {
	var release func(context.Context) error
	err := b.withSelectedSession(ctx, "acquire_page_focus", func(session TargetSession) error {
		controller, ok := session.(PageFocusLeaser)
		if !ok {
			return classified(ErrorBrowserProtocol, "selected page does not support bounded focus", map[string]any{"reason_code": "unsupported_operation"}, nil)
		}
		var err error
		release, err = controller.AcquirePageFocus(ctx)
		return err
	})
	// If selection changed after acquisition, the caller still owns cleanup.
	return release, err
}

var _ PageFocusLeaser = (*StatefulBroker)(nil)
