# Project cycle batches

Project leadership returns {"request": <FACTORY_REQUEST_BATCH>}. The worker
output requires this wrapper; file/CLI batch ingress takes the bare batch in
batch-input-example.json. Inside request use requestId,
type, works and relations, as in batch-input-example.json. Never invent items or
workType aliases. A unique idea name is also its task name; branchName in its
PRD is codex/<idea-name>. Every idea carries project and contractRevision.
The one project-cycle has the same name as its parent project and a literal
string payload: continue, complete or blocked. Its DEPENDS_ON edges target every
emitted idea/validation at requiredState complete. Failed prerequisites flow back
through the graph; do not silently treat them as completed prerequisites.

Only the launcher admits a project. The manager emits no thoughts loopbacks;
routine meta planning is scheduled every four hours. Use the explicit server
and session for inspection. For canonical syntax run `you docs batch-inputs`.
