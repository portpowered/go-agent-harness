package webmcp

// BrowserCapabilityState is the session-owned browser capability state that
// is safe to carry into a provider contract. It deliberately separates
// endpoint availability from page selection: a connected broker may have no
// selected page while it waits for an exact customer choice.
type BrowserCapabilityState string

const (
	BrowserCapabilityInitializing        BrowserCapabilityState = "initializing"
	BrowserCapabilityDisabled            BrowserCapabilityState = "disabled"
	BrowserCapabilityUnavailable         BrowserCapabilityState = "unavailable"
	BrowserCapabilityDisconnected        BrowserCapabilityState = "disconnected"
	BrowserCapabilityConnectedUnselected BrowserCapabilityState = "connected_unselected"
	BrowserCapabilitySelected            BrowserCapabilityState = "selected"
)
