# Governing execution plan

Read factory/docs/handoff-plan.md and factory/docs/baseline-status.md. These
explain the architecture decisions and verified checkpoint. The original request
and acceptance in this directory govern scope; derived PRDs cannot weaken them.

1. Integrate the code baseline 3194edd97aed588f7cdf2f8c58a69ac21da4c9ad and the
   bootstrap revision pinned by the running factory into freshly fetched main in
   an isolated codex/ branch. Resolve actual conflicts, prove relevant behavior,
   preserve main's concurrent work, open a PR, require CI and independent review.
2. Measure remaining legacy CLI business logic and service ownership. Emit the
   smallest useful behavior slices with disjoint package ownership and explicit
   shared-composition dependencies. Preserve a working embedded/CLI execution path.
3. Finish canonical audio clocks/DSP/buffers, device ownership and physical tracing,
   bounded recording projections, and hermetic replay. Carry reported failures
   through characterization, targeted fixes and regression proof.
4. Complete remaining service-private ownership and thin CLI routing. Enforce
   architecture/size/complexity/generation/lint gates without baseline growth.
5. Independently validate the final immutable artifact through customer and
   engineering missions. Keep failed/blocked criteria open and feed exact evidence
   back to project leadership. Complete only against the immutable acceptance.

First-slice readiness is distinct from whole-project acceptance. Existing focused
checks are evidence, not a substitute for final release gates. Record exact base
and candidate SHAs, test results, limitations and rollback commits.
