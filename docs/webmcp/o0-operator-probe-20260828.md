# O0 operator probe results — 2026-08-28 (darwin/arm64)

Direct operator experiments on this machine, run before the O0 lane completes.
These are dated facts the O0 evidence doc should incorporate and extend.

## Environment

- Stock **Google Chrome 151.0.7922.174** (stable channel), `/Applications/Google Chrome.app`
- Node v26.7.0 (native WebSocket used as the CDP client; no chromedp involved yet)
- Headless launch shape used throughout:
  `--headless=new --remote-debugging-port=<port> --user-data-dir=<fresh tmp dir> --no-first-run`

## Findings (all verified live, not assumed)

1. **The experimental `WebMCP` CDP domain ships in STOCK STABLE Chrome 151 — no flags needed for the CDP side.**
   `WebMCP.enable` succeeds on a default headless launch. The build's own
   `/json/protocol` reports the domain (experimental: true) with commands
   `enable()`, `disable()`, `invokeTool(frameId, toolName, input) -> invocationId`,
   `cancelInvocation(invocationId)` and events `toolsAdded`, `toolsRemoved`,
   `toolInvoked`, `toolResponded(invocationId, status, output, errorText, exception)`;
   types `Annotation`, `InvocationStatus`, `Tool`, `RemovedTool`.
   Older public docs claiming Canary-146-only are outdated for the CDP surface.

2. **`invokeTool`'s `input` parameter is a JSON OBJECT, not a string.** Passing a
   JSON-encoded string fails with `CBOR: map start expected`. The broker's
   Chrome adapter must decode `input_json` before the CDP call.

3. **Declarative page tools work end to end under `--enable-blink-features=DeclarativeWebmcp`**
   (launched together with `--enable-experimental-web-platform-features`; minimal
   flag set not yet isolated). A form annotated with `toolname`, `tooldescription`,
   per-input `toolparamdescription`, and `toolautosubmit` produced:
   - `WebMCP.toolsAdded` with a real generated JSON Schema
     (`{type: object, properties: {message: {type: string, description: ...}}}`),
     `annotations: {autosubmit: true}`, `frameId`, `backendNodeId`;
   - `invokeTool` -> `invocationId`; `toolInvoked` with the input echoed;
   - `toolResponded` fired; **independent page-state oracle confirmed the page
     DOM actually changed** (`STATUS:E2E-PROOF`) — the invocation genuinely
     drove the page.
   - Note: `toolResponded.status` was `Error` ("The site has a programming
     error...") because the throwaway fixture used `preventDefault` without the
     proper response contract; the state still changed. A proper fixture must
     follow the declarative autosubmit/response contract. Determining that exact
     contract is O0/fixture work.

4. **Imperative `navigator.modelContext` (registerTool/provideContext) is NOT exposed
   in stable 151** under any flag combination tried: `--enable-features=`
   `WebMCP`/`WebMCPTesting`/`DevToolsWebMCPSupport`/`NavigatorModelcontext`/`DeclarativeWebmcp`,
   `--enable-blink-features=` (same names), `--enable-blink-test-features`,
   `--enable-experimental-web-platform-features`. The binary contains
   `kNavigatorModelcontext`, so the code exists but is gated beyond flags in this
   build (likely Canary/field-trial). **Consequence: the hermetic fixture for I1
   should use DECLARATIVE form tools first** (proven working), and/or O0 must
   pin a Chrome for Testing / Canary build where imperative registration is
   flag-enableable if the fixture needs `registerTool`.

5. **`navigator.modelContextTesting`** (interface `ModelContextTesting`: `listTools`,
   `executeTool`, `getCrossDocumentScriptToolResult`, `ontoolchange`) appears under
   `--enable-experimental-web-platform-features` — a page-side testing/inspection
   surface, useful as a second oracle in fixtures.

6. Feature-name strings found in the 151 binary for future pinning work:
   `kDeclarativeWebmcp`, `kNavigatorModelcontext`, `ModelContextTesting`,
   `WebMCPTesting`, `DevToolsWebMCPSupport`, `WebMCPFormAssociatedCustomElements`,
   `WebMCPDeclarativeFileInput`, `blink::InspectorWebMCPAgent`,
   declarative attributes `toolname`, `tooldescription`, `tooltitle`,
   `toolparamdescription`, `toolautosubmit`.

## Consequences for the O0 lane

- Assumption (3) of the lane brief ("whether the pinned Chrome exposes the WebMCP
  surface") is answered for the CDP half: yes, in stock stable 151. The remaining
  O0 work: pin an exact Chrome for Testing build, isolate the minimal flag set for
  declarative tools, determine the declarative response contract (why a
  preventDefault handler yields status=Error and what the correct page-side
  completion looks like), test the detach-survival lifecycle, and confirm
  chromedp/cdproto bindings cover the `WebMCP` domain (if cdproto lacks it,
  raw-CDP via the existing WebSocket path is a viable fallback — proven here with
  a ~40-line node client; the Go adapter can do the same without full chromedp).
