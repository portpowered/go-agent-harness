package chrome

import (
	"context"
	"errors"
)

// PinnedChromeAcquirerFunc adapts a function to PinnedChromeAcquirer.
type PinnedChromeAcquirerFunc func(context.Context, PinnedChromeRequest) (ChromeExecutable, error)

// AcquirePinnedChrome implements PinnedChromeAcquirer.
func (f PinnedChromeAcquirerFunc) AcquirePinnedChrome(ctx context.Context, request PinnedChromeRequest) (ChromeExecutable, error) {
	if f == nil {
		return ChromeExecutable{}, errors.New("pinned Chrome acquirer is nil")
	}
	return f(ctx, request)
}
