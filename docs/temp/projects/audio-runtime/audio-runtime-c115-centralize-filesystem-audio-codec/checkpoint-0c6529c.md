# C115 current-main integration and static-repair checkpoint

## Identity and ancestry

- Admission was reverified with `factory/scripts/project-control.py verify-work --type task --name audio-runtime-c115-centralize-filesystem-audio-codec --root "$FACTORY_ROOT"`; the result was `status=admitted`, project `audio-runtime`. The factory session is `~default` at `http://127.0.0.1:7439`.
- The isolated branch `codex/audio-runtime-c115-centralize-filesystem-audio-codec` matches `prd.json.branchName`. The worktree is clean at `0c6529c2e72877f117f5325423e65ea60953eeae` before this evidence file is added.
- `git fetch origin main` refreshed `origin/main` to `b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`. Merge commit `c60d116f9e6934fe71cf98cc5cd13493abb644d1` integrates that mainline into the candidate without touching the running host checkout. Startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main `3963bc3566da24f8214634c17a9d0f79a6724171`, and refreshed `origin/main` are all ancestors.

## Static rejection and repair

- PR #501's exact current-head run `34735661094`, job `103666511165`, rejected head `f78f92bd99b7c068c22ce24fb1eea37084055929` in `CI (static)`. The completed log names exactly one failing step: `make architecture-size-check` reported `generated-file-spoof services/audiocodec/wire/wire_gen.go: generated header is not registered with a reproducible generator`; fmt, Wire regeneration, vet, pinned lint and pinned staticcheck completed successfully.
- Commit `0c6529c2e72877f117f5325423e65ea60953eeae` adds only the exact generated-file registration for `go-agent-runtime/services/audiocodec/wire/wire_gen.go` to the architecture policy. It does not edit either formerly shared C79 registry file; mainline's registry-contention migration is preserved.
- Post-repair `make wire-check`, `make architecture-size-check` (`189` packages, `1,906` files, `28,158` functions), `make test-architecture-gate`, `make coverage-registration` (`179` packages across `6` modules), `make fmt`, `make build`, `make vet`, pinned golangci-lint `2.9.0`, pinned staticcheck `2026.1`, and `git diff --check` pass.

## Causal and accumulated evidence

- Audiocodec normal tests pass: `39` tests in `3` packages. Focused codec race tests pass: `99` tests in `3` packages at `-count=3`.
- Filesystem audio/path tests pass: `58` normal tests and `57` race tests at `-count=3`.
- The separate `GOWORK=off` external consumer compiles and runs successfully. It exercises public Wire construction, conversion, input immutability, output bounds and cancellation identity. Codec service coverage is `91.6%` with the nomicrophone service profile.
- The affected tools package passes `341` tests in `17` packages, and the affected CLI wire/service packages pass `1,247` tests in `20` packages. `make test-regressions` passes the agent-cli and go-llm-gateway replay fixtures.
- The accepted-main filesystem baseline remains `261` lines with SHA-256 `6f67450d01cda17896afcc7a302dc024401b4fb56c163cb5eb625546e2197679`; the candidate is `228` lines with SHA-256 `a086ff1a7366cd59891bdfe6b5f2e9237d9079389530affa5ef46dd28bd20e2e`, a strict `33`-line reduction.

This is an executor handoff checkpoint. The repaired head has not yet been submitted to the script CI gate, and no green CI, independent review, guarded merge, vertical probe or project acceptance is claimed. Next action: commit this checkpoint, push the same PR #501 branch, update the PR with the exact rejection/repair and evidence, then submit the changed head to script CI without polling; retain C115 ownership for any exact actionable rejection.
