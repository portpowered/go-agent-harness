# C83 bounded tracker/interruption checkpoint

## Admission and ancestry

- Factory task: `audio-runtime-c83-retire-cli-browser-scenario-runner`
- Project: `audio-runtime`, session: `~default`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c83-retire-cli-browser-scenario-runner` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c83-retire-cli-browser-scenario-runner"}`.
- Isolated branch: `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`
- PRD branch: `codex/audio-runtime-c83-retire-cli-browser-scenario-runner`
- Accepted base and `origin/main` after `git fetch origin main`: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- Legacy baseline census at the accepted base: runner 1,280; run 956; tracker 487; interrupt 276; fixture options 144; total 3,143 lines.

## Bounded implementation

This checkpoint moves only the evidence-tracker and event-driven interruption behavior into the new public `go-agent-runtime/services/browserrunner` contract, private implementation, and generated Wire graph. The CLI files are thin adapters over that contract. The public boundary has no agent-cli, provider, device, credentials, flag, or mutable-global dependency.

The unchanged legacy runner, its large runner test, and C61 policy/report paths remain in place. The reviewed C61 public scenario contract is not present on `origin/main`; its local branch remains unreviewed and was not cherry-picked or copied. Shared `scripts/wire-packages.txt` and architecture baseline updates remain deferred under the current lease instructions.

## Gate evidence

- `GOWORK=off go test ./services/browserrunner/...`: passed.
- `GOWORK=off go test -race ./services/browserrunner/...`: passed.
- Focused unchanged CLI browser conversation tests: passed in normal and race modes.
- `GOWORK=off go test ./...` in the external consumer module: passed.
- `GOWORK=off go test -race ./...` in the external consumer module: passed.
- Coverage registration: passed, 179 workspace packages across 6 modules.
- New internal service coverage: 70.9% with the declared 70.00% floor.
- `git diff --check`: passed.

## Next action

Commit and push this disjoint checkpoint, then hand the same task to the script CI gate. After C61 lands through its reviewed guarded merge, reconcile its exact runner checkpoint against this base before continuing the remaining C83 orchestration extraction and thin CLI adapter work.
