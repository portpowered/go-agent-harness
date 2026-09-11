# C52: characterize PR438 room liveness

## Summary

This is an evidence-only characterization of the rejected PR438 hermetic room-liveness result. It contains no production, test, architecture, or CI changes.

- Candidate source: `bb29005d0bb545db5e08ffda0205929b021d4fc1` (the refreshed `origin/main` source used for the clean rerun).
- Comparison sources: planning main `7f73c8b3b4ebc99b55b8bb5e802beff024385407` and PR438 `823bd350fe5d11782c38bda87d7b7bfd7d89d7cd`.
- Preserved prior CI evidence: run `34562579355`, job `103148160889`, whose failure reported `silent_provider_timeout` at `browser_parity_test.go:220`.
- Canonical clean matrix: run `20260911T081500Z-compressed-archives`, 11 cells per revision, 22/22 passing within the declared bounds.
- Negative controls rejected incorrect cell selection, wrong-package execution, and survivor processes.
- Accumulated normal and focused race lifecycle regressions pass.

## Finding

The bounded clean matrix classifies the prior result as `NON_REPRODUCED`; it does not claim that hosted CI is flaky or absent. The retained trace comparison records only non-causal concurrent trace scheduling variance in five cases, with all behavioral checkpoints passing. The separately retained primary rerun report remains `REPORTED_UNDER_PAYLOAD` because it lacks raw command output and environment/source provenance.

Run the repository verification command before review:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/verify.py --mode all
```

This candidate is handed to script CI for the required broad gate. This evidence does not assert CI success and does not replace independent review.
