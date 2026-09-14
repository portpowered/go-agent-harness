# C75 room replay bundle retirement

This directory is the admitted C75 evidence scope. The reusable room replay
bundle contract and admission policy now live in
`go-agent-runtime/services/roomreplaybundle`; the three CLI files retain only
deprecated aliases, compatibility JSON helpers, and a package marker.

Run the bounded evidence from this directory with:

```text
rtk python3 run.py --case all
rtk python3 verify.py
```

The runner includes normal/race runtime tests, the unchanged CLI room replay
package, vet, staticcheck, coverage registration, the separate `GOWORK=off`
consumer, and the exact shared Wire/architecture checks. The latter two are
recorded as blocked by the active C57/C61 ownership leases; this task does not
edit `scripts/wire-packages.txt` or
`docs/architecture/architecture-size-baseline.json`.

The external consumer prints `C75_ROOMREPLAYBUNDLE_CONSUMER PASS`, imports only
the public roomreplaybundle contract and its Wire package, and proves a
same-length artifact mutation is rejected by the public typed error.
