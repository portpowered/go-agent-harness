## Summary

- Retires room static/browser tool validation, ordering, cloning, composition, refresh and lifecycle decisions from the CLI adapters into the public `go-agent-runtime/services/roomcapabilities` contract and private implementation.
- Preserves host-only config/filesystem/stderr and WebMCP adaptation in thin CLI wrappers.
- Adds dedicated Wire construction, provider-neutral routing/refresh/lifecycle tests, coverage manifests, mutation oracles, and a credential-free external consumer.
- Reduces the two owned CLI production files from 334 to 153 physical lines (181-line reduction).
- Integrates current `origin/main` `3d3e72786ac6fc1fd47c7e029589e5117674b035` as merge `39af6805139ae45ac2a731a275805fa549e88bf1`, preserving startup, planning, and predecessor ancestry; rollback checkpoint is `f927de537eff2d499873b559ae03b12bfa4ce388`.

## Verification

- Focused normal/race runtime, CLI room, and CLI transport tests pass.
- Pinned lint/staticcheck, focused vet, coverage registration, verifier, normal/race room tests, external `GOWORK=off` consumer, credential-free room composition, non-room CLI regression, mutation oracles, and accumulated bounded regressions (`COUNT=3` normal/coverage/race) pass at evidence revision `c7fc6260`. The repository-wide changed-coverage command was intentionally interrupted before a result because it expanded into duplicated six-module CI.
- `verify.py --mode all` passes at the current-main merge with required ancestry, merged-main-relative scope, excluded-file, boundary, retirement, and mutation checks.

## Handoff status

The candidate source is checkpointed at merge `39af6805139ae45ac2a731a275805fa549e88bf1` over current `origin/main` `3d3e72786ac6fc1fd47c7e029589e5117674b035`; owned evidence was refreshed at `c7fc6260eb70d18c4f75a7df09aea1a37e92e23e`. The completed static job for prior pushed head `f927de537eff2d499873b559ae03b12bfa4ce388` and the bounded current-main recheck report exactly the C88 generated-Wire registration finding in both `make wire-check` and `make architecture-size-check`; C79 currently leases `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json` (PR #470 head `5ea7aec83731a0c168a84637218fc53be63189b`, still open with no reviewed guarded merge). After C79's reviewed guarded merge and release, integrate the accepted main it establishes, make only the demonstrated C88 registry/baseline changes, rerun the gates, and submit the changed head to Script CI. CI, independent review, guarded merge, and the fresh vertical probe remain pending.
