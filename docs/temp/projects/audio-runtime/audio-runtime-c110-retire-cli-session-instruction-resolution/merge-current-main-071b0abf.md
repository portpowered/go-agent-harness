# C110 merged-current-main handoff checkpoint

- Admission was reverified for the sole `audio-runtime` project and the task
  branch remains `codex/audio-runtime-c110-retire-cli-session-instruction-resolution`.
- `origin/main` was fetched at `071b0abfd67501db61e3c1929971c6dd6e77eb62` and
  merged into the preserved C110 branch as `54857e3128d13775622a6504889c1c16a96486b7`.
  The only conflict was the shared
  `docs/architecture/architecture-policy.json`; the C110
  `sessioninstructions/wire/wire_gen.go` registration and current-main
  `rtctransport/wire/wire_gen.go` registration were both retained. The
  auto-merged `scripts/wire-packages.txt` content was preserved.
- C79 is now terminal-complete and vertically accepted in the canonical board,
  so its guarded-merge lease on the shared registries is released. The
  demonstrated C110 registry changes remain limited to the sessioninstructions
  Wire registration and the four stale legacy function-baseline deletions.
- Focused merged-tree gates passed: sessioninstructions normal (51 tests),
  sessioninstructions race (75 tests), CLI focused normal (223 tests), CLI
  focused race (615 tests), the GOWORK=off two-instance consumer, both
  mutation guards, retirement/owned-path verification, format, coverage
  registration (187 packages), Wire, architecture-size (197 packages, 1924
  files, 28324 functions), vet, pinned staticcheck 2026.1, and pinned
  golangci-lint 2.9.0 with zero findings.
- Accumulated session regressions passed at `COUNT=1` in normal, coverage and
  race modes, including all 20 high-rate trials and preserved negative controls.
- Source-pinned public evidence from `54857e3` passed: YUI artifact
  `662ee37170c70642baa44e6c942e2876f2f766349364471300b9b3714ba2095c`,
  instruction/text-seed and audio/tool replay exit 0 with clean process reap,
  and malformed/oversized instruction inputs rejected before provider/session
  planning. CI, independent review, guarded merge, and post-merge vertical
  acceptance remain external.
