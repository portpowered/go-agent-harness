# architecturegate

`architecturegate` is the repository's service-shape and boundary gate. It
checks only what golangci-lint cannot express. It is kept in its own Go module
so checking the workspace does not make the runtime depend on its analysis
implementation. The policy manifest is
[`docs/architecture/architecture-policy.json`](../../docs/architecture/architecture-policy.json).

## What it owns, and what golangci-lint owns

[`.golangci.yml`](../../.golangci.yml) is the single owner of file and
function size, statement count, cyclomatic and cognitive complexity, nesting,
package globals and `init` functions, and of every import rule depguard can
express. It applies the same limits to all code, with no baseline. See
[lint-policy.md](../../docs/architecture/lint-policy.md).

The gate keeps:

- **Service shape** (`service_roots`, `module_rules`): a service under
  `services/<name>/` has root contracts, `internal/` implementations, `wire/`
  construction and optional `transports/` (`service-shape`); a root declares a
  `Service` interface (`service-interface`) and exposes no free functions or
  constructors (`root-exported-function`, `root-constructor`); wire packages
  hold no exported business methods (`wire-business-method`); extracted
  runtime modules only grow allow-listed top-level directories
  (`module-root-shape`).
- **Service boundaries**: roots consume contracts only
  (`root-implementation-import`, `root-private-import`) and import no effectful
  packages (`root-forbidden-import`, `forbidden_root_imports`); only
  composition imports a service wire (`wire-import`); peers never import each
  other's private packages (`peer-private-import`); tests do not bypass
  composition (`test-private-import`); exported APIs never reach an
  `internal` type (`public-implementation-leak`). These follow the gate's
  service classifier and the `composition_registry`, which depguard's
  file-glob/import-prefix lists cannot model.
- **Session wrappers** (`session-wrapper-capabilities`): a type that holds and
  is a `messages.Session` embeds `messages.SessionCapabilities`.
- **Source patterns** (`forbidden_source_patterns`): hand-rolled encodings stay
  in the module that owns them (see below).
- **Glob import rules** (`forbidden_imports`): only the rules whose import
  pattern has a wildcard in the middle (`**/services/internal/**`,
  `**/services/servicetest`, `.../services/**/wire`). depguard matches
  import-path prefixes only.
- **Generated files** (`generated_files`): a `Code generated` header must be a
  registered, reproducible generator output (`generated-file-spoof`).

There is no baseline: every finding fails the gate.

## Run

Run it from the repository root with `make architecture-check`, or from its
directory with the root passed explicitly:

```sh
cd tools/architecturegate
GOWORK=off go run . \
  -repo ../.. \
  -manifest docs/architecture/architecture-policy.json
```

Use `-format json` to archive a deterministic CI report. `-module-dir` may be
repeated to check a fixture or a smaller module set; `-pattern`/`-scope` may be
repeated to select package patterns. `-goos` and `-goarch` let the inventory
remain the same while type loading is repeated for a supported build matrix.

The inventory walks every `.go` file in a selected package directory,
including inactive platform files. Type-aware checks use `go/packages` for the
selected host matrix. Registered generated files are excluded only when they
have a standard generated header and match a manifest generator entry.

The manifest rejects unknown keys, so a retired section cannot linger in it
unenforced.

## Policy rules

`forbidden_imports` rules name the importing packages (`from`) and the
forbidden import patterns (`imports`). Optional fields narrow a rule:
`except` re-allows specific imports, `except_from` removes packages from
`from`, `files` limits the rule to module-relative source paths, and
`production_only` skips `_test.go` files. Add a rule here only when depguard
cannot express it; otherwise add a depguard list to `.golangci.yml` and a case
to `TestDepguardImportRulesRejectViolations`.

`forbidden_source_patterns` rules keep a hand-rolled encoding inside the
module that owns it. Each rule has a `name` (the issue rule), the governed
packages (`from`, narrowed by `except_from`), optional `except_files` named
by module path plus module-relative path (so an exemption covers one file in
one module), and what it forbids: `literals` are substrings of string
literals, and `selectors` are package-qualified selector chains such as
`encoding/binary.LittleEndian.PutUint16`, matched under whatever local name
the file imports that package as. The checked-in rules keep WAV containers
(`"RIFF"`) and PCM16 packing (16-bit `binary.LittleEndian` access) inside
go-audio; use `wavio` and `codec` instead. Tests are included, because test
helpers were where the copies accumulated.

The rules match syntax, not behavior, so they are a tripwire for the common
copy rather than a proof. Known bypasses that the PCM16 rule does not see:
manual byte shifts (`int16(b[i]) | int16(b[i+1])<<8`), `binary.Read` or
`binary.Write` into an `[]int16`, and a value alias such as
`le := binary.LittleEndian` followed by `le.Uint16(...)` (an aliased package
import is caught; an aliased value is not). Review catches these. The WAV rule
likewise sees only the `"RIFF"` literal, not the bytes built another way.
`tests/localai` is governed by both rules. Its `protocol_test.go` is the one
explicit `hand-rolled-pcm16` exemption: the standalone black-box conformance
suite has no go-audio dependency and keeps its own PCM16 oracle on purpose,
as its README says. Any other hand-rolled PCM16 or WAV code there is flagged.

Composition authority is explicit. A whole package may be registered for an
external application module; a repository test gets a single exact
`_test.go` source entry. Wildcards, production files, and paths outside the
module are rejected. Registered tests can assemble public service Wire
packages while the private implementation import rule still applies.

`module_rules` provides an explicit top-level allowlist for extracted runtime
modules. Keep this list narrow (the module root, `services`, `wire`, and named
platform contracts) so copied public implementation trees cannot become an
accidental second API. Generated files are registered per module and exact
path; a recursive wildcard cannot register arbitrary Wire packages.

## Tests

`make test-architecture-gate` runs the positive and negative fixtures. They
also load the checked-in policy and `.golangci.yml`:
`TestRepositoryImportRulesRejectViolations` and
`TestRepositorySourcePatternRulesKeepAudioEncodingInGoAudio` prove the gate's
own repository rules, and `TestDepguardImportRulesRejectViolations` runs the
depguard analyzer version golangci-lint v2.9.0 pins over the repository's
depguard lists.
