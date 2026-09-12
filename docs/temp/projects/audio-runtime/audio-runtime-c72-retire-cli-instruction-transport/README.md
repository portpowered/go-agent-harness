# C72 session-update transport retirement

This change extracts the one-shot session instruction update from the CLI
runtime into the public, host-neutral `go-agent-runtime/services/sessionupdate`
contract, private service, and generated Wire composition. The CLI keeps only
the deprecated planner adapter and the narrow RTC-media bridge around the
original session.

The `consumer/` directory is a separate Go module. It imports only
`services/sessionupdate` and its public Wire constructor; it does not import
`agent-cli`, device packages, provider packages, flags, or hidden globals.

Evidence run against the isolated branch at the C72 admitted base:

- `go test ./services/sessionupdate/...`: 7 tests passed.
- `go test -race -count=1 ./services/sessionupdate/...`: 7 tests passed.
- `go test ./...` in `go-agent-runtime`: 858 tests passed in 60 packages.
- `go test -race ./...` in `go-agent-runtime`: 858 tests passed in 60 packages.
- CLI focused `Instruction|Capability` tests: 45 passed.
- `COUNT=1 scripts/test-session-ci-regressions.sh normal`: passed.
- `COUNT=1 scripts/test-session-ci-regressions.sh race`: passed.
- `make coverage-registration`: 179 workspace packages passed.
- `go vet ./services/sessionupdate/...`: passed.
- `GOWORK=off go test ./... -count=1` and `-race` in `consumer/`: passed.
- `GOWORK=off go list -deps ./...` in `consumer/`: no `agent-cli` dependency.
- `verification/run-drain-negative-control.sh`: the mutated drain test failed
  with status 1, then the source hash was restored and the positive test
  passed.
- `verification/run-bounded-regressions.sh`: passed with capped output and
  process-group cleanup; the temporary YUI binary hash was recorded in the
  command output.
- `wc -l agent-cli/internal/services/internal/agentruntime/session.go`: 76
  lines (225 before extraction; 149 physical production lines retired).

The local Wire and architecture gates remain intentionally pending shared
lease release. `make wire-check` reports the new
`go-agent-runtime/services/sessionupdate/wire/wire_gen.go` as unregistered,
and `make architecture-size-check` reports the same generated-file registry
fact. C57 currently owns `scripts/wire-packages.txt`; C61 owns
`docs/architecture/architecture-size-baseline.json`. This task does not edit
either shared file. After those owners release their leases, register the new
Wire package, reconcile the size baseline through the owner-mediated path, rerun
the gates, then submit the same commit to script CI.
