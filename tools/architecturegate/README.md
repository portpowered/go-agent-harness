# architecturegate

`architecturegate` is the repository's standalone shape and complexity gate.
It is kept in its own Go module so checking the workspace does not make the
runtime depend on its analysis implementation. The policy manifest is
[`docs/architecture/architecture-policy.json`](../../docs/architecture/architecture-policy.json).

Run it from the repository root with the repository-pinned Go toolchain. The
tool is a standalone module, so invoke it from its directory with the root
passed explicitly:

```sh
cd tools/architecturegate
GOWORK=off go run . \
  -repo ../.. \
  -manifest docs/architecture/architecture-policy.json \
  -check architecture
```

The size lane is deliberately separate because existing code is migrated in
measured phases. A reviewed baseline can be supplied explicitly:

```sh
GOWORK=off go run . \
  -repo ../.. \
  -manifest docs/architecture/architecture-policy.json \
  -baseline docs/architecture/architecture-size-baseline.json \
  -baseline-base origin/main \
  -check size
```

The same baseline flags may be used with `-check architecture` once the
reviewed file contains architecture debt entries. The driver filters entries
by the selected lane, so one file can carry both size and architecture debt.

Composition authority is explicit. A whole package may be registered for an
external application module; a repository test gets a single exact
`_test.go` source entry. Wildcards, production files, and paths outside the
module are rejected. Registered tests can assemble public service Wire
packages while the private implementation import rule still applies.

There is no accept-current or baseline-writing mode. Produce a review report
first, preserving failures with `|| true`, then add only confirmed pre-existing
issues to the checked-in JSON with a rationale and migration phase:

```sh
GOWORK=off go run . \
  -repo ../.. \
  -manifest docs/architecture/architecture-policy.json \
  -check all -format json > /tmp/runtime-refactor-architecture-report.json || true
```

`-baseline-base` is required in CI. When that ref already contains the
baseline, the gate rejects added entries and ceiling increases by comparing
the two manifests. For the first baseline, it archives the merge-base source
tree and measures its source-level architecture and size issues, allowing only
entries that existed in that source tree; violations in newly extracted
modules remain live failures. Buildable historical packages are type-loaded
from that snapshot for public-surface checks, while inactive platform/tag-only
directories remain in the physical inventory without blocking the snapshot
load. Historical source is never executed through the current module graph.
This bootstrap still requires review of each entry's rationale and phase.

Use `-format json` to archive a deterministic CI report. `-module-dir` may be
repeated to check a fixture or a smaller module set; `-pattern`/`-scope` may be
repeated to select package patterns. `-goos` and `-goarch` let the inventory
remain the same while type loading is repeated for a supported build matrix.

The inventory walks every `.go` file in a selected package directory,
including inactive platform files. Type-aware checks use `go/packages` for the
selected host matrix. Registered generated files are excluded only when they
have a standard generated header and match a manifest generator entry. A
header without a registration is reported as `generated-file-spoof`.

The baseline is deletion-only. A new issue fails; a metric increase fails; a
metric reduction requires the recorded value to be lowered; and a resolved
entry must be deleted. There is no automatic accept-current or update flag.
Renames are explicit one-to-one entries in the baseline and cannot multiply a
debt exemption.

The pinned `golangci-lint` configuration enables the staged correctness and
policy checks (`errcheck`, `ineffassign`, `unused`, `nilerr`, `errorlint`,
`bodyclose`, `contextcheck`, `durationcheck`, `goconst`, `mnd`, `exhaustive`,
`gochecknoglobals`, `gochecknoinits`, `forbidigo`, and `nolintlint`). Verify the
configuration with the resolver's v2.9.0 binary before a run:

```sh
golangci-lint config verify --config .golangci.yml
(cd go-agent-runtime && GOWORK=off golangci-lint run --new-from-rev origin/main ./...)
```

The config carries the same `origin/main` no-new-debt default for direct
invocations. CI should pass its fetched merge-base explicitly when the
selected base differs; the existing analyzer resolver remains the only
installation path and checks the pinned version and digest.

The service shape is recognized only under `services/<name>/`: contracts live
at the root, private implementation packages under `internal/`, construction
under `wire/`, and optional adapters under `transports/`. Root contracts cannot
reach implementation types or expose constructors. Peer service internals and
wire packages are protected, and reusable modules listed by policy cannot
import a CLI module.

`module_rules` provides an explicit top-level allowlist for extracted runtime
modules. Keep this list narrow (the module root, `services`, `wire`, and named
platform contracts) so copied public implementation trees cannot become an
accidental second API. Generated files are registered per module and exact
path; a recursive wildcard cannot register arbitrary Wire packages.
