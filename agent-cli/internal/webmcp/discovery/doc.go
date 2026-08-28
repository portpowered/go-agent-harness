// Package discovery implements the browser-neutral WebMCP endpoint
// discovery boundary.
//
// The package deliberately stops at a normalized browser candidate. It does
// not import a browser protocol implementation and does not expose transport
// URLs in discovery results, errors, or semantic events. Browser runtimes can
// be added behind the endpoint and version seams without changing this
// contract.
package discovery
