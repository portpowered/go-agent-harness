## Summary

Repair the current-main live terminal-drain race by claiming provider RTC media immediately after `ConnectSession()` and before `Receive()`. The existing continuous-audio option is preserved and the later media attachment remains idempotent.

## Evidence

- Historical PR 504 runs `34748383831` and `34749714271` are retained with exact raw metadata/log hashes. Each failure lost one 6,400-sample output block with zero sink drops, overflow, discards, and terminal queue residue.
- C64 source-first and repaired controls remain unchanged.
- Focused normal/race tests, vet, architecture/size, and separate strict test45/test46 pass.
- Offline credential-free public replay from the source-pinned `yui` emitted `PROBE_TOOL_MARKER_9182`, ordered tool call/result, continuation, provider-close terminal metadata, and 4,800 bytes of output PCM.

See `docs/temp/projects/audio-runtime/audio-runtime-c127-repair-current-main-terminal-drain-regression/README.md`, `causal-evidence.json`, `verification-summary.json`, and `provenance.json` for exact commands, source/build hashes, boundary citations, and limitations.

This is a C127 vertical handoff only. Script CI should run on this exact head; CI has not been polled and this PR is not a project-wide completion claim.
