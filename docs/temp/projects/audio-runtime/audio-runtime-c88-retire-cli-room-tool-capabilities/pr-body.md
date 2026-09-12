## Summary

- Retires room static/browser tool validation, ordering, cloning, composition, refresh and lifecycle decisions from the CLI adapters into the public `go-agent-runtime/services/roomcapabilities` contract and private implementation.
- Preserves host-only config/filesystem/stderr and WebMCP adaptation in thin CLI wrappers.
- Adds dedicated Wire construction, provider-neutral routing/refresh/lifecycle tests, coverage manifests, mutation oracles, and a credential-free external consumer.
- Reduces the two owned CLI production files from 334 to 153 physical lines (181-line reduction).
- Integrates current `origin/main` `59af6325614d80173447fe2018a0471e27b4e7b1` as merge `d3b53e6ee47d7467de26dcc75eb64484775d32e0`, preserving startup, planning, and predecessor ancestry.

## Verification

- Focused normal/race runtime, CLI room, and CLI transport tests pass.
- Pinned lint, staticcheck, vet, changed coverage, coverage registration, external `GOWORK=off` consumer, credential-free room composition, non-room CLI regression, mutation oracles, and accumulated `COUNT=3` normal/coverage/race regressions pass.
- `verify.py --mode all` passes with required ancestry, merged-main-relative scope, excluded-file, boundary, retirement, and mutation checks.

## Handoff status

The candidate is checkpointed at implementation/evidence revision `78649197beb29e1de42e703033ecfe3adeb899e0`. Wire and architecture gates each report exactly the C88 generated-Wire registration finding and have one shared prerequisite: C79 currently leases `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json` (PR #470 head `5e511ab38b38547b5e006c7052bbd626ab43b5d1`, still open with no reviewed guarded merge). After C79's reviewed guarded merge and release, integrate the accepted main it establishes, make only the demonstrated C88 registry/baseline changes, rerun the gates, and submit the changed head to Script CI. CI, independent review, guarded merge, and the fresh vertical probe remain pending.
