# C109 browser-retirement integration characterization (v2 evidence)

This directory is the complete owned surface for `audio-runtime-c109-characterize-browser-retirement-integration`.
It is evidence-only.  The analyzer makes disposable detached worktrees, records
the C61/C83 provenance and merge-order behavior, and removes those worktrees
before returning.  It never edits either predecessor worktree, the host
checkout, shared registries, or candidate source files.

The accepted main revision is `d4766c3dbbf2c198142047ead4449d58dd47d485`.
The preserved candidate revisions are C61
`8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83
`22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
The final provenance also records fetched `origin/main`
`1a8467246c6607a06ffc7289075da2595724ce8b` as the newer integrated review
main; the branch diff relative to that review main remains C109-owned only.
The required and control synthetic rehearsals intentionally remain based on
the admitted accepted main, as required by the PRD.  Because the newer review
main deletes the architecture baseline that C61 still modifies, the final
reports also retain a supplemental current-main compatibility rehearsal: it
records the exact modify/delete conflict and abort/cleanup result without
resolving or promoting it.

Run the analyzer from the repository root, writing outside the checkout while
iterating:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/analyze.py \
  --main d4766c3dbbf2c198142047ead4449d58dd47d485 \
  --c61 8e8177c031a7b3b9322d712af19970e13fa7a1bc \
  --c83 22cc6769aaf06d1e2c1275b064cc7ec29de3e371 \
  --order c61-c83 --output-dir /tmp/c109-required
```

The reverse control is run with `--order c83-c61 --expect-control`.  The
verifier accepts the two output directories and deliberately fails closed for
changed SHAs, reversed sequence ownership, unowned paths, missing merge
evidence, and prohibited candidate-acceptance claims.  `run_public_checks.py`
is the bounded software-only test runner; it records command, timeout,
credential-scrub, process-group, test-discovery, shipped-report, and
effect-cleanup evidence without claiming hardware or acoustic coverage. The
default child/aggregate bounds are 90/300 seconds; accumulated race coverage
is split into bounded transport, integration, devices, and OpenAI children
without relaxing the checked-in test patterns. A caller-provided `--tree`
must be the exact clean main -> C61 -> C83 synthetic tree and is rejected if a
child mutates it.

The C79-owned `scripts/wire-packages.txt` and
`docs/architecture/architecture-size-baseline.json` remain untouched.  The
6,400-sample integration loss remains assigned to C79/provider audio and is
not duplicated, repaired, or relabeled here.  C61 and C83 remain unmerged and
unaccepted until their own task/review/CI gates are resolved.

The checked-in final bundle includes separate `ci-attribution.json` evidence,
source-derived caller/API spans, exact committed-head provenance, both merge
orders, and the two public reports.  `verify.py --mode all` is the accumulated
gate for determinism, report/public negative controls, causal attribution, and
handoff readiness; it does not claim that candidate CI is green or that the
project is accepted.

The analyzer resolves API references from complete package/module identity:
anonymous function bodies do not become declarations, qualified imports are
matched to their exact imported package, same-name symbols in another package
are not treated as dependencies, and public module paths are not duplicated.
The checked-in `test_analyze.py` regressions cover those cases plus the admitted
public-command argument contract.  The final evidence source checkpoint is
`b9f6f0742131a78618df8bfcfe93eb8b78461364`.

## Review repair and current-main integration

Independent review `work-review-37` found two actionable evidence defects:
the admitted public commands omitted the runner's required `--output`, and
the evidence did not contain the newer review-time `origin/main`.  Commit
`0c93c4ce0ba30599d7ae92fdf7b81cc0b5f2bcae` integrates review main without
rewriting the C109 history; `b9f6f0742131a78618df8bfcfe93eb8b78461364` makes
`--output` optional for the declared commands and adds a regression test.  An
explicit output path still retains the JSON report used by the final bundle.

Fresh required/control analyzer outputs both record accepted main
`d4766c3dbbf2c198142047ead4449d58dd47d485`, review main
`1a8467246c6607a06ffc7289075da2595724ce8b`, source HEAD `b9f6f074`, unchanged
C61/C83 refs/worktrees, and complete cleanup.  The required output hashes are
`provenance 2e4bcecbe0a5c7e274f816464aae73d9dcfd53e43f6d11379cfc2d4d0347b686`,
`ledger 44dcb4c42c53f52ff7544eba9939436822960d2153aeee79e1e88ac9efa79209`,
`merge-orders 5d10f96910d69786a578097c14a7d88b561a2fc679712c89c682fadf75affd44`,
`report b769eb304057d4f97d59e1a8f6abf59187ada15dbdd4cc9207460808c569d01d`,
and `run-manifest 4690e2cef53541b4e588fcf7a2800d95a76ce0c055f1f0e5a3544fe47044c3c8`.
The control merge/report/manifest hashes are
`774be9b078a622fb82ccd0652685034f5638894ebdeddc1bc2c8b5a269fe071a`,
`cbfab1f02266996c56eb8559cda54f15c477b60fc0134f4da811450530393614`, and
`c0aab79c9b60e0d45e3f40d2396b9492a38ea96eb2f3dd805e39c16629d1a562`.

The repaired public runner passes the explicit positive case `18/18` in
`275.452s` and malformed/canceled case `3/3` in `30.409s`; their retained
report hashes are `0d367f5921101c52e98cf1308f6ec96f2d384429db4b02c388e77838542e6412`
and `d70da2bafbe2f2f5f87a46b31a2581b24a74060f25ab0e9e3d50b66ca9546e40`.
`verify.py --mode all` passes all eight checks and 19 negative fixtures; its
retained verification hash is
`c99e255602ed6c81d8dc31b33767337ef4a918b818dff2ce10f7a4b83a5aaccb`.
The prior hosted provider-audio terminal-drain rejection remains preserved
under C79 ownership and is not repaired or relabeled here.  C61/C83 and all
nine broad project gates remain open.

## Aggregate-budget runner repair

The first fresh current-source public run after the latest CI rejection passed
17/18 checks but exhausted the 300-second aggregate budget before the shipped
credential-free workflow could finish: all preceding checks passed, the final
child received only four seconds, terminated cleanly, and left no descendants.
This was an owned evidence-runner scheduling defect, not a C61/C83 or provider
audio failure. Commit `34a4a9404e1238b840c83c7acf7b93c14fa1abca` schedules the
required shipped workflow first without changing its command, assertions,
credential scrubbing, child timeout, or aggregate timeout.

The repaired runner passes the exact positive case 18/18 in 265.785 seconds
under 90/300 bounds and the malformed/canceled case 3/3 in 35.286 seconds under
60/180 bounds. The regenerated final public reports are bound to runner SHA-256
`04161bdc2d5f838b9af54b58c6b95f37fb5e1c515cffbb0693908b194fcc694a`, and the
final verifier passes all eight checks and 19 negative controls. This remains
software-only evidence; no C61/C83 merge, review, probe, acceptance, hardware,
acoustic proof, or broad project gate is claimed.

## Current script-CI rejection

The changed C109 head `a2fc31af4f59c2aa8b7c3ee6d4e61b6231c597c2` was checked by
PR #495 run `34731461393`. Coverage, hermetic, unit, static, race, WebMCP,
macOS, and Windows passed. Integration failed only in
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
at `agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:183`:
the remote playback deadline expired with `462720` rendered samples,
`174391` expected samples, `final_marker=false`, zero queued/dropped/overflow/
discarded samples, and the child still running. The exact sanitized record is
`ci-rejection-34731461393.json`.

This is the existing C79/provider-audio terminal-drain ownership boundary, not
a C109 evidence defect. The exact local control passed once in `15.867s`, but
that does not relabel or waive the CI failure. C109 did not change production,
integration, device, transport, Wire-registry, or architecture-baseline paths;
the changed evidence checkpoint is to be pushed and submitted to script CI,
while C79 retains the necessary source repair.

## Latest script-CI rejection

The exact current-head rejection for PR #495 is run `34732547786`, job
`103657892852`, at head `0185f03afbd2bf81e6d2cdb32d3a1586576aad7d`. Eight
other required lanes passed. Integration failed only in
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
at `agent-cli/test/integration/session_tool_audio_remote_e2e_test.go:183`:
the remote playback deadline expired with `464160` rendered samples,
`150871` nonzero samples, `174391` expected samples, `final_marker=false`,
`967` callbacks, an underflow trace, zero queued/dropped/overflow/discarded
samples, and the child still running at timeout. The complete sanitized
record is `ci-rejection-34732547786.json`; the retrieved job log SHA-256 is
`3ec0d3acb03405b376f2ef7a6e4fe32fbfdd968939811d9e2ae3fdb73dbc13a1`.

The exact same local control passed in `15.617s`; this is preserved as a
non-waiver only. The failure remains the C79/provider-audio terminal-drain
family, with no C109 repair authorization. The current C109 candidate stays
evidence-only and must be resubmitted to script CI after this evidence
checkpoint; C61, C83 and all nine project gates remain unmerged/open.
