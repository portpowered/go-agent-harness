# C23 long-session tool characterization

This folder is the sole owned path for the C23 evidence-only candidate. It
contains a source-file-built public consumer and a bounded Python driver. The
consumer imports exported `messages`, `session`, `session/wire`,
`recording`, `recording/wire`, `audio/clock`, and gateway testing contracts;
it does not import CLI-private or runtime-internal packages.

The deterministic fixture runs 16, 64, 128, and 256 logical turns. Every turn
has two distinguishable tool identities, one correlated result per call, a
continuation response, and one exact PCM16 frame. Recording off/on runs share
the same provider fixture and schedule; the driver compares normalized
semantic results and the complete PCM byte stream and SHA-256.

The interruption scenario exercises `RESPONSE.CANCEL`, proves that the
cancelled response emits no later output, and requests a distinct healthy
response with a non-empty exact tail. It is provider-simulated evidence only;
it does not claim physical device, acoustic, or host-load proof.

The runner also records source/ancestry/toolchain/build provenance, sanitized
child execution, bounded output, raw public session capture, semantic
recording, per-turn timestamp-domain latency samples, heap/goroutine
measurements, and deliberate negative controls for PCM mismatch, identity
swap, missing result, and duplicate result. It stops at the first unexpected
failure and preserves that report for classification.

Required driver commands are documented by `characterize.py --help`; the
admission handoff invokes `prepare`, `verify-provenance`, `controls`, `matrix`,
`shipped-regressions`, `report`, and `self-check` in that order.
