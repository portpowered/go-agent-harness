# C17 pre-fix public-loop reproduction

- Source revision: `f8e0863222da1bdbf296e2220fcbc081461cc877`
- Base: freshly fetched `origin/main=f8e0863222da1bdbf296e2220fcbc081461cc877`
- Consumer source SHA256: `7b0954a36e5b57d0d0f41e7d6ecab14029bb2c9ba33fd9342706febab4ac579d`
- Consumer executable SHA256: `2494f974eac4dcc9f55b60b7eca48cdbe01686d8a762dd39455712cbc7efb2a2`

Build command and status:

```text
rtk go build -o docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer.go
status=0
```

Pre-fix execution command and status:

```text
rtk proxy python3 -c "import subprocess; subprocess.run(['docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer'], check=True, timeout=30)"
status=1
```

Raw child output:

```text
pong[1] observed=1788929940791 expected=1767323045123
c17 public-loop clock reproduction failed: PONG timestamp mismatch: observed=1788929940791 expected=1767323045123
```

The failure is the expected timestamp mismatch: the public loop accepted and
processed the ping, but the PONG used current host time instead of the fixed
deterministic clock epoch. The consumer uses a real watchdog, waits for the
public `SESSION.OPEN`/`Deltas` path, sends through public `AgentLoop.Send`, and
joins `Run` on cancellation.

Post-repair execution from the same source tree:

```text
rtk go build -o docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer.go
status=0
rtk proxy python3 -c "import subprocess; subprocess.run(['docs/temp/projects/audio-runtime/c17-canonical-clock-pong/consumer'], check=True, timeout=30)"
status=0
pong[1] observed=1767323045123 expected=1767323045123
pong[2] observed=1767323045123 expected=1767323045123
pong[3] observed=1767323045160 expected=1767323045160
c17 public-loop clock reproduction passed
```

- Post-repair consumer source SHA256: `7b0954a36e5b57d0d0f41e7d6ecab14029bb2c9ba33fd9342706febab4ac579d`
- Post-repair consumer executable SHA256: `632bc6f2498f47baa48bd605d38d0fa694f37e005ff2ae2d91f9a03b8072e7a3`

Current-main integration checkpoint:

- Fresh `origin/main`: `49ca32eb6e3fba5fe95a993324d0b3d27f18c324`
- Candidate merge commit: `4c63cfef72f37d2a7c1de60c77b63baa83b1ef8e`
- Candidate parent: `f5bfca25ab16850019f5ab65319b64ac582c097b`
- Mainline parent: `49ca32eb6e3fba5fe95a993324d0b3d27f18c324`
- `origin/main` is an ancestor of the candidate; the running host checkout was not touched.

Final local validation on the formatted candidate:

```text
rtk go test ./pkg/subsystems ./pkg/agentloop -run 'Test.*(Ping|Pong|RunCancellationReleasesBlockedDeltaForwarder|SendSessionEvent|SendSessionMessage)' -count=1 -timeout=60s
status=0; 16 passed in 2 packages
rtk go test -race ./pkg/subsystems ./pkg/agentloop -run 'Test.*(Ping|Pong|RunCancellationReleasesBlockedDeltaForwarder|SendSessionEvent|SendSessionMessage)' -count=1 -timeout=60s
status=0; 16 passed in 2 packages
rtk go test ./pkg/subsystems ./pkg/agentloop -count=1 -timeout=60s
status=0; 151 passed in 2 packages
rtk go test -race ./pkg/subsystems ./pkg/agentloop -count=1 -timeout=60s
status=0; 151 passed in 2 packages
rtk go build ./pkg/subsystems ./pkg/agentloop
status=0
rtk go vet ./pkg/subsystems ./pkg/agentloop
status=0
rtk make architecture-check
status=0; 181 packages, 1859 files, 27084 functions
rtk make size-check
status=0; 181 packages, 1859 files, 27084 functions
rtk git diff --check
status=0
```

The first size-check attempt with a separate `agentloop/ping_clock_test.go`
correctly rejected immutable baseline drift (`18 > 17` files). The public-loop
tests were consolidated into the owned external `subsystems/ping_pong_test.go`
without changing assertions or the baseline; the final size gate is green.
