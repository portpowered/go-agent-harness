# Factory worktree cleanup

`factory/scripts/worktree-cleanup.py` audits shared-repository worktrees and
regenerable build/test outputs. It writes a JSON report and defaults to a dry
run. The command requires a healthy live Factory session; mutation stops if the
session snapshot cannot be read before or during deletion.

```sh
python3 factory/scripts/worktree-cleanup.py \
  --you /absolute/path/to/you \
  --server http://127.0.0.1:7439 \
  --dry-run

python3 factory/scripts/worktree-cleanup.py \
  --you /absolute/path/to/you \
  --server http://127.0.0.1:7439 \
  --apply
```

The build-output pass covers every registered Factory-managed checkout under a
`.claude/worktrees` directory, even when another clone owns that checkout. It
removes only old ignored Go build caches carrying Go's regeneration marker,
ignored Go tool caches, and ignored coverage directories. A live or queued Work
name and a locked checkout always block removal. Dirty or unmerged source does
not block removal of these recognized outputs because the source itself remains
untouched.

Whole-worktree removal is stricter. The checkout must be under a system
temporary directory or a registered `.claude/worktrees` directory, old, clean
including ignored files, unlocked, absent from the live board, merged into the
configured base ref, and unreferenced by repository or Factory-run text. Codex
task worktrees outside `.claude/worktrees` remain report-only because the
Factory API cannot establish ownership for separate Codex sessions.

The default state and report paths are
`docs/temp/projects/audio-runtime/worktree-cleanup-state.json` and
`docs/temp/projects/audio-runtime/worktree-cleanup-report.json`. The state is
written only after an unblocked apply run and records apparent deleted bytes as
well as measured free-space change; on APFS clones those values can differ.

The factory supervisor also runs this cleanup every 30 minutes while the Data
volume is under pressure. It starts below 32 GiB free and stops deleting after
48 GiB is free, so it does not repeatedly scan a healthy disk. A repository
common-dir lock prevents overlapping manual and automatic runs. Automatic
reports live under `.git/factory-cleanup/`; active or queued Work names and all
of the safety checks above remain protected.
