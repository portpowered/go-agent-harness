# Validation probe contract

`prepare-validation.py` stages a fresh immutable artifact and writes
`docs/temp/probes/<work-name>/mission.json`. Every mission carries the admitted
`project`, `contractRevision`, role, immutable criterion rubrics, budget, report
path, build descriptor and optional fixtures. The mission's `scope` defaults to
`project`.

Use `scope: "vertical"` immediately after one integrated vertical. A vertical
mission must carry a nonempty `vertical` name and nonempty `sourceRevision` (the
merged/source identity for that artifact), and its `criteria` list must be a
nonempty subset of the manifest criteria with unchanged rubrics. The staged build
hash remains the artifact authority. Its mission names the vertical contribution
and public workflow; a vertical PASS covers that contribution only, with any
unproven remainder of the broad rubric recorded as a limitation.

```json
{
  "project": "audio-runtime",
  "contractRevision": "audio-runtime-v1",
  "scope": "vertical",
  "vertical": "audio-runtime-c01",
  "sourceRevision": "<merged-source-revision>",
  "role": "engineering",
  "criteria": [{"id": "AUDIO", "rubric": "<manifest rubric>"}],
  "budget": {"timeSeconds": 1800, "realtimeSessions": 0, "realtimeSeconds": 0},
  "mission": "Launch the shipped executable and exercise the changed public workflow.",
  "reportPath": "<absolute project evidence path>.json",
  "build": {"identity": "<id>", "path": "<absolute executable>", "sha256": "<digest>"},
  "fixtures": []
}
```

## Reviewed C39 scope amendment

The only supported amendment is the append-only record
`factory/projects/audio-runtime/amendments/user-windows-hardware-scope-20260910.json`.
It is generated from, and is valid only while all of these reviewed inputs are
present as regular files with the recorded SHA-256 values:

- `docs/temp/projects/audio-runtime/user-scope-service-decomposition-20260910/scope-amendment-authority.json`
  (`c3bfa91543b349b2c9b89f48c3a95203db72bf59a4fd93b5e4b1851aee772413`), the
  explicit user-direction anchor;
- `docs/temp/projects/audio-runtime/audio-runtime-c32-capture-energy-vertical-probe.json`
  (`0345a628a6038e18bf7c7a59016e7a0002ae89734c6ff59aa541642f34b3d960`), the
  preserved `FAILED` report whose `DEVICE` result is `BLOCKED`.

The reviewed manifest is bound by SHA-256
`ec1439b3b1edf5ab935a59cfe67756b67f4a27e51c20ffcaad87ffab35acdf3d`; its three
authority file paths and digests must also remain unchanged.  The record allows
only these two subproofs to be marked `OUT_OF_SCOPE`:

```json
[
  {"criterionId":"DEVICE","subproof":"Native Windows hardware/endpoints testing","verdict":"OUT_OF_SCOPE"},
  {"criterionId":"PARITY","subproof":"Physical acoustic testing","verdict":"OUT_OF_SCOPE"}
]
```

This is not a DEVICE or PARITY waiver.  Windows software execution and
compilation, hermetic codec/capture behavior, device isolation/lifecycle,
consumption versus queue admission, all nine original rubrics, fresh evidence,
and the original `3` sessions/`120` seconds Realtime limits remain required.
The historical C32 bytes and verdict are never rewritten.

The direct controller operations are:

```text
python3 factory/scripts/project-control.py amendment-append --root <root> --record <contained-record.json>
python3 factory/scripts/project-control.py amendment-status --root <root> [--amendment-id user-windows-hardware-scope-20260910]
```

`amendment-append` validates the entire candidate before publishing a canonical
JSON file.  Publication uses a fully written temporary inode and an exclusive
hard-link, so an existing ID is never replaced; an exact repeat reports
`already-present`.  `create`, `validate`, `append`, and `status` are also
available from `factory/scripts/project_scope_amendment.py` for isolated
fixture controllers.  A missing amendment leaves the legacy preparation and
completion behavior in force; a malformed or stale amendment fails closed.

An amended preparation payload must explicitly use `scope: "project"`, carry a
nonempty `sourceRevision`, and include the exact reference returned by
`amendment-status`:

```json
{
  "scope":"project",
  "sourceRevision":"<tested-source-revision>",
  "amendment": {
    "id":"user-windows-hardware-scope-20260910",
    "path":"factory/projects/audio-runtime/amendments/user-windows-hardware-scope-20260910.json",
    "contentSha256":"<published-record-sha256>",
    "manifestSha256":"ec1439b3b1edf5ab935a59cfe67756b67f4a27e51c20ffcaad87ffab35acdf3d",
    "authority":{"sourcePlan":{"path":"...","sha256":"..."},"request":{"path":"...","sha256":"..."},"acceptance":{"path":"...","sha256":"..."}},
    "authorization":{"path":"<reviewed-anchor-path>","sha256":"<reviewed-anchor-sha256>","provenance":"<reviewed-user provenance>"},
    "historicalReport":{"path":"<preserved-C32-report>","sha256":"<historical-sha256>","canonicalState":"failed","decision":"FAILED","deviceVerdict":"BLOCKED"},
    "excludedSubproof":[
      {"criterionId":"DEVICE","subproof":"Native Windows hardware/endpoints testing","verdict":"OUT_OF_SCOPE"},
      {"criterionId":"PARITY","subproof":"Physical acoustic testing","verdict":"OUT_OF_SCOPE"}
    ]
  }
}
```

Preparation stages this reference, the original manifest authority, the
`sourceRevision`, and an immutable mission digest.  Its JSON result also repeats
the admitted `project`, the complete staged `build` descriptor, and
`buildIdentity`; consumers must carry those exact identities into canonical Work
and report validation.  Both final project reports
must repeat the exact reference and bind to their staged `mission.json`, its
digest, the same artifact identity/hash, and the source revision.  Each report
must provide explicit PASS evidence for the four retained software/device
areas and repeat the two excluded entries plus the preserved historical report
metadata in `scopeEvidence`; an old vertical report cannot be relabeled into
amended completion.

Before primary activation, verify the merged source and these input hashes,
preserve existing admission/active missions, run `amendment-status`, append the
reviewed record once, and prepare new project-scope missions.  No
`factory.json`, admission identity, live graph, resume hash, or active mission
is changed by this plumbing.  The bounded controller/evidence probe is run as:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c39-authorized-scope-amendment/probe.py --source-revision <tested-sha> --yui <same-source-immutable-yui> --output <fresh-owned-evidence-directory>
```

When the reviewed C32 inputs are staged, repeat `--replay-fixture` and
`--replay-config` in pairs, first for the audio/tool fixture and then for the
interruption fixture.  Each config argument is the regular config directory
used by the shipped yui, not a single YAML file; the probe copies it into a
fresh private workdir, runs with credential environment variables removed,
checks literal PCM/tool/healthy-follow-on oracles, replays the resulting
bundle, and checks missing-timeline rejection.

The probe uses only an isolated Git/admission fixture and a credential-free
`yui --help` process smoke unless an existing replay fixture is available. It
records exact source, controller-script, reviewed-input, replay-fixture/config,
and executable hashes before launch; append/status/preparation/completion
effects; negative exit codes; protected-file hashes before and after the
controller run; bounded child cleanup; and any replay result without promoting
software replay to physical/acoustic evidence. Child output is capped while it
is read, and a fresh output directory receives bounded `FAILED` evidence even
when the probe aborts after creating that directory.

The reviewed controller fixture result is `ACCEPTED`: authorized append and
idempotent status passed; a widened exclusion exited nonzero; explicit
`amendment: null` was rejected; fresh amended customer and engineering missions
completed against one artifact with distinct validation identities and exact
canonical Work name/project/mission/artifact bindings; and the protected
manifest, authority, authorization, and FAILED/BLOCKED C32 report hashes were
unchanged before and after the run. The deterministic timeout control exited
after TERM with bounded reap, and the supplied same-source immutable yui
`--help` exited `0` without changing its recorded SHA-256. The replay controls
also assert the interruption oracle (3,360 rendered PCM bytes and the expected
healthy follow-on tail), so an older or mismatched executable fails rather than
being accepted. C39 itself uses zero live Realtime sessions/seconds; amended
missions retain, and validate, the original maximum of 3 sessions/120 seconds.
The owned evidence directory contains the exact machine-readable report and
source revision. These are controller/software controls only; they do not
establish native Windows endpoint consumption or physical acoustic output.

Use `scope: "project"` for the final customer and engineering missions. They must
each cover every manifest criterion against the same build and produce distinct
canonical validation Work identities. `project-control.py verify-completion` only
accepts project-scope reports; a missing scope on an older report is interpreted as
the legacy project scope, while an explicit vertical scope is never final evidence.

```json
{
  "project": "audio-runtime",
  "contractRevision": "audio-runtime-v1",
  "scope": "project",
  "role": "customer",
  "criteria": [{"id": "AUDIO", "rubric": "<every manifest rubric>"}],
  "budget": {"timeSeconds": 1800, "realtimeSessions": 3, "realtimeSeconds": 120},
  "mission": "Exercise the final public behavior against every manifest criterion.",
  "reportPath": "<absolute project evidence path>.json",
  "build": {"identity": "<id>", "path": "<absolute executable>", "sha256": "<digest>"},
  "fixtures": []
}
```
