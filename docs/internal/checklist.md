# Internal Checklist

## Phase 1 - Authoritative Checkout Baseline

This checklist is the Phase 1 inventory for the authoritative checkout baseline.
The convergence validator must cite these item IDs directly when it records
`pass`, `fail`, or `uncertain` evidence.

| Item ID | Area | Required outcome | Primary evidence surfaces |
| --- | --- | --- | --- |
| `P1-CHK-01` | Checklist source | The repository contains an authoritative Phase 1 checklist that reviewers can cite directly during convergence validation. | `docs/internal/checklist.md` |
| `P1-CHK-02` | Checklist convergence | The convergence report maps the repaired Phase 1 baseline to the relevant checklist items and required outcomes with explicit evidence and affected surfaces. | `docs/internal/phase-1-authoritative-checkout-convergence-report.md`, `README.md`, `Makefile`, `.github/workflows/ci.yml`, `go.work`, `docs/architecture/*` |
| `P1-ARCH-01` | Root workspace baseline | The root workspace contract is coherent across the repository root, `go.work`, the root `Makefile`, and the workspace architecture documentation. | `README.md`, `go.work`, `Makefile`, `docs/architecture/workspace.md` |
| `P1-ARCH-02` | CI baseline | GitHub Actions delegates to the same deterministic root validation pipeline contributors run locally. | `.github/workflows/ci.yml`, `Makefile`, `docs/architecture/workspace.md` |
| `P1-ARCH-03` | Dependency and audit alignment | Dependency direction and contract-gap audit documents describe the same Phase 1 module architecture exposed by the repository. | `docs/architecture/dependencies.md`, `docs/architecture/contract-gap-audit.md`, module `README.md` files |
| `P1-ARCH-04` | README set coherence | Module and docs index READMEs describe the current repository layout and entrypoint paths without stale directory references. | `README.md`, `agent-cli/README.md`, `agent-cli/docs/README.md`, `go-agent-loop/README.md`, `go-llm-gateway/README.md` |
| `P1-MERGE-01` | Split-brain resolution | The authoritative Phase 1 baseline clearly identifies whether local `main`, `origin/main`, and the prior convergence branch still compete or whether any remaining divergence is only an explicitly documented stale ref. | local `main`, `origin/main`, `phase-1-authoritative-workspace-convergence`, `phase-1-authoritative-checkout-reconciliation` |
| `P1-MERGE-02` | Reviewer readiness | The current head is mergeable, the convergence report records an overall verdict, and any remaining repair work is explicitly scoped from observable evidence. | `docs/internal/phase-1-authoritative-checkout-convergence-report.md`, PR mergeability state, root validation commands |
