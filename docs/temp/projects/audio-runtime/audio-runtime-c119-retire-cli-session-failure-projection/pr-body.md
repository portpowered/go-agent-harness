## Summary

- Extract session failure normalization, provider-close synthesis, cancellation/non-terminal exclusion, output-state derivation, continuation projections, unsupported-tool diagnostics, synchronized first-observation state, and original-error preservation into go-agent-runtime/services/sessionfailure.
- Add the dedicated public Wire seam and retain an exactly 86-line Deprecated CLI forwarding adapter; delete the 246-line legacy implementation.
- Add the separate GOWORK=off consumer, adapter regressions, bounded causal-mutation verifier, and credential-free shipped-yui replay runner.

## Focused evidence

- go test ./go-agent-runtime/services/sessionfailure/... -count=1: 22 passed.
- go test -race ./go-agent-runtime/services/sessionfailure/... -count=3: 66 passed.
- Adapter-focused race tests: 12 passed; combined focused legacy/adapter regressions: 30 passed.
- verify.py --mode positive-and-two-mutations: accepted; both targeted mutants fail with --- FAIL: output.
- External consumer with GOWORK=off: passed and proves Wire isolation, errors.Is, errors.As, defaults, exclusions, close output, and continuation projection.
- run.py --case provider-error --case synthesized-provider-close --case cancellation-negative --case healthy-audio-tool-continuation --child-timeout 60 --aggregate-timeout 240: accepted; pinned fixtures, bounded process groups, provider failure/close terminal manifests, cancellation negative, and healthy audio/tool output/effect.
- session_diagnostics_failure.go is absent; adapter is 86 lines; accepted-main retirement accounting is 246 CLI production lines; excluded caller/shared fingerprints remain unchanged.

## Handoff

This head is based on accepted main 3963bc3566da24f8214634c17a9d0f79a6724171 and preserves startup ancestry. scripts/wire-packages.txt and docs/architecture/architecture-size-baseline.json remain untouched because C79 still holds their active lease; C119-05 must integrate current origin/main only after C79's reviewed guarded merge and explicit release. The executor has not polled or claimed Script CI green.
