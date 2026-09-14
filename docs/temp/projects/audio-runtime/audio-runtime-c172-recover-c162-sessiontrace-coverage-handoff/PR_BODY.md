## C172 recovery handoff

This is the same C162 candidate recovery on PR #519, not a recreated implementation. The exact pushed C162 head `67865c27ac4c065f3e8833d48ad2b7cea4c329a2` was adopted without rewriting history. C154's room-bound repair was current-head green, independently reviewed, and guarded-merged as `8490f8dcad63adde99036016e1e7ffd9ecf61e34`; no C154 path is authored here.

The adopted candidate was non-rewritingly merged with accepted main (`1a547d531`) and then with fresh main `1b1c0296b9471b930c2b290f3bf6fa10559957de` (`f1de875ee`). It preserves startup `8bdafc7f`, planning main `2c79ec6a`, C162 `67865c27`, and the C153/C158/C162 history. The stale-base review finding from `work-review-85` and the prior C153 50/100 negative finding remain recorded in `evidence.md`.

Bounded focused proof passes: room-bound normal/nomicrophone coverage, the C162 100-trial canceled-close oracle normal/nomicrophone coverage, targeted and full sessiontrace normal/race/coverpkg, retained-path/redaction/no-overwrite controls, the C154 completion regression, accumulated normal/coverage/race session regressions, credential-free scheduled-audio/tool replay, vet, Wire, fmt, architecture-size, pinned lint/staticcheck and diff-check. No assertion, timeout, coverage floor or baseline was weakened.

Script CI owns broad current-head polling. This executor claims no current-head CI result, independent review, guarded merge, vertical acceptance or project acceptance. Submit this changed exact SHA once to the script-owned gate; retain the same task for any exact rejection.
