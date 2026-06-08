package providers

import "fmt"

// UnsupportedFeatureError reports a deterministic provider capability mismatch
// that was rejected locally before provider execution.
type UnsupportedFeatureError struct {
	Provider   string
	Feature    string
	Mode       string
	Capability Capability
}

func (e *UnsupportedFeatureError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Capability.Rationale == "" {
		return fmt.Sprintf("%s %s feature %q is %s", e.Provider, e.Mode, e.Feature, e.Capability.State)
	}
	return fmt.Sprintf("%s %s feature %q is %s: %s", e.Provider, e.Mode, e.Feature, e.Capability.State, e.Capability.Rationale)
}
