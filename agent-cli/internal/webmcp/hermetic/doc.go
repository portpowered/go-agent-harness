// Package hermetic runs WebMCP browser scripts offline and records their
// semantic evidence.
//
// The probe's hermetic browser executor (agent probe run, the default
// --browser-executor) replays a browser-script fixture through
// BrowserScriptRuntime and BrowserScriptAdapter, with a FakeClock and
// deterministic IDs, and records redacted semantic browser events through
// Recorder. Nothing here imports Chrome, CDP, a provider, or a websocket.
//
// The scripted browser fakes for tests live in the webmcptest package.
package hermetic
