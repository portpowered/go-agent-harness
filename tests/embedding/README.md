# Independent runtime consumer

This module deliberately lives outside the workspace module list and imports
only public runtime and lower-library contracts. It uses adjacent replacements
for checkout testing, and must be run with `GOWORK=off`.

The tests exercise inert construction, an explicitly empty tool surface,
cancellation before inference, identifier error propagation, retained history
across invocations, and two independent concurrent hosts. No CLI config directory, terminal, credential or
device is supplied. They are an acceptance gate for the extraction; an importable
wrapper that still requires CLI configuration does not satisfy these tests.

The live acceptance cases exercise exact PCM and metadata transfer, terminal
tails, cancellation causes, and device playback-controller binding through the
public media endpoint. They must preserve the audible response identity and
device-consumed duration used for barge-in truncation. These tests are migration
gates; failures remain visible while the new runtime is being integrated.

Capability acceptance checks preserve the full typed browser observation and
participant identity, defer initialization until Start, close the capability
once, and cancel its watcher before Close returns. Commit-ordering tests keep
later PCM behind the provider's acknowledgement of an earlier commit.

Room acceptance uses a deterministic clock and public services to distinguish
valid empty source recordings from unavailable provider traces. Empty PCM and
WAV streams stay empty; a missing provider trace marks the bundle partial,
fails strict replay admission, and is never replaced with a synthetic file.

From this directory, run `GOWORK=off go test ./...`. Full protocol replay and
device parity remain separate completion requirements.
