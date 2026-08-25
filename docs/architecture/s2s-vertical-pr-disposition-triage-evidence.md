# s2s vertical PR disposition triage — raw evidence appendix (2026-08-25)

Companion to §9 of `docs/architecture/s2s-program-status-2026-08-17.md` and the four
`s2s-*-recut-spec.md` documents in this directory. Every claim in the disposition table
traces to a command below; every command is re-runnable verbatim against the recorded
refs.

Environment: worktree of `portpowered/go-agent-harness`, fresh `git fetch origin --prune`
immediately before analysis.

```bash
git fetch origin --prune && git rev-parse origin/main
# b91bd65c0ce48bd20eaffb5a2dff9952bef33340   (= local HEAD b91bd65 at analysis time)
```

## 1. PR metadata (gh pr view, fetched 2026-08-25)

| PR | head branch | head SHA | base | mergeStateStatus | mergeable |
|----|-------------|----------|------|------------------|-----------|
| #165 | s2s-v2d-audio-in-multi-utterance | `2090fbda32772c5fb918859a3fb29d90ecfa5eeb` | main | DIRTY | CONFLICTING |
| #166 | s2s-v6d-error-malformed-response | `c61abdda5c8f02adfc74529ce20d286c90cb86b3` | main | DIRTY | CONFLICTING |
| #167 | s2s-v2e-audio-in-truncated | `79a54e5790a417bfaaf0807a05f668426b94c9ad` | main | DIRTY | CONFLICTING |
| #168 | s2s-v2c-audio-in-silence | `266f220f6561e5fa9603ae04b22bfef75cfa7ff6` | main | UNSTABLE | MERGEABLE |

Repro:

```bash
gh pr view <N> --json number,title,state,mergeStateStatus,mergeable,baseRefName,headRefName,headRefOid,statusCheckRollup
```

Check rollups on each head (2026-08-25):

| PR | check | status/conclusion | run URL |
|----|-------|-------------------|---------|
| #165 | ci | COMPLETED / SUCCESS | https://github.com/portpowered/go-agent-harness/actions/runs/32817061323/job/97707382229 |
| #165 | CI (hermetic) | COMPLETED / SUCCESS | https://github.com/portpowered/go-agent-harness/actions/runs/32817061323/job/97707382077 |
| #166 | ci | COMPLETED / SUCCESS | https://github.com/portpowered/go-agent-harness/actions/runs/32808807424/job/97684073182 |
| #166 | CI (hermetic) | COMPLETED / SUCCESS | https://github.com/portpowered/go-agent-harness/actions/runs/32808807424/job/97684073422 |
| #167 | ci | COMPLETED / FAILURE | https://github.com/portpowered/go-agent-harness/actions/runs/32813309442/job/97709610359 |
| #167 | CI (hermetic) | COMPLETED / FAILURE | https://github.com/portpowered/go-agent-harness/actions/runs/32813309442/job/97709610521 |
| #168 | ci | COMPLETED / FAILURE | https://github.com/portpowered/go-agent-harness/actions/runs/32815156735/job/97701834238 |
| #168 | CI (hermetic) | COMPLETED / FAILURE | https://github.com/portpowered/go-agent-harness/actions/runs/32815156735/job/97701834362 |

## 2. Changed files vs merge base + blob-for-blob compare with origin/main

Method:

```bash
MB=$(git merge-base origin/main <head>)
git diff --name-only "$MB" <head>
git rev-parse <head>:<file>        # PR-side blob
git rev-parse origin/main:<file>   # main-side blob (nonzero exit => ABSENT_ON_MAIN)
```

Note: `git rev-parse <ref>:<path>` echoes an unresolvable argument on stdout with a
nonzero exit; classification must key on exit code, not stdout emptiness.

Merge bases: #165/#167/#168 → `81267d6e4a52`; #166 → `b08c6da64293`.

### PR #165 (6 files)

| class | file | PR blob | main blob |
|-------|------|---------|-----------|
| ABSENT_ON_MAIN | agent-cli/test/integration/s2s_v2d_multi_utterance_test.go | a963677a35d4d795865f10435e09fcdf82e2bb4c | — |
| ABSENT_ON_MAIN | agent-cli/test/integration/testdata/s2s-v2d/s2s_v2d_multi_utterance.session.json | 23a9dcaa8cd79cfcb57a327d59fb73ac27619ba2 | — |
| ABSENT_ON_MAIN | agent-cli/test/integration/testdata/s2s-v2d/s2s_v2d_multi_utterance_merged.session.json | 9a734f1572803e08871a3b06176cc7e837c6e369 | — |
| ABSENT_ON_MAIN | agent-cli/test/integration/testdata/s2s-v2d/scenarios/s2s_v2d_multi_utterance.scenario.json | 53427aa81fc1a3bbfdde5b70a084b0ac89b4efdc | — |
| ABSENT_ON_MAIN | agent-cli/test/integration/testdata/s2s-v2d/scenarios/s2s_v2d_multi_utterance_missegmented.scenario.json | f0d088266d83525045f87122b401eeca4cd61543 | — |
| DIVERGENT | go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go | 43833e41f6361df2c48e33cf0e8575bf38e88875 | 603e23b307e5f1424309913bef84c4b7df2e3778 |

The one DIVERGENT file differs only in the exact-count constant (`23` on main vs `22`
on the PR head); verified via `git show <blob>` of both versions.

### PR #166 (6 files)

| class | file | PR blob | main blob |
|-------|------|---------|-----------|
| DIVERGENT | agent-cli/internal/cli/probe.go | 7b2b4a3a4c9f2eab40804ff8eb0285566e1d9e00 | 6e858b58f259d9cf98e6f61897b49995810031a9 |
| DIVERGENT | agent-cli/internal/cli/probe_test.go | c9fd2580cc99583252fe64e50860f1237f6c5b43 | 22b001fa780628ee278e88bfa34ac4278995b0e6 |
| ABSENT_ON_MAIN | go-agent-loop/pkg/probe/scenario_error_malformed_response.go | aa4911d2c9d857b793efd0cc91e69d58081c8fa6 | — |
| DIVERGENT | go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go | 43833e41f6361df2c48e33cf0e8575bf38e88875 | 603e23b307e5f1424309913bef84c4b7df2e3778 |
| ABSENT_ON_MAIN | go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6d-error-malformed-response-healthy-control.session.json | eb328a65f19ac37677059be4953e853426348ce9 | — |
| ABSENT_ON_MAIN | go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v6d-error-malformed-response-malformed.session.json | f1e308c63d90d84ee62880ed6290e856a3656fe0 | — |

### PR #167 (14 files)

| class | file | PR blob | main blob |
|-------|------|---------|-----------|
| DIVERGENT | agent-cli/internal/cli/probe.go | 85d40508d74d7cdbdae20449ebe0b9f477c7cf0c | 6e858b58f259d9cf98e6f61897b49995810031a9 |
| DIVERGENT | agent-cli/internal/cli/probe_test.go | e6c82dbf5b46a7220d6153b9938694a813ecc1f7 | 22b001fa780628ee278e88bfa34ac4278995b0e6 |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_16k.session.json | d42a6e4355cc7d07fa06ba03b5169cf8c65f2748 | — |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_24k.session.json | c3608b4f6471b4f955a906f1a83f48f92f80ea4f | — |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-fixtures/s2s_v2e_audio_in_truncated_uncommitted.session.json | 0afc1bdeb654de479ddf7a0262f6e407cea34324 | — |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-16k.scenario.json | a04bba5ea316d27aa44ec053fbbc5c4eb4a37 | — |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-24k.scenario.json | 6f86ce138499c1e3fc726f6089aa29a006c2dc80 | — |
| ABSENT_ON_MAIN | agent-cli/internal/cli/testdata/probe-scenarios/s2s-v2e-audio-in-truncated-uncommitted-negative.scenario.json | 704186ca2c2c35929e5cd7ee17f671af7f1ff0a4 | — |
| DIVERGENT | docs/architecture/s2s-program-status-2026-08-17.md | 5c5bd63717dd0b14317dc502972ab8073c22585a | b01665478a87f21041a8fc92cbf5b388ca3bc2a3 |
| ABSENT_ON_MAIN | docs/architecture/s2s-v2e-audio-in-truncated-proof.md | 3c8243b4255e15f985092a2b5495f9c5f33cb4c7 | — |
| DIVERGENT | go-agent-loop/pkg/probe/deadguard.go | 556985d985191a9d1356512cc40dfea44b3a2cce | 76a58c1803fe8f163fdf66ccd3865a5c45fa2f38 |
| DIVERGENT | go-agent-loop/pkg/probe/expect.go | dccc9914d00585c4d7b2e64991352ee291c89817 | fcd66c81162f1e6a10f7056b6422ab3cbd7902a2 |
| DIVERGENT | go-agent-loop/pkg/probe/expect_test.go | fbbb4a61c473c9880f22a56f8a862c0de6e88968 | e042c4081941efae42407ed6732ff9916bad4da0 |
| DIVERGENT | go-agent-loop/pkg/probe/scenario_measurable.go | eb0dc79c2ccac1a8f968969df010aad58ce089d4 | d3bf5068f13d91879ff0edc9ec89c47d88031061 |

### PR #168 (3 files)

| class | file | PR blob | main blob |
|-------|------|---------|-----------|
| DIVERGENT | agent-cli/internal/services/session_audio_in.go | 5f7e67b0c4e223499a812db76ef82c26956d9fdd | 4d70a03b67db192cff360b62fc56ec6cca9059a2 |
| DIVERGENT | agent-cli/internal/services/session_audio_in_internal_test.go | 0037c2b4f533a0c618244e481652694830f2d7b9 | b58d52a36280132def820be50d36219f7518fc2a |
| ABSENT_ON_MAIN | agent-cli/test/integration/session_audioin_silence_test.go | c92f53d5392a5e466a8258e8a55debadb87762b6 | — |

Main-side churn on `session_audio_in.go` since merge base is **zero**:

```bash
git log --oneline 81267d6e4a52..origin/main -- agent-cli/internal/services/session_audio_in.go
# (no output)
```

so the DIVERGENT class there means "PR modified a file main did not touch", not
"main absorbed an equivalent change".

**Blob-for-blob verdict: zero IDENTICAL classifications across all 29 changed files.
No outcome of #165–#168 exists on origin/main.**

## 3. Conflict enumeration (git merge-tree, not GitHub labels)

Method:

```bash
git merge-tree --write-tree --name-only origin/main <head>
# first output line = merged tree OID; subsequent lines = conflicted paths
```

| PR | exit | merged tree | real conflicts (content) |
|----|------|-------------|--------------------------|
| #165 | 1 | 5c06d5193dd6 | go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go (1 hunk) |
| #166 | 1 | 289e453ac0b3 | agent-cli/internal/cli/probe.go (1 hunk); committed_fixtures_test.go (1 hunk) |
| #167 | 1 | 2dc97b41b9f1 | agent-cli/internal/cli/probe.go (3 hunks); go-agent-loop/pkg/probe/deadguard.go (1 hunk); go-agent-loop/pkg/probe/expect.go (3 hunks) |
| #168 | 0 | c3c9c9185ce8 | none — clean merge, consistent with GitHub MERGEABLE |

Conflict-hunk digests (extracted from the merge-tree result trees):

- **committed_fixtures_test.go (#165, #166 — identical hunk):**
  main asserts `FilesScanned != 23`; PR heads assert `!= 22`. Pure constant collision.
- **probe.go (#166):** main inserted `deriveToolResultObservation(...)` (~50 lines,
  tool-result lifecycle scanning from the #164/#170 barge-in repair) where the PR adds
  the v6d scenario registration; both sides added different content at the same site.
- **probe.go (#167):** three hunks — expectation-kind alias map, observation-derivation
  call site, and the `deriveToolResultObservation` insertion; main's tool-result keys vs
  the PR's buffer-disposition additions.
- **deadguard.go (#167):** one hunk in the measurable-expectation allowlist switch:
  `ExpectToolResultDelivered, ExpectToolResultDiscarded, ExpectNoOrphanedToolResult:` (main)
  vs `ExpectBufferDisposition:` (PR).
- **expect.go (#167):** three hunks — kind constants/docs, evaluation switch cases, and
  allowlist line; same tool-result-vs-buffer-disposition pattern.

Driver attribution — commits touching every conflicted file since each merge base:

```bash
git log --oneline 81267d6e4a52..origin/main -- <conflicted file>
# 0e8184d s2s-repair-v3b-bargein-pr164-conflict (#170)
# bb85450 test: reconcile committed session fixture count     [fixture-count file only]
```

All conflict churn traces to #170 (v3b barge-in tool-result repair) and its fixture
reconciliation commit.

## 4. Fixture exact-count ground truth (decisive for #167's red CI)

Executed in detached temp worktrees at each SHA
(`git worktree add --detach /tmp/wt-<sha> <sha>; cd go-llm-gateway && go test ./internal/sessionfixturevalidator/ -run TestAllCommittedSessionFixturesPassWithExactCount -count=1`):

| SHA | result |
|-----|--------|
| `81267d6e` (merge base #165/#167/#168) | FAIL — "ValidatePaths scanned **20** committed session fixtures, want exact count **18**" |
| `79a54e5` (#167 head) | FAIL — identical message (scanned 20, want 18) |
| `2090fbd` (#165 head) | ok (asserts 22, scans 22) |
| `origin/main` | ok (asserts 23, scans 23) |

Conclusion: **#167's failing checks are inherited from its stale merge base, which was
already red before #167's single commit.** #167 adds no file inside any scanned root
(`go-llm-gateway/pkg/providers/openai/testdata`, gateway shared session-fixtures,
`agent-cli/test/integration/testdata` — see `allCommittedFixtureRoots()` on main);
its probe fixtures live under `agent-cli/internal/cli/testdata/**`, which that test does
not scan. Rebasing onto current main fixes the red without any code change.

## 5. #168 failure root cause (from job log 97701834238)

```
gofmt drift detected in agent-cli:
./internal/services/session_audio_in_internal_test.go
Run 'make fmt-fix' to rewrite files before rerunning 'make ci'.
make[1]: *** [Makefile:56: fmt] Error 1
```

Mechanical, self-inflicted on the PR branch, fixed by `make fmt-fix`. #167's log shows
the same inherited fixture-count failure documented in §4.

## 6. Proposed recut branch names — uniqueness proof (2026-08-25)

```bash
git ls-remote origin refs/heads/<name>
```

| proposed branch | ls-remote result |
|-----------------|------------------|
| s2s-v2d-multi-utterance-recut | empty → unique |
| s2s-v6d-error-malformed-recut | empty → unique |
| s2s-v2e-audio-in-truncated-recut | empty → unique |
| s2s-v2c-audio-in-silence-recut | empty → unique |

## 7. Current vertical coverage on origin/main (outcome-on-main context)

```bash
git ls-tree -r --name-only origin/main go-agent-loop/pkg/probe/
# scenario_error_auth.go, scenario_measurable.go,
# scenario_s2s_v1_text_in_audio_out.go(+_test), scenario_s2s_v3b_barge_in_tool_result.go

git ls-tree -r --name-only origin/main agent-cli/test/integration/ | grep -iE 's2s|audioin'
# session_audioin_live_test.go, testdata/s2s-v3b-barge-in-tool-result-{delivered,discarded,orphaned}.session.json

git ls-tree -r --name-only origin/main go-llm-gateway/pkg/testing/testdata/session-fixtures/ | wc -l
# 14   (v6a auth-error fixtures exist; NO v6d malformed-response fixtures)
```

Main carries v1 text-in→audio-out, v3b barge-in/tool-result, v6a auth-error, plus the
generic `session_failure_malformed_frame.session.json` failure fixture — none of which is
the v2c/v2d/v2e/v6d outcome.
