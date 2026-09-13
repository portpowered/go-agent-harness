// Command terminal-policy-consumer exercises the public provider terminal
// policy from an independent Go module without importing agent-cli.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	providerswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers/wire"
)

type report struct {
	Status             string `json:"status"`
	NormalizedStatus   string `json:"normalized_status"`
	PositiveEligible   bool   `json:"positive_eligible"`
	PositiveDelay      string `json:"positive_delay"`
	LegacyDelay        string `json:"legacy_delay"`
	LegacyMessageBytes int    `json:"legacy_message_bytes"`
	Cancellation       bool   `json:"cancellation_eligible"`
	WrongCode          bool   `json:"wrong_code_eligible"`
	InvalidDelay       string `json:"invalid_delay"`
	TinyDelay          string `json:"tiny_delay"`
	CappedDelay        string `json:"capped_delay"`
	NilEligible        bool   `json:"nil_eligible"`
}

func main() {
	wrongOracle := len(os.Args) == 2 && os.Args[1] == "wrong-oracle"
	if len(os.Args) > 1 && !wrongOracle {
		fail(fmt.Errorf("unknown argument %q", os.Args[1]))
	}

	policy := providerswire.NewTerminalPolicy()
	positive := &messages.MessageEndValue{
		Status:               " FAILED ",
		ProviderErrorCode:    providers.RateLimitRetryCode,
		ProviderErrorMessage: "Please try again in 1.668s.",
	}
	positiveDelay, positiveEligible := policy.RateLimitRetryDecision(positive)
	if wrongOracle {
		if positiveDelay != 1667*time.Millisecond {
			fail(fmt.Errorf("wrong-oracle assertion: positive delay=%s expected=1.667s", positiveDelay))
		}
		return
	}

	legacyMessage := "Please try again in 1.5s, preserve this comma-bearing provider context."
	legacy := &messages.MessageEndValue{
		Status:        "failed",
		StatusDetails: "reason=error, code=rate_limit_exceeded, message=" + legacyMessage,
	}
	legacyDelay, legacyEligible := policy.RateLimitRetryDecision(legacy)
	if !positiveEligible || positiveDelay != 1668*time.Millisecond {
		fail(fmt.Errorf("positive policy result=(%s,%t)", positiveDelay, positiveEligible))
	}
	if !legacyEligible || legacyDelay != 1500*time.Millisecond {
		fail(fmt.Errorf("legacy policy result=(%s,%t)", legacyDelay, legacyEligible))
	}
	if got := policy.ProviderTerminalErrorMessage(legacy); got != legacyMessage {
		fail(fmt.Errorf("legacy message=%q want=%q", got, legacyMessage))
	}
	if got := policy.ProviderTerminalErrorCode(legacy); got != providers.RateLimitRetryCode {
		fail(fmt.Errorf("legacy code=%q want=%q", got, providers.RateLimitRetryCode))
	}

	explicit := &messages.MessageEndValue{
		Status:               "failed",
		StatusDetails:        "code=wrong, message=Please try again in 45s",
		ProviderErrorCode:    " " + providers.RateLimitRetryCode + " ",
		ProviderErrorMessage: " Please try again in 0.25s ",
	}
	if got := policy.ProviderTerminalErrorCode(explicit); got != providers.RateLimitRetryCode {
		fail(fmt.Errorf("explicit code precedence=%q", got))
	}
	if got := policy.ProviderTerminalErrorMessage(explicit); got != "Please try again in 0.25s" {
		fail(fmt.Errorf("explicit message precedence=%q", got))
	}

	cancellation := *positive
	cancellation.TerminalReason = messages.TerminalReasonCancellation
	_, cancellationEligible := policy.RateLimitRetryDecision(&cancellation)
	wrongCode := *positive
	wrongCode.ProviderErrorCode = "RATE_LIMIT_EXCEEDED"
	_, wrongCodeEligible := policy.RateLimitRetryDecision(&wrongCode)
	if cancellationEligible || wrongCodeEligible {
		fail(fmt.Errorf("negative eligibility cancellation=%t wrong-code=%t", cancellationEligible, wrongCodeEligible))
	}

	invalidDelay := policy.ParseRateLimitRetryDelay("provider did not include retry guidance")
	tinyDelay := policy.ParseRateLimitRetryDelay("Please try again in 0.0000000004s")
	cappedDelay := policy.ParseRateLimitRetryDelay("Please try again in 45s")
	if invalidDelay != providers.DefaultRateLimitRetryDelay || tinyDelay != time.Nanosecond || cappedDelay != providers.MaxRateLimitRetryDelay {
		fail(fmt.Errorf("delay bounds invalid=%s tiny=%s capped=%s", invalidDelay, tinyDelay, cappedDelay))
	}
	_, nilEligible := policy.RateLimitRetryDecision(nil)
	if nilEligible {
		fail(fmt.Errorf("nil terminal was eligible"))
	}

	bounded := strings.Repeat("x", providers.MaxLegacyStatusDetailBytes+19)
	if got := policy.LegacyStatusDetailField("message="+bounded, "message"); len(got) != providers.MaxLegacyStatusDetailBytes {
		fail(fmt.Errorf("legacy field length=%d want=%d", len(got), providers.MaxLegacyStatusDetailBytes))
	}

	result := report{
		Status:             "ok",
		NormalizedStatus:   policy.NormalizeTerminalStatus("  FaIlEd "),
		PositiveEligible:   positiveEligible,
		PositiveDelay:      positiveDelay.String(),
		LegacyDelay:        legacyDelay.String(),
		LegacyMessageBytes: len(policy.ProviderTerminalErrorMessage(legacy)),
		Cancellation:       cancellationEligible,
		WrongCode:          wrongCodeEligible,
		InvalidDelay:       invalidDelay.String(),
		TinyDelay:          tinyDelay.String(),
		CappedDelay:        cappedDelay.String(),
		NilEligible:        nilEligible,
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
