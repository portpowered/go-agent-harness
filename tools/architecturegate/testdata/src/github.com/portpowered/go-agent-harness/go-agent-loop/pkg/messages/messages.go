// Package messages is a minimal stand-in for the loop's session contracts.
package messages

type Session interface {
	Send(msg string) bool
	Close() error
}

type BargeInCapableSession interface {
	Session
	ProviderTurnDetection() bool
}

type SessionCapabilities struct{ Wrapped Session }

func (SessionCapabilities) ProviderTurnDetection() bool { return false }
