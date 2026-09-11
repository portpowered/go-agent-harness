# C52: characterize PR438 room liveness

## Summary

This is an evidence-only characterization of the rejected PR438 hermetic room-liveness result. It contains no production, test, architecture, or CI changes.

- Candidate source tested: `c05d910a44c306a30e55f8478931e34503fd890e`, including refreshed `origin/main=2456a5d1594e73faf85e1132d6050735bc3e4710`; the final handoff commit is an evidence-only descendant with unchanged executable/test inputs.
- Comparison sources: planning main `7f73c8b3b4ebc99b55b8bb5e802beff024385407` and PR438 `823bd350fe5d11782c38bda87d7b7bfd7d89d7cd`.
- Preserved prior CI evidence: run `34562579355`, job `103148160889`, whose failure reported `silent_provider_timeout` at `browser_parity_test.go:220`.
- Canonical matrix: run `20260911T143300Z-review52-repair-v2`, 11 cells per revision, 22/22 passing in 69.655 seconds within the 900-second aggregate bound.
- Six negative controls rejected ambient environment forwarding, incorrect cell selection, wrong-package execution, timeout survivors, normal-exit descendants, and mismatched archive reuse.
- Accumulated normal and focused race lifecycle regressions pass from the candidate source; the final fail-closed integrity probes reject cleanup-field deletion, raw-count tampering, duplicate or transformed checkpoints, tampered CI metadata, selected behavior failures hidden as selection errors, and mismatched retained archives.
- The runner validates reused archive contents, removes run-local caches after child reaping, and uses bounded deferred cleanup only to preserve room outcome evidence after an assertion abort; production and test sources remain untouched.

## Finding

The bounded clean matrix classifies the prior result as `NON_REPRODUCED`; it does not claim that hosted CI is flaky or absent. A pre-final local diagnostic attempt observed one planning-main `gomaxprocs-4` peer-cancel assertion at `browser_parity_test.go:224`; the fresh canonical matrix after the repairs did not reproduce it, so no causal attribution is made. The separately retained primary rerun report remains `REPORTED_UNDER_PAYLOAD` because it lacks raw command output and environment/source provenance. The evidence does not claim production repair, CI success, review, merge, vertical acceptance, or project completion.

Run the repository verification command before review:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/verify.py --mode all
```

This candidate is handed to script CI for the required broad gate. This evidence does not assert CI success and does not replace independent review.
