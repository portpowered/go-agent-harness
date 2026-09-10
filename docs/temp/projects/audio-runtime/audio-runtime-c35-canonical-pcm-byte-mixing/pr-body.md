## Summary

- Add exported `MixPCM16Bytes` in the existing mixer package, reusing codec Decode/Encode and `MixPCM16Samples` with bounded preflight validation.
- Route the legacy room frame adapter through the canonical byte operation and consume sorted queue prefixes only after successful mix/encode.
- Preserve literal PCM byte oracles, queue-preservation regressions, and the bounded public verifier.

## Provenance

- Tested implementation source: `e695f03e8dfe2197da2628753d8c2fe15aebfc69`
- Current integrated `origin/main`: `4e6cfab5d8d53b7db93c723e0a4e955bdc1c6f1d`
- Integration merge: `ddb79e40` (`Merge current main into C35 evidence repair`)
- Baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`; startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`; planning: `c61ee2774986c896560ee40a92441c914976000d`
- Source archive: `129894400` bytes, SHA-256 `969b0fdf93222ea04073a2b09da9d8fa6fcab71b5ea6b9cb256fe534a9f761b8`
- Consumer SHA-256: `a7e0bba1289f7d4f887be2dacc37da8aba45e77d94c1c3c39291326c80f529d0`
- Yui SHA-256: `832be88f7eac6d20d0cd1830706918b3c1450d8118c64605291227f61f594b9d`
- The verifier build-input manifest includes `docs/temp/projects/audio-runtime/audio-runtime-c35-canonical-pcm-byte-mixing/consumer/go.sum` (`0eb084ae04738236d00d96d849c231482df0549e2456fcf8637f4bceb96c6857`).

## Validation

- Full owned verifier `--mode all` from clean source `e695f03e` returned `ACCEPTED` in `32.637286s`; normal/race mixer and room tests, targeted vet, architecture-size-check, diff-check, consumer controls, and public replay controls all passed.
- Literal positive controls cover signed extrema, final clipping, short/empty tails, order independence, immutability, invalid dimensions and source limits. The independent mutated oracle exits nonzero as intended.
- Same-source public software replay passed `PROBE_TOOL_MARKER_9182`, strict continuation, clean shutdown, 18 wire events / 1 tool call, provider PCM 4,800 bytes (`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`) and rendered PCM 3,200 bytes (`7d2d8221eb8ec0be3da4a3ed518e1e183aa56e4ac0140ca0cf761068555805`).
- Interruption replay passed provider PCM 3,840 bytes (`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`), rendered PCM 3,360 bytes (`302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`) and the exact healthy 2,400-byte tail (`16508b8b42304d49869684c95e47c794b0eb9b54fd9137537dfaa4370097dfbf`). Tampered replay failed with a digest diagnostic; bounded TERM/KILL/reap controls passed.

## Handoff

The current-main merge and evidence cleanup are confined to the admitted C35 branch and owned evidence directory; `progress.txt` is no longer in the PR diff. This handoff claims no script-CI result, independent review, guarded merge, post-merge vertical acceptance, physical/acoustic proof, or project completion. The next bounded verifier run will record the evidence-only candidate revision and prove executable-input equality against the tested source before this same PR is handed to script CI.
