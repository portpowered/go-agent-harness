// Package discovery implements the browser-neutral WebMCP endpoint
// discovery boundary.
//
// The package deliberately stops at normalized browser/target records and a
// selection state boundary. It does not import a browser protocol
// implementation and does not expose transport URLs in discovery results,
// errors, selections, or semantic events. Browser runtimes can be added behind
// the endpoint, target, attach, and activation seams without changing this
// contract.
package discovery
