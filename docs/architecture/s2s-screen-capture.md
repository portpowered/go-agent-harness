# Screen capture permission and failure runbook

The `show` tool remains advertised on macOS, but it must report an honest,
typed failure when the host cannot capture pixels. The CLI cannot grant macOS
Screen & System Audio Recording permission and must never turn a TCC denial
into a fake frame
or a long outer timeout.

## Grant access

1. Open **System Settings → Privacy & Security → Screen & System Audio Recording**.
2. Enable the application that launched the CLI:
   - **Terminal**: enable Terminal.app.
   - **iTerm2**: enable iTerm2.app.
   - **IDE-integrated terminal**: enable the IDE host (for example, VS Code,
     GoLand, or another JetBrains IDE), not only the shell process shown in
     the terminal.
   - **Direct CLI host**: enable the host process shown in the list for the
     process that launched `agent`; a shell or wrapper may need its own entry.
3. Completely quit and restart that terminal, IDE, or direct CLI host. TCC
   permission changes are not reliable for an already-running host.
4. Retry the same `agent session` command and the `show` request.

The `show` error names the detected terminal or CLI host and repeats these
steps. There is no supported command-line or API path for this tool to grant
the permission itself.

## Interactive recording bounds

The `record` action is bounded for voice/realtime use: duration accepts one to
five seconds and frame rate accepts one to two frames per second. Omitted values
use the three-second/two-frame-per-second defaults. An explicit value outside
those ranges is rejected before display admission or capture starts.

The recording loop uses the caller's context for display geometry, every frame,
pixel conversion, GIF encoding, and result preparation. Cancellation or a
deadline returns a typed failure and never returns an encoded partial animation;
the tool does not add a second session-wide timeout. The frame cap is computed
from the requested duration and rate, and successful results report the number
of encoded frames and their animation duration.

## Diagnose a revoked permission

To diagnose a regression, turn the host's Screen & System Audio Recording entry off, quit and
restart the host, and retry `show`. A revoked entry should produce the typed
`denied` outcome with the System Settings guidance, not a black image or a
generic 60-second timeout. Re-enable the entry, restart the host again, and
retry. If the entry is missing, check that the command is being launched by the
expected Terminal, iTerm2, IDE, or direct CLI host; a newly signed or moved
binary can appear as a new TCC identity.

## Grounded result and recording contract

Successful `show` and browser-enabled `show_page` calls return one compact
version-2 metadata object and one image part made from the same bytes. The
metadata includes the source kind, MIME type, byte length, dimensions,
lowercase SHA-256 digest, and the `input_image` projection marker; it never
contains a data URL or base64 pixels. A failed call returns the versioned
error object without an image, allowing the session to continue and explain
the failure.

When `--record-dir` is active, each successful capture is copied to a
collision-safe `screenshots/` artifact. The session log links that artifact to
the tool call and selected browser target (when applicable), while the
recording manifest hashes the file. Bundle finalization verifies the expected
artifact bytes and rejects missing, changed, or unreferenced files. Without a
recording directory, the capture remains in-memory only.
