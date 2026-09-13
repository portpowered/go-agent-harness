## Summary

Repair the current-main live terminal-drain race by claiming provider RTC media immediately after `ConnectSession()` and before `Receive()`. The existing continuous-audio option is preserved and the later media attachment remains idempotent. Source checkpoint: `bb88393e41ff93d3f1663b4085c8b03ac11baf6b`; the candidate also contains freshly fetched `origin/main` `bd6a1289218d1bef1a3af36e64e9d4496062416f`.

## Evidence

- Historical PR 504 runs `34748383831` and `34749714271` are retained with exact raw metadata/log hashes. Each failure lost one 6,400-sample output block with zero sink drops, overflow, discards, and terminal queue residue.
- C64 source-first and repaired controls remain unchanged.
- The bounded ordered causal report proves the first divergence is `rtc_forwarding`, with explicit six-boundary sequence/timing/sample evidence; the unmodified pinned current-main control fails and accepted C64 remains healthy.
- Focused normal/race tests, accumulated normal/coverage/race session regressions, vet, pinned lint/staticcheck, coverage registration, Wire, architecture, and separate strict test45/test46 pass.
- The source-pinned `yui` public runner and credential-free replay emitted `PROBE_TOOL_MARKER_9182`, ordered tool call/result, continuation, provider-close terminal metadata, zero terminal queue, and 4,800 bytes of output PCM.
- The full changed-package coverage attempt reached an unrelated existing SIGINT deadline failure under broad instrumentation; the exact test passes on both origin/main and this candidate outside that run, and its source is outside the C127 lease. This is recorded for the executor rather than claimed as C127 green coverage.
- The prior SCRIPT CI rejection additionally found the 772-line causal test and 26/28 complexity scores; the causal probe is now in `go-agent-runtime/services/session/internal/live/causal/terminal_drain_test.go` (241 lines) with helper-level assertions, while the parent live package remains within its 15-file budget. The same CI coverage job failed the out-of-lease browser interruption test; it remains preserved for its owner and is not claimed as a C127 result. A standalone local run of that exact browser test passed.

See `docs/temp/projects/audio-runtime/audio-runtime-c127-repair-current-main-terminal-drain-regression/README.md`, `causal-evidence.json`, `verification-summary.json`, and `provenance.json` for exact commands, source/build hashes, boundary citations, and limitations.

This is a C127 vertical handoff only. Script CI should run on this exact head; CI has not been polled and this PR is not a project-wide completion claim. No independent review or guarded merge is claimed yet; any CI rejection remains actionable on this same task.
