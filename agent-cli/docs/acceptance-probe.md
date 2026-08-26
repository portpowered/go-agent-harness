# Blind acceptance probes

`agent probe acceptance <binary> <goal>` runs one acceptance probe. The
entrypoint creates a fresh empty working directory, starts the supplied
executable with the goal as its only argument and a sanitized environment
containing only `PWD` for that working directory, captures stdout/stderr,
transcript, exit status, and files created in that directory, and prints one
machine-readable verdict record.

The verdict passes only when all of these conditions hold:

- the report names a non-empty artifact and checked claim that the recorded
  artifact actually contains; and
- the report's subjective rating is `easy` or `workable`.

The production runner also requires a goal-aware `RecordedArtifactVerifier`
checker (or another `ObjectiveVerifier`) to independently establish that the
recorded bytes satisfy the plain-English goal. The default zero-value verifier
fails closed because a probe-selected substring is not proof that the goal was
attained. The goal-catalog lane supplies that checker through the existing
runner injection seam.

`confusing`, missing, or unknown ratings fail. A claimed success without a
verifiable artifact also fails. The verdict retains the existing probe result
fields (`name`, `pass`, `terminal_reason`, and `error`) so it can be consumed
as a result line by downstream probe tooling. `run_directory` points to the
durable captured artifacts.

The live and replay paths share the same runner. Replay callers construct a
`probe.ReplayFixture` with any workspace-created evidence in its safe relative
`workspace_files` map and pass `probe.NewReplayRunner` to
`cli.NewProbeAcceptanceCommand`; this changes only the transport, not the
command or verdict contract. A live probe executable reports its acceptance
claim as the final JSON line on stdout, for example:

```json
{"claimed_success":true,"objective_artifact_path":"result.txt","checked_claim":"goal complete","subjective_rating":"easy"}
```

The process may create `result.txt` in its empty working directory. The
acceptance runner snapshots that file into the run directory before verifying
the claim. A process that exits non-zero, crashes, or reaches the stuck
deadline produces a non-passing terminal state and a non-zero command result.
