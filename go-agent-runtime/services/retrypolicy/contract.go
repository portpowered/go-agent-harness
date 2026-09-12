// Package retrypolicy exposes provider-neutral retry classification for
// terminal response metadata. Hosts supply values already detached from wire
// or CLI representations; the service owns compatibility parsing and bounds.
package retrypolicy

import "time"

// Terminal contains the provider-neutral terminal fields needed to classify a
// bounded rate-limit retry. Empty explicit fields allow legacy status details
// to provide compatibility values.
type Terminal struct {
	Status               string
	StatusDetails        string
	ProviderErrorCode    string
	ProviderErrorMessage string
	TerminalReason       string
}

// Decision is the stable retry outcome for one terminal.
type Decision struct {
	Delay    time.Duration
	Eligible bool
}

// Service owns terminal classification, metadata precedence, compatibility
// parsing, and bounded retry-delay calculation. Implementations are inert and
// keep no invocation state.
type Service interface {
	Decide(Terminal) Decision
	ProviderErrorCode(Terminal) string
	ProviderErrorMessage(Terminal) string
	NormalizeStatus(string) string
}
