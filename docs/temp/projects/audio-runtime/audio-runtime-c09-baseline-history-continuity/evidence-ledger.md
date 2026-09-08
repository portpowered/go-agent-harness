# audio-runtime-c09-baseline-history-continuity evidence ledger

Task: `audio-runtime-c09-baseline-history-continuity`
Project: admitted `audio-runtime`; factory session `~default`; resolved session
`f24459ea-a7d9-4fd8-a1bb-91b0bd9629bd`; server `http://127.0.0.1:7439`.

## Admission, branch, and preserved ancestry

- Read `factory/docs/operating-policy.md`, `factory/docs/implementation-handoff.md`,
  `factory/docs/meta-planner-handoff.md`, the immutable audio-runtime manifest,
  request, acceptance, source plan, `progress.txt`, and prior C08 evidence.
- Admission command:
  `rtk proxy python3 factory/scripts/project-control.py verify-work --type task --name audio-runtime-c09-baseline-history-continuity`
- Admission result: `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c09-baseline-history-continuity"}`.
- `prd.json.branchName` and the isolated worktree branch are both
  `codex/audio-runtime-c09-baseline-history-continuity`.
- `git fetch origin main` completed at setup and again before handoff;
  `origin/main` is `00c147585f31808c7bdbbf6051cc9df423dbdf37`.
- Implementation checkpoint: `9b6b80a8f6425a4f79e94a2d1cbac905817e89d7`
  (`fix: preserve established architecture baseline history`). The baseline
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`, startup integration
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, and fetched main revision are
  all ancestors of that checkpoint.
- C08 remains untouched and preserved: its isolated worktree is clean at
  `e8bc361ae5fbbd419516f66e1f74e7c78af8a741`; PR400 remains OPEN at that head,
  based on `00c147585f31808c7bdbbf6051cc9df423dbdf37`, with commits
  `71986c13`, `8055c551`, and `e8bc361a` retained.

## Preserved hosted failure and local reproduction

The preserved C08 hosted failure is run `34186932187`, job `101937050020`,
head `e8bc361ae5fbbd419516f66e1f74e7c78af8a741`, command
`make architecture-size-check`. Its gate exit status was `1`, Make exit status
was `2`, and its diagnostic was:

```text
baseline-history-source docs/architecture/architecture-size-baseline.json: baseline source_commit "ddebb8f44346cde4c7e989a1880b8ffac6179df0" does not identify merge base 00c147585f31808c7bdbbf6051cc9df423dbdf37
```

Before the repair, the exact bounded local command `rtk make
architecture-size-check` exited `2` after `13.484s` with the same single inner
gate issue and diagnostic. The baseline and policy files were not edited.

## Repair and behavioral coverage

`compareBaselineHistory` now reads the merge-base baseline before applying
source provenance rules. An already-established baseline is compared against
that historical manifest, so its original `source_commit` survives later main
advancement. A baseline absent at merge base still takes the bootstrap path,
which requires `source_commit` to equal the exact merge-base revision before
measuring source history. Established history rejects any source replacement or
removal, added exemptions, ceiling increases, changed messages, and missing
rename targets; reductions and deletion-only history remain valid.

The focused command
`rtk proxy env GOWORK=off go test ./... -count=1 -timeout 120s -run
'TestBaselineHistory|TestBaselineBootstrap' -v` passed all selected tests,
including the new merged-baseline/mainline-advance fixture and provenance,
addition, reduction/deletion, rename, and bootstrap negative controls.

## Candidate checks

- `rtk make test-architecture-gate`: passed.
- `rtk make architecture-size-check`: passed; `181 package(s), 1850 file(s),
  26899 function(s) checked`.
- `rtk make fmt`, `rtk make build`, and `rtk make vet`: passed.
- `rtk make test-tools`: passed, including factory script checks, analyzer-gate,
  session-race-gate, architecture-gate, and Wire checks.
- Pinned `rtk make lint`: passed with `0 issues` in every workspace module.
- Pinned `rtk make staticcheck`: passed for every workspace module.
- `rtk make wire-check`: regenerated all eight registered graphs successfully;
  `git status` remained limited to the intended task files and no generated
  diff remained.
- `rtk git diff --check`: passed.
- `docs/architecture/architecture-size-baseline.json` and
  `docs/architecture/architecture-policy.json` are byte-identical to HEAD.
  The implementation checkpoint changed only `tools/architecturegate/`;
  this ledger is the only subsequent owned evidence path.

No CI was polled or claimed green. The exact next action is push the admitted
branch, open/update its PR against `main`, verify the submitted head, and return
`ACCEPTED` to the script-owned CI gate without polling terminal CI. CI rejection
must return to this same task for exact-log repair; independent review and merge
remain later stages.

## Executor continuation checkpoint

- Re-fetched `origin/main` before verification; it remains
  `00c147585f31808c7bdbbf6051cc9df423dbdf37`. Admission was reverified with the
  command above, the isolated branch still matches `prd.json.branchName`, and
  the baseline, startup integration, and fetched-main revisions remain
  ancestors of `HEAD`.
- Before this continuation checkpoint, PR #401 was OPEN at the exact remote
  candidate head `0f8916374467098a3a3080960963f6c031854415`; its review state
  had no independent reviews or comments. No review finding is being treated
  as resolved by executor testing.
- Fresh focused causal evidence on the committed candidate:
  `rtk proxy env GOWORK=off go test . -run
  'TestBaselineHistory|TestBaselineBootstrap' -count=1 -timeout=3m -v`
  exited 0 in 1.786s; the focused race command
  `rtk proxy env GOWORK=off go test -race . -run
  'Test(Baseline|SizeMetricsAndDeletionOnlyBaseline)' -count=1 -timeout=3m -v`
  exited 0 in 3.008s. The full architecturegate package passed with
  `rtk proxy env GOWORK=off go test ./... -count=1 -timeout=3m` in 2.347s.
- `rtk make architecture-size-check` exited 0 and reported 181 packages, 1850
  files, and 26899 functions. The accumulated `rtk make test-tools` target
  exited 0, including Wire integrity, analyzer-gate, session-race-gate and the
  architecturegate tests. `git diff --check` passed.
- `git diff --exit-code origin/main --
  docs/architecture/architecture-size-baseline.json
  docs/architecture/architecture-policy.json` passed. The candidate change
  scope remains the three architecturegate files and this owned ledger only.

This continuation evidence is checkpointed at
`b624cec5728c8e15a5feb9875b7c6f0a71e7862f`; the exact next action is push this
same admitted head, update PR #401 with the exact head/base and evidence, and
return `ACCEPTED` to the script-owned CI gate. Executor evidence does not claim
CI, independent review, merge, vertical probe, or project acceptance.
