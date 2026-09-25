# golangci-lint policy

`make lint` runs the pinned golangci-lint (v2.9.0, through the Makefile's
analyzer resolver) twice for every `LINT_MODULES` module. Production and test
files are both checked. These limits are separate from the stricter
[architecture gate budgets](size-baselines.md) and do not replace them.

## Hard pass: all code

[`.golangci.yml`](../../.golangci.yml) runs with `--all-code`. It has no
`new-from-rev` filter and no baseline, so any finding in any file fails lint.

| Check | Limit |
| --- | --- |
| `linters.default: standard` | `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused` |
| `errcheck` | also checks blank assignments and type assertions; only Win32 `LazyProc.Call`, `Proc.Call` and `SyscallN` are excluded |
| `revive` `file-length-limit` | 1,000 physical lines per file |
| `funlen` | 100 lines per function (statements are not counted) |
| `gocyclo`, `gocognit` | 30 |
| Also enforced | `goconst`, `nilerr`, `bodyclose`, `durationcheck`, `gochecknoinits`, `nolintlint` |

Every finding is reported (`uniq-by-line: false`). A `//nolint` directive must
name one linter and give a reason. An unused directive is itself a finding.

Some errors are safe to discard, for example a `Close` failure after a
successful read. Discard them with a documented helper instead of a blank
assignment. Do not turn a success into a failure because cleanup failed.

## New-code pass: changed code only

[`.golangci.new.yml`](../../.golangci.new.yml) runs with
`--new-from-rev $(LINT_BASE)` (default `origin/main`). It covers linters that
still have legacy findings: `errorlint`, `contextcheck`, `mnd`, `exhaustive`,
`gochecknoglobals` and `forbidigo`. New and modified lines must be clean.

## Promoting a linter to the hard pass

1. Measure its repository-wide count without `new-from-rev` on `GOOS=linux`,
   `darwin` and `windows`.
2. Fix every finding.
3. Move the linter and its settings from `.golangci.new.yml` to `.golangci.yml`
   in the same change.

## Run locally

```sh
make lint
# One module, hard pass only:
scripts/golangci-lint-working-tree.sh --analyzer <pinned golangci-lint> \
  --all-code --config .golangci.yml --module agent-cli --repo . -- ./...
# One module, new-code pass only:
scripts/golangci-lint-working-tree.sh --analyzer <pinned golangci-lint> \
  --config .golangci.new.yml --base origin/main --module agent-cli --repo . -- ./...
```

Findings depend on the target platform. When you change platform-tagged files,
run golangci-lint with `GOOS=linux`, `darwin` and `windows`. CI runs the Linux
pass in the `CI (static lint)` job. The `golangci-lint` on PATH may be a
different version, so use the Makefile resolver.
