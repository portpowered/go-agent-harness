package probe

import "fmt"

// Registered scenario for the v6c rate-limit error-path vertical. The
// throttled case requires the session to terminate with a rate-limit-
// classified error (surfaced by the replay exec seam as the terminal reason
// "error:<classification>"); it runs offline over the recorded fixture
// selected by scenario name or ID.
//
// Negative controls (documented contract): feeding either comparable
// non-throttle capture — the invalid_api_key auth control or the
// invalid_request_error/bad_request invalid-request control — into this
// unchanged rate_limited expectation must FAIL, naming the observed
// classification beside the expected one. Both controls are exercised through
// the production CLI in agent-cli/test/integration/s2s_v6c_error_rate_limit_test.go.

const (
	// ScenarioIDS2SV6CErrorRateLimit selects the whole rate-limit vertical
	// suite; every registered case whose ID extends this prefix runs when it
	// is selected.
	ScenarioIDS2SV6CErrorRateLimit = "s2s-v6c-error-rate-limit"
	// ScenarioIDS2SV6CErrorRateLimitThrottled is the throttled case backed by
	// a recorded provider rate-limit fixture (error.type=rate_limit_error,
	// error.code=rate_limit_exceeded). The session must terminate with the
	// typed rate_limited classification instead of an auth, parse, or generic
	// provider-rejected classification.
	ScenarioIDS2SV6CErrorRateLimitThrottled = ScenarioIDS2SV6CErrorRateLimit + "-throttled"
)

func s2sv6CErrorRateLimitScenario() Scenario {
	return terminalReasonScenario(terminalReasonCase{
		id:          ScenarioIDS2SV6CErrorRateLimitThrottled,
		description: "Session receiving a provider rate-limit error must terminate with the typed rate_limited classification",
		reason:      "error:rate_limited",
	})
}

func registerS2SV6CErrorRateLimitScenario(register func(Scenario, ...DeadSessionControl) error) error {
	return register(s2sv6CErrorRateLimitScenario())
}

// NewBuiltinScenarioRegistry builds a fresh registry with every built-in
// scenario registered explicitly and in a fixed order. Each call returns an
// independent registry, so callers never share mutable scenario state.
func NewBuiltinScenarioRegistry() (*ScenarioRegistry, error) {
	registry := NewScenarioRegistry()
	for _, registerBuiltin := range []func(func(Scenario, ...DeadSessionControl) error) error{
		registerS2SV1TextInAudioOutScenario,
		registerS2SV3ABargeInBasicScenarios,
		registerS2SV3BBargeInToolResultScenarios,
		registerS2SV3CBargeInRepeatedScenarios,
		registerErrorAuthScenarios,
		registerErrorDisconnectScenarios,
		registerErrorMalformedResponseScenarios,
		registerS2SV6CErrorRateLimitScenario,
		registerS2SV7AMetricsModalityScenarios,
	} {
		if err := registerBuiltin(registry.Register); err != nil {
			return nil, fmt.Errorf("register built-in probe scenario: %w", err)
		}
	}
	return registry, nil
}

// Scenarios returns a snapshot of the built-in scenarios in registration
// order.
func Scenarios() ([]Scenario, error) {
	registry, err := NewBuiltinScenarioRegistry()
	if err != nil {
		return nil, err
	}
	return registry.Snapshot(), nil
}
