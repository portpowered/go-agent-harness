# C97 session observation evidence

The `external-consumer` module is intentionally separate from the workspace.
It imports only the public `sessionobservation` contract and its generated Wire
package, and is always tested with `GOWORK=off`.

Run the positive consumer and the three causal controls with:

```sh
python3 verify.py --mode positive-and-three-mutations
```

The controls mutate a temporary overlay of the private implementation. They do
not modify the candidate worktree: reused commit payload, duplicate terminal,
and rejected playback classification must each fail its named consumer test.

The bounded shipped/runtime cases use the source-pinned YUI build and never
inherit provider credentials:

\`\`\`sh
python3 run.py --case ordered-observation-replay --child-timeout 60 --aggregate-timeout 300
python3 run.py --case malformed-or-out-of-order-replay --child-timeout 60 --aggregate-timeout 300
python3 run.py --case audio-tool-continuation --child-timeout 60 --aggregate-timeout 300
\`\`\`

The replay evidence proves exact PCM bytes and recorded event ordering. It does
not claim physical-device or acoustic behavior.
