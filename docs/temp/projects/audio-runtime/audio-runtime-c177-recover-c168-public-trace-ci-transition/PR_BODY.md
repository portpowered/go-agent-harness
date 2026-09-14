C177 recovery checkpoint for existing PR #503.

Adopted candidate: `21734b0b2f4a619ff22d8527e18480e7ba865a4b`.
Current `origin/main` was fetched and integrated in merge commit
`e3fc5db1cc0fa91e00aa9157d2d35e8c6265466c`; the C154 room-bound-grace
coverage failure from script run `34791475655` is therefore resolved by its
peer-owned mainline repair. No C177-owned repair was justified.

The later current-main tip `1b1c0296b9471b930c2b290f3bf6fa10559957de` was
also fetched and integrated in baseline merge
`f37dc5690fe56c59f73541e13175c3942b6a5599`; its delta is CI/Makefile/check
partition infrastructure and does not alter the C177-owned source paths.

Focused evidence passed: public trace normal/race, causal room-bound test,
three shipped SIGINT integration cases, all normal/coverage/race lanes of
`scripts/test-session-ci-regressions.sh`, Wire, architecture/size, and the
bounded six-case C146 replay matrix. The candidate is credential-free and
`git diff --check` is clean.

The legacy C108/C146 aggregate verifiers reject only the explicit C177-owned
SIGINT integration path as outside their older C108 scope; predecessor
verifier files were preserved.

This comment hands the existing PR to script CI. It is not a claim that CI,
independent review, or guarded merge is green.
