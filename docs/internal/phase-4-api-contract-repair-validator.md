# Phase 4 API Contract Repair Validator

## Subject Under Review

This validator reviews the candidate Phase 4 public API contract hardening
repair baseline. The baseline under review combines these repair lanes:

1. Audit reconciliation
2. Typed errors and stream repair
3. Provider capability discovery and local request validation repair
4. Dependency ownership, result contract, context, and lifecycle repair

The validator inspects the delivered repository state as an observable public
contract surface. It does not implement new feature behavior and does not close
rows from implementation intent alone.

## Checklist Rows Under Review

This report covers exactly these authoritative rows from
`docs/internal/checklist.md`:

| Checklist row | Required outcome cited from `docs/internal/checklist.md` |
| --- | --- |
| `P4-API-01` | Public blocking calls expose caller-controlled cancellation and timeout behavior anywhere they wait, perform external work, relay streams, replay fixtures, or flush recordings. |
| `P4-API-02` | Public gateway, provider, CLI, replay, and validation boundaries expose typed caller-actionable errors that support `errors.Is` or `errors.As` instead of requiring string parsing. |
| `P4-API-03` | Public result, buffer, session, and stream APIs expose unambiguous outcomes for empty success, cancellation, partial success, closed or drained state, and terminal failure. |
| `P4-API-04` | Public APIs expose provider capabilities for tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific configuration with supported, unsupported, and unknown states. |
| `P4-API-05` | Public streaming APIs preserve terminal events, cancellation, replay mismatch, partial output, and typed error details through observable stream events or documented result surfaces. |
| `P4-API-06` | Unsupported stateless and session request features fail locally before provider execution with inspectable errors identifying feature, provider, requested mode, and capability state. |
| `P4-API-07` | Constructors, provider runtime seams, prompt resolution, filesystem, environment, process, transport, network, and time dependencies are caller-owned, injected, side-effect free, or explicitly documented as open work. |
| `P4-GATE-01` | The Phase 4 public API contract hardening baseline is review-ready only when docs, examples where present, audit rows, public APIs, deterministic tests, and reviewer-runnable commands describe the same current contract. |

## Evidence Rules

Every row finding must be based on public, reviewer-verifiable evidence. Public
evidence can include exported declarations, public docs, examples where present,
deterministic tests, emitted events, CLI output contracts, or local commands
that require no live provider credentials.

CI success alone is not sufficient row closure. Typecheck, lint, and tests can
support a pass only when the cited command proves the specific public contract
under review. Docs or audit prose alone cannot close an implementation row
unless the row is explicitly documentation-only.

When evidence is absent, stale, contradictory, dependent on live credentials, or
not tied to a public contract surface, the row must be marked `fail` or
`uncertain` and must include exact future repair work.

## Row Finding Shape

Each row finding must use this shape:

### `[Checklist row]` - `[Area]`

- `verdict`: `pass` | `fail` | `uncertain`
- `closure decision`: `may mark complete` | `remains open`
- `public evidence`:
- `affected files / declarations`:
- `docs, examples, tests, audit, and API alignment`:
- `reviewer commands`:
- `exact repair work for non-pass rows`:

## Reviewer Commands

The final validator pass must cite exact commands next to each row. Commands
must be deterministic and must not require live credentials, external network
access, private local state, or hidden setup. The root quality commands available
for supporting evidence are:

```sh
make typecheck
make fmt
make vet
make test
make test-integration
make test-regressions
```

Those commands prove only their observable behavior. They do not, by themselves,
prove row closure without the row-specific public evidence above.

## Current Story Status

This initial scope pass establishes the Phase 4 validator subject, checklist row
coverage, evidence rules, and required finding shape. Row-by-row convergence
findings are intentionally deferred to the later validator stories so each pass
can compare the current public API, docs, examples where present, audit rows,
tests, and deterministic command evidence at the correct depth.
