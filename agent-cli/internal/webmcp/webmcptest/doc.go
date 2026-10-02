// Package webmcptest contains the scripted WebMCP browser runtime fakes for
// tests: ScriptedBrowserRuntime and its target sessions model browser
// targets, catalogs and invocations without Chrome, CDP, a provider, or a
// websocket. Only _test.go files may import it; production code uses the
// hermetic package's browser-script runtime and evidence recorder instead.
package webmcptest

// toolResponseStatusCompleted and toolResponseStatusCanceled are the scripted
// tool-response statuses of the WebMCP protocol.
const (
	toolResponseStatusCompleted = "Completed"
	toolResponseStatusCanceled  = "Canceled"
)

// discardCleanupError runs a release on an abandon or cleanup path whose
// outcome is already decided. The resource is dropped either way, so its
// release error cannot change the result reported to the caller.
func discardCleanupError(release func() error) {
	if err := release(); err != nil {
		return
	}
}
