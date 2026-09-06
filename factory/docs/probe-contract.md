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
  "budget": {"timeSeconds": 1800, "realtimeSessions": 0, "realtimeSeconds": 0},
  "mission": "Exercise the final public behavior against every manifest criterion.",
  "reportPath": "<absolute project evidence path>.json",
  "build": {"identity": "<id>", "path": "<absolute executable>", "sha256": "<digest>"},
  "fixtures": []
}
```
