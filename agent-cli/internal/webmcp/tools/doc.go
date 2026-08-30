// Package tools adapts the browser-neutral WebMCP broker to the repository's
// existing CLI Tool and agent-loop ToolExecutor surfaces.
//
// The package exposes the six stable broker functions and the opt-in
// browser-enabled show_page capture function. Dynamic page descriptors remain
// result data and never become provider definitions.
package tools
