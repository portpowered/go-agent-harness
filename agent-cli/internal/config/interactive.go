package config

import (
	"fmt"
	"time"
)

const (
	// DefaultInteractiveFastReadTimeout is the default deadline for a
	// read-shaped tool in a voice or realtime session.
	DefaultInteractiveFastReadTimeout = 5 * time.Second
	// DefaultInteractiveLongRunningTimeout bounds an admitted long-running
	// operation while leaving time for a grounded continuation before the
	// session's 30-second silence budget is exhausted.
	DefaultInteractiveLongRunningTimeout = 20 * time.Second
	// DefaultInteractiveAcknowledgementThreshold is when a pending
	// long-running operation becomes eligible for a spoken acknowledgement.
	DefaultInteractiveAcknowledgementThreshold = 2 * time.Second
	// InteractiveFastReadTimeoutLimit is exclusive: fast/read values must be
	// positive and strictly below ten seconds.
	InteractiveFastReadTimeoutLimit = 10 * time.Second
	// InteractiveLongRunningTimeoutLimit is exclusive so a bounded operation
	// cannot consume the complete 30-second no-acknowledgement budget.
	InteractiveLongRunningTimeoutLimit = 30 * time.Second
	// InteractiveAcknowledgementThresholdLimit is inclusive. Keeping this
	// threshold at or below two seconds preserves the voice patience contract.
	InteractiveAcknowledgementThresholdLimit = 2 * time.Second
)

// InteractiveToolConfig contains the latency policy for voice/realtime tool
// calls. It is deliberately separate from batch tool settings, including the
// existing cron and command execution configuration.
type InteractiveToolConfig struct {
	FastReadTimeout          time.Duration `koanf:"fast_read_timeout" yaml:"fast_read_timeout"`
	LongRunningTimeout       time.Duration `koanf:"long_running_timeout" yaml:"long_running_timeout"`
	AcknowledgementThreshold time.Duration `koanf:"acknowledgement_threshold" yaml:"acknowledgement_threshold"`
}

// InteractiveToolsConfig is retained as a descriptive plural alias for
// callers that refer to the surrounding tools configuration by name.
type InteractiveToolsConfig = InteractiveToolConfig

// DefaultInteractiveToolConfig returns the complete voice/realtime timeout
// policy. Returning a value keeps defaults independent across sessions.
func DefaultInteractiveToolConfig() InteractiveToolConfig {
	return InteractiveToolConfig{
		FastReadTimeout:          DefaultInteractiveFastReadTimeout,
		LongRunningTimeout:       DefaultInteractiveLongRunningTimeout,
		AcknowledgementThreshold: DefaultInteractiveAcknowledgementThreshold,
	}
}

// Validate checks the complete, resolved interactive policy. ConfigStorage
// fills absent values from DefaultInteractiveToolConfig before this method is
// called, so a zero value here represents an invalid explicit setting rather
// than an omitted field.
func (c InteractiveToolConfig) Validate() error {
	if c.FastReadTimeout <= 0 || c.FastReadTimeout >= InteractiveFastReadTimeoutLimit {
		return fmt.Errorf("tools.interactive.fast_read_timeout must be positive and less than 10s; got %s", c.FastReadTimeout)
	}
	if c.LongRunningTimeout <= 0 || c.LongRunningTimeout >= InteractiveLongRunningTimeoutLimit {
		return fmt.Errorf("tools.interactive.long_running_timeout must be positive and less than 30s; got %s", c.LongRunningTimeout)
	}
	if c.AcknowledgementThreshold <= 0 || c.AcknowledgementThreshold > InteractiveAcknowledgementThresholdLimit {
		return fmt.Errorf("tools.interactive.acknowledgement_threshold must be positive and no greater than 2s; got %s", c.AcknowledgementThreshold)
	}
	if c.LongRunningTimeout <= c.AcknowledgementThreshold {
		return fmt.Errorf("tools.interactive.long_running_timeout must exceed tools.interactive.acknowledgement_threshold; got %s and %s", c.LongRunningTimeout, c.AcknowledgementThreshold)
	}
	return nil
}

// ResolveInteractiveToolConfig returns the validated policy for a config
// snapshot. A completely zero programmatic Config is treated as omitted and
// receives the documented defaults; once any value is supplied, all values
// must be valid and are not silently repaired.
func (c Config) ResolveInteractiveToolConfig() (InteractiveToolConfig, error) {
	policy := c.Tools.Interactive
	if policy == (InteractiveToolConfig{}) {
		policy = DefaultInteractiveToolConfig()
	}
	if err := policy.Validate(); err != nil {
		return InteractiveToolConfig{}, err
	}
	return policy, nil
}

// ValidateInteractive checks the interactive policy without validating the
// selected model. Session callers use this before provider construction.
func (c Config) ValidateInteractive() error {
	_, err := c.ResolveInteractiveToolConfig()
	return err
}
