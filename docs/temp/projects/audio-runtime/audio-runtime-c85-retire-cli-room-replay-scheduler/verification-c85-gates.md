# C85 verification ledger

Project: `audio-runtime`
Task: `audio-runtime-c85-retire-cli-room-replay-scheduler`
Implementation checkpoint: `b07e8e6`
Baseline: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`
Branch: `codex/audio-runtime-c85-retire-cli-room-replay-scheduler`

## Admission and scope

- `project-control.py verify-work --type task --name audio-runtime-c85-retire-cli-room-replay-scheduler` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c85-retire-cli-room-replay-scheduler"}`.
- `prd.branchName` matches the isolated worktree branch.
- `git fetch origin main` completed; `HEAD`, `origin/main`, and the planning-main checkpoint are descendants of the admitted startup ancestry.
- `verify.py --mode all` passed: baseline scheduler `409` lines, current adapter `139` lines, `270` retired; no excluded paths changed; external imports are only the public runtime service and Wire package.

## Focused causal and regression evidence

- `rtk go test ./services/roomreplayschedule/...`: `12 passed in 3 packages`.
- CLI fractional-timeline and production-mixer overlap tests: `2 passed in 1 package`.
- Accumulated replay regression selection: `38 passed in 1 package`.
- Runtime race selection (`Test(Build|Run)`, count `3`): `36 passed in 3 packages`.
- Production-mixer replay race: `1 passed in 1 package`.
- Independent credential-free consumer: `{"external_consumer":true,"credential_free":true,"released":2,"acknowledgements":1,"malformed_rejected":true}`.
- Coverage gate against the current local profiles: `coverage gate passed: 179 registered packages checked across 7 profiles`.
- `make coverage-registration`: `179 workspace packages checked across 6 modules`.
- `make lint`: all workspace modules reported `0 issues`.
- `make staticcheck` and `make vet`: completed successfully for all configured workspace modules.

## Shared C79 gates intentionally deferred

The task does not modify shared registry or architecture-baseline files. These exact gate results remain open for the C79 owner/released-main reconciliation:

- `rtk make wire-check` exited `2`: `wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/roomreplayschedule/wire/wire_gen.go'], outside modules=[]`.
- `rtk make architecture-size-check` exited `2` with `10` findings: `9` stale baseline entries for the retired legacy scheduler file/functions, plus `generated-file-spoof services/roomreplayschedule/wire/wire_gen.go: generated header is not registered with a reproducible generator`.

Required shared files remain unchanged: `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`. No new architecture complexity, size, or mutable-global findings were reported.

CI has not been polled or represented as green. The exact next action is to push this candidate and submit it to the script CI gate; repair only exact CI rejection findings on this same task/branch.
