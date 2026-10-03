# C109 accepted-main/API-resolution revalidation — 2026-09-12

This checkpoint changes only the admitted C109 evidence directory.  The
admission manifest and PRD are immutable; the referenced
`factory/docs/implementation-handoff.md` exists under `FACTORY_ROOT` and was
read before implementation.  C61/C83 refs and predecessor worktrees, C79
shared paths, production/test source, the host checkout and unrelated
worktrees were preserved.

Admission and ancestry:

- `project-control.py verify-work --type task --name audio-runtime-c109-characterize-browser-retirement-integration` returned `admitted` for the sole `audio-runtime` project.
- The isolated branch is `codex/audio-runtime-c109-characterize-browser-retirement-integration`, matching `prd.json.branchName` exactly.
- The accepted main is `d4766c3dbbf2c198142047ead4449d58dd47d485`.  A fresh `git fetch origin main` resolved `origin/main` to `b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`, which is integrated into the C109 HEAD `652453bfea05b0d09e5257c7cfbd65c38e54f93f`.
- The exact immutable candidates remain C61 `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83 `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`; preserved predecessor worktrees and refs were unchanged before/after analyzer execution.

Analyzer and causal repair:

- Required `main -> C61 -> C83` and reverse `main -> C83 -> C61` analyzers passed from clean committed source, with exact candidate refs and preserved worktrees unchanged.
- The required/control evidence is based on the admitted `d4766c3` synthetic contract.  Supplemental `reviewMainCompatibility` evidence records both current-main orders: C61 fails at `docs/architecture/architecture-size-baseline.json` with a modify/delete conflict, C83 first succeeds in the reverse order, and C109 aborts/cleans both conflict rehearsals without resolving them.
- The source-derived API ledger now reports zero C83 -> C61 API edges and `c83_requires_c61_api=false`.  Public imports resolve to `github.com/portpowered/go-agent-harness/go-agent-runtime/services/browserscenario` and `/wire`; no bare cross-package name or anonymous-function keyword is promoted to a dependency.
- `test_analyze.py` passed both focused regressions for anonymous functions, qualified/module-path resolution, and same-name cross-package symbols.  `verify.py --mode merge-orders`, `--mode determinism`, and `--mode all --write runs/final/verification.json` passed.
- All accumulated negative fixtures rejected as required, including changed/stale refs, reversed order, missing conflict/caller/preserved evidence, heuristic ledger, forbidden claims, zero-test discovery, cleanup failures, and caller-tree mutation.

Bounded public evidence:

- `run_public_checks.py --case browser-audio-tool --child-timeout 90 --aggregate-timeout 300` passed all 18 checks in 280.4s.  The workflow emitted `PROBE_TOOL_MARKER_9182` and `strict replay continuation`; the synthetic tree stayed clean and unchanged and all process groups were gone.
- `run_public_checks.py --case malformed-or-canceled --child-timeout 60 --aggregate-timeout 180` passed all 3 checks in 31.632s.  The structured negative control and normal/race cancellation checks completed with clean shutdown.
- The public reports are `runs/final/public/browser-audio-tool.json` and `runs/final/public/malformed-or-canceled.json`; both are bound to the exact accepted-main/C61/C83 tree and are validated by the all-mode verifier.

Handoff state:

- The prior sanitized C79/provider-audio rejection `ci-rejection-34732547786.json` and log hash remain unchanged and are not relabeled, waived, duplicated or repaired by C109.  No C109 CI-green, candidate acceptance, merge, vertical probe, hardware/acoustic proof or project-acceptance claim is made; all nine broad gates remain `OPEN`.
- Exact next action: commit the regenerated evidence, push the same admitted branch, update PR #495, and submit this exact head to the script CI gate without polling.  Any new C109 evidence defect remains with this task; the provider-audio terminal-drain repair remains with C79.
