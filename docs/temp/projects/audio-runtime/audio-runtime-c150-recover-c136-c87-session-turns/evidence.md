# C150 recovery evidence

Project: `audio-runtime`  
Contract revision: `audio-runtime-v1`  
Task: `audio-runtime-c150-recover-c136-c87-session-turns` (`work-task-35`)  
Admitted branch: `codex/audio-runtime-c150-recover-c136-c87-session-turns`

## Pre-integration preservation checkpoint — 2026-09-13

Admission was verified with `factory/scripts/project-control.py verify-work
--type task --name audio-runtime-c150-recover-c136-c87-session-turns`; the
result was `admitted` for the sole `audio-runtime` project. `prd.json` has the
same branch name as this isolated C150 worktree. The C150 worktree was clean at
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`, which is the freshly fetched
`origin/main` and includes the required startup ancestor
`8bdafc7f947a3a2c9856220abdc539437035bd21`.

The implementation is adopted from the existing C136 checkout, not rebuilt:

- C136 worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c136-recover-c87-session-turns`
- C136 branch: `codex/audio-runtime-c136-recover-c87-session-turns`
- C136 checkpoint: `7268f46a8c23cea480aecba621d49c01e4a30aac`
- PR: `#513`, whose head was `7268f46a8c23cea480aecba621d49c01e4a30aac`
- C136 dirty path: `docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/evidence.md`
- Dirty working-tree file SHA-256: `e10d62497e8b8a293574e482c754e4f863800c57c97a991da360f2a47b0f7a77`
- Committed C136 blob SHA-256 before the append: `1d47e4374a31827dbf3a2e0c855dc826ca9d9219b2d06e2266b49cdee2c0ec99`
- Dirty delta: exactly 48 added lines, zero deletions

The C136 checkout and branch were not reset, rebased, cleaned, or otherwise
mutated. This C150 record checkpoints the dirty append before integration; the
original C136 worktree remains the authoritative preserved source until the
same append is carried forward into the adopted candidate.

## Preserved latest rejection and ownership

PR #513 run `34772088096` evaluated head `7268f46a8c23cea480aecba621d49c01e4a30aac`.
The static job failed only on the 12 stale downward session-turns baseline
entries and the unregistered generated file
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`; vet, lint and
staticcheck succeeded. The coverage job independently failed only on
`TestManagedBrowserManagerReusesStateAndClosesOnlyOnExplicitClose`, where
TempDir cleanup found a non-empty WebMCP browser profile. That package is out
of C150 scope; focused rechecks passed 10/10 normal and 5/5 coverage trials.
This external non-reproduction is preserved as unresolved evidence, not called
fixed and not used to authorize WebMCP edits.

C110 PR #497, C111 PR #496, and C119 PR #505 remain open and retain the exact
shared registry/policy leases. C150 must not edit their paths until their
guarded merges release them. No prior independent review findings exist for
PR #513 (`reviews: []`); the latest CI rejection is the authoritative repair
inbox. No CI-green, review, merge, vertical-probe, or project-acceptance claim
is made by this checkpoint.

Next action: integrate the preserved C136 implementation into this C150
worktree without losing current-main or peer ancestry, then wait for or verify
release of the exact shared leases before applying only the demonstrated
session-turns registry and downward baseline repairs.
