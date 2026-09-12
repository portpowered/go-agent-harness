## Summary

- Retires room static/browser tool validation, ordering, cloning, composition, refresh and lifecycle decisions from the CLI adapters into the public `go-agent-runtime/services/roomcapabilities` contract and private implementation.
- Preserves host-only config/filesystem/stderr and WebMCP adaptation in thin CLI wrappers.
- Adds dedicated Wire construction, provider-neutral routing/refresh/lifecycle tests, coverage manifests, mutation oracles, and a credential-free external consumer.
- Reduces the two owned CLI production files from 334 to 153 physical lines (181-line reduction).

## Verification

- Focused normal/race runtime, CLI room, and CLI transport tests pass.
- Pinned lint, staticcheck, vet, coverage registration, external `GOWORK=off` consumer, credential-free room composition, non-room CLI regression, and mutation oracles pass.
- `verify.py --mode all` passes with required ancestry, scope, excluded-file, boundary, and retirement checks.

## Handoff status

The candidate is checkpointed at implementation revision `a3cc66ecac39af585fad0c89ee0b95b7a98d5852`. Full architecture and Wire gates have exactly one shared prerequisite: C79 currently leases `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`. After C79's reviewed guarded merge and release, integrate accepted main, make only the demonstrated C88 registry/baseline changes, rerun the gates, and submit the changed head to Script CI. CI, independent review, guarded merge, and the fresh vertical probe remain pending.
