# C36 frame-metadata repair evidence

These summaries were generated from clean committed source revision
`621e2909857904fe4d098aee975a47c61ad7ed0d`, after fetching and preserving
`origin/main=d6efc88d10e046396e777762802f5f731c9c6d9d` as an ancestor.

The bounded command was:

```sh
python3 verify.py --action all --source <isolated-worktree> \
  --evidence evidence/current-head-621e290 --child-timeout 60 \
  --aggregate-timeout 600
```

It returned `accepted` in `15.443803` seconds. The source archive SHA-256 is
`1fbd35c8da0d3162e9c03c63308432a0207265e0131f1312158fa0767b2025e0`, the
input manifest SHA-256 is
`8605ca75749526d2582f6dd2f87d0349751bce30bde740250f8c502c8faa7244`, the
room binary SHA-256 is
`9305fff3ffe4f308666429901b33c4921a7b69d04cef713481731e7d71dbbad6`, and the
same-source yui binary SHA-256 is
`79dca64f4413b3e926071b18769660f57d7d04a06359955f02bd25cf08abec33`.

The raw report retains frame format `{sample_rate: 1000, channels: 1,
bit_depth: 16, encoding: pcm16}`, source stream IDs `alice-provider` and
`bob-provider`, room stream IDs `room:alice`, `room:bob`, and
`room:listener`, exact start-sample cursors, and zero playback-response
identity. The Go/Python checks accepted peer outputs
`alice=[211,212,213,214]` and `bob=[111,112,113,114]`, and all nine mutation
subprocesses rejected their serialized wrong-oracle reports, including
format, stream ID, start sample, and playback-response identity.

The same run also accepted boundary, provenance, partial-recording rejection,
software consumption, lifecycle/join, TERM/KILL descendant cleanup, and the
same-source C21 audio-tool/interruption parity controls. Physical playback is
explicitly unavailable and is not claimed by this software-only evidence.
No script-CI success, independent review, merge, or post-merge vertical
acceptance is claimed here.
