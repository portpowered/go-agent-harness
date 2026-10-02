package discovery

import "context"

// TargetCapabilityProbeFunc adapts a function to TargetCapabilityProbe.
type TargetCapabilityProbeFunc func(context.Context, BrowserCandidate, Target) (TargetCapabilities, error)

// Probe implements TargetCapabilityProbe.
func (f TargetCapabilityProbeFunc) Probe(ctx context.Context, browser BrowserCandidate, target Target) (TargetCapabilities, error) {
	if f == nil {
		return TargetCapabilities{}, nil
	}
	return f(ctx, browser, target)
}

// TargetListerFunc adapts a function to TargetLister.
type TargetListerFunc func(context.Context, BrowserCandidate) ([]TargetDescriptor, error)

// List implements TargetLister.
func (f TargetListerFunc) List(ctx context.Context, browser BrowserCandidate) ([]TargetDescriptor, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, browser)
}

// TargetAttacherFunc adapts an attach function to TargetAttacher.
type TargetAttacherFunc func(context.Context, BrowserCandidate, Target) (TargetDetacher, error)

// Attach implements TargetAttacher.
func (f TargetAttacherFunc) Attach(ctx context.Context, browser BrowserCandidate, target Target) (TargetDetacher, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, browser, target)
}

// ConfiguredSourceFunc adapts functions to ConfiguredSource.
type ConfiguredSourceFunc struct {
	SourceName  string
	ResolveFunc func(context.Context) (Endpoint, error)
}

// Name implements ConfiguredSource.
func (s ConfiguredSourceFunc) Name() string { return s.SourceName }

// Resolve implements ConfiguredSource.
func (s ConfiguredSourceFunc) Resolve(ctx context.Context) (Endpoint, error) {
	if s.ResolveFunc == nil {
		return Endpoint{}, nil
	}
	return s.ResolveFunc(ctx)
}

// StaticConfiguredSource is useful for resolved configuration and deterministic
// tests.
type StaticConfiguredSource struct {
	SourceName string
	Value      Endpoint
}

// Name implements ConfiguredSource.
func (s StaticConfiguredSource) Name() string { return s.SourceName }

// Resolve implements ConfiguredSource.
func (s StaticConfiguredSource) Resolve(context.Context) (Endpoint, error) {
	return s.Value, nil
}

// NewBrowserDisconnectedError constructs a safe marker for injected seams.
func NewBrowserDisconnectedError(browserID, targetID, phase string, cause error) error {
	return &BrowserDisconnectedError{
		BrowserID: browserID,
		TargetID:  targetID,
		Phase:     phase,
		Cause:     cause,
	}
}

// IsBrowserDisconnected reports whether an injected error represents loss of
// the browser connection. EOF and a closed network connection are included so
// simple neutral fakes need not import a browser websocket package.
func IsBrowserDisconnected(err error) bool {
	return isBrowserDisconnected(err)
}
