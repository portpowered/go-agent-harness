// Command retrypolicy-consumer exercises the public retry policy from a
// separate module without importing the CLI or any private implementation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy"
	retrypolicywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/retrypolicy/wire"
)

type report struct {
	Status     string        `json:"status"`
	Default    time.Duration `json:"default"`
	Capped     time.Duration `json:"capped"`
	Explicit   time.Duration `json:"explicit"`
	Ineligible bool          `json:"ineligible"`
}

func main() {
	service := retrypolicywire.NewService()
	if len(os.Args) > 1 && os.Args[1] == "wrong" {
		wrongExpectation(service)
		return
	}
	explicit := service.Decide(retrypolicy.Terminal{
		Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 0.25s",
	})
	defaulted := service.Decide(retrypolicy.Terminal{Status: "failed", ProviderErrorCode: "rate_limit_exceeded"})
	capped := service.Decide(retrypolicy.Terminal{
		Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 45s",
	})
	ineligible := service.Decide(retrypolicy.Terminal{
		Status: "failed", TerminalReason: "cancellation", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "Please try again in 1s",
	})
	if !explicit.Eligible || explicit.Delay != 250*time.Millisecond || !defaulted.Eligible || defaulted.Delay != 2*time.Second || !capped.Eligible || capped.Delay != 15*time.Second || ineligible.Eligible || ineligible.Delay != 0 {
		fail(fmt.Errorf("public decision oracle failed: explicit=%#v default=%#v capped=%#v ineligible=%#v", explicit, defaulted, capped, ineligible))
	}
	write(report{Status: "ok", Explicit: explicit.Delay, Default: defaulted.Delay, Capped: capped.Delay, Ineligible: !ineligible.Eligible})
}

func wrongExpectation(service retrypolicy.Service) {
	decision := service.Decide(retrypolicy.Terminal{Status: "failed", ProviderErrorCode: "rate_limit_exceeded"})
	if decision.Delay == 3*time.Second {
		fail(fmt.Errorf("wrong expectation unexpectedly accepted"))
	}
	fail(fmt.Errorf("wrong expectation rejected as intended: got %s", decision.Delay))
}

func write(value report) {
	encoded, err := json.Marshal(value)
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
