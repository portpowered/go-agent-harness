## Summary

- Add exported `MixPCM16Bytes` in the existing mixer package, reusing codec Decode/Encode and `MixPCM16Samples` with bounded preflight validation.
- Route the legacy room frame adapter through the canonical byte operation and consume sorted queue prefixes only after successful mix/encode.
- Preserve literal PCM byte oracles, queue-preservation regressions, and the bounded public verifier.

## Provenance

- PR head: `a209e2bef58815d246cee1f2cf69dd6ba2cb2ac0`
- Tested implementation/evidence source: `bcd22224c3caab89c04ea567710754c07917624e`
- Main integration merge: `f9ed552d92c93546b0805e663868f1d71f9241b5`
- Integrated `origin/main`: `84f0a9d308729e38da8479f87c3518f1634bad82`
- Baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`; startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`; planning: `c61ee2774986c896560ee40a92441c914976000d`
- Source archive: `128819200` bytes, SHA-256 `2775457d2bd92a4d81fd768e5aeb7eba942cf2685bb0e02e8e85a3d6f59d278e`
- Consumer SHA-256: `a7e0bba1289f7d4f887be2dacc37da8aba45e77d94c1c3c39291326c80f529d0`
- Yui SHA-256: `97f1d63e421d0a667b4bc85094ff5c79a8b002011e9e9fbbfdaf7b05eced486b`

## Validation

- Full owned verifier from clean tested source returned `ACCEPTED` in `34.973202s` with 27 bounded checks.
- Mixer/room normal and race suites discovered 18 tests each and passed; targeted vet, `git diff --check`, and architecture-size-check passed at 181 packages / 1,866 files / 27,568 functions.
- Literal positive controls cover signed extrema, final clipping, short/empty tails, order independence, immutability, invalid dimensions and source limits. The independent mutated oracle exits nonzero as intended.
- Same-source public software replay passed `PROBE_TOOL_MARKER_9182`, strict continuation, clean shutdown, 18 wire events / 1 tool call, provider PCM 4,800 bytes (`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`) and rendered PCM 3,200 bytes (`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`).
- Interruption replay passed provider PCM 3,840 bytes (`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`), rendered PCM 3,360 bytes (`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`) and the exact healthy 2,400-byte tail (`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`). Tampered replay failed with a digest diagnostic; bounded TERM/KILL/reap controls passed.
- The final PR head is an evidence-only descendant of the tested source. The exact verifier executable/build-input path comparison exited 0; no executable inputs changed after the tested run.

## Handoff

Factory admission is verified for the sole `audio-runtime` project and the branch matches `prd.json`. The current merge conflict is resolved in this isolated worktree; no host checkout, second project, acceptance waiver, or peer-path mutation was used. This handoff claims no script-CI result, independent review, guarded merge, post-merge vertical acceptance, physical/acoustic proof, or project completion. Submit this changed head to the script-owned current-head CI gate; any exact rejection returns to this executor for causal repair.
