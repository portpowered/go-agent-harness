// Package webmcp contains the browser-neutral contracts used by the WebMCP
// broker. Browser protocol implementations belong behind these interfaces.
//
// This checkout may be built before the broker-core lane is integrated. The
// declarations in this package intentionally match the frozen C0 runtime
// shapes so the Chrome adapter has one neutral seam to implement.
package webmcp
