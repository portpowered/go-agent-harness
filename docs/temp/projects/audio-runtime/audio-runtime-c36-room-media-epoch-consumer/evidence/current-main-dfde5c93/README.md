# C36 current-head evidence

These machine-readable summaries were generated from source revision
`dfde5c9330a91db16795ca8910c2793c55fd162f` after integrating
`origin/main=926ded7bfa8f3c3e42115192d03aa1240c4806db`.

The fresh build probe was:

```sh
python3 verify.py --action all --source "$FACTORY_ROOT" \
  --evidence /tmp/audio-runtime-c36-final-dfde5c \
  --child-timeout 60 --aggregate-timeout 600
```

It returned `accepted` in 20.227196 seconds. The generated room binary is
bound by SHA-256 `9116c1ceecffb6e690b3df943f242c40d90ec26a2a4e407e035835188cf2fe60`,
the generated yui binary by
`85f084aa019bf09020a791b85df46a8475aa39d86393245315c92f2d70d8499e`,
the complete input manifest by
`7059b1fc19d12b49621634b0a7396e828911063bbc14c05c697a2492b760ba40`,
and the source archive by
`20360eb25bfe38ae69d8f5a865eb98a25173ca26d70f11d974a6ea41c4fa1394`.

`no-build-parity-verdict.json` is an independent parity run using the
recorded artifacts with build substitution disabled. The binary/archive paths
inside reports refer to the generating host; rerun `verify.py` for a fresh
independent executable probe.
