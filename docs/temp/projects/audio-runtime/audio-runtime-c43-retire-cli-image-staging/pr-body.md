## Summary

- Retire CLI image staging, extension selection, read_image path advertisement,
  and refresh decoration behind the public tools contract.
- Keep CLI host ConfigDir/home resolution and option mapping only; add a direct
  tools Wire constructor without changing generated Wire graphs.
- Add private normal/race lifecycle controls and a public consumer with literal
  PNG, actual public read_image execution, refresh checks, cleanup, and a
  deliberate wrong-oracle negative control.

## Evidence

- Focused verifier: `verify.py --mode focused` PASS.
- Full tools normal and race suites PASS.
- CLI `Image|ReadImage` normal and race tests PASS.
- Public consumer normal and race builds PASS; positive literal PNG read,
  refresh, cleanup, and post-cleanup negative control PASS.
- Shipped CLI image/audio workflow PASS:
  `TestSessionCommandImageAndScheduledAudioUsesExactStagedImagePath`.
- Credential-free tool replay PASS:
  `TestSessionCommand_DefaultRegistryExecRoundTripInStrictOpenAIReplay`.
- Wire regeneration PASS with no generated diffs.
- CLI adapter source reduced from 142 to 68 lines; the three legacy staging
  symbols are retired.

## Required primary repairs before script-CI handoff

- `make architecture-check size-check wire-check` reaches size-check but
  reports package-files baseline drift in
  `agent-cli/internal/services/internal/agentruntime`: 238 files versus the
  reviewed baseline 237 because the explicitly leased
  `session_image_staging_test.go` is added. The architecture baseline is
  outside this task lease; do not lower or edit it here.
- `make coverage-registration coverage-changed
  COVERAGE_BASE=926ded7bfa8f3c3e42115192d03aa1240c4806db` stops at registration:
  `go-agent-runtime/services/tools/internal/imagestaging` is absent from
  `coverage-manifest`, which is outside this task lease.

This PR is intentionally not claiming green CI or script-CI readiness until
those exact primary-owned bookkeeping repairs are assigned and applied.
