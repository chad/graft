# freeq-lite-py: the same yardstick, in Python

This is freeq-lite, generated as a **Python** program. It shows that graft's
pipeline is not Go-specific. The spec, the visible smoke suite and the
held-out oracle are the ones in `../freeq-lite`, used unchanged. Only the
language profile, the conventions and the gates differ.

| file | what |
|---|---|
| `graft.json` | the `language` profile (Python), gates, and the model |
| `conventions.md` | the architecture: `running.py` owns the core and `HANDLERS`; every other module registers handlers on import |
| `gates/check.py` | every module compiles and imports cleanly |
| `gates/registry.py` | only `running.py` reads `HANDLERS`; other modules define only `<module>_*` functions and no classes |
| `gates/unused.py` | no dead functions |
| `case.json` | the bench case: shared spec and oracle, run as `python3 main.py -addr …` |

`main.py` is written into a fresh tree by the profile's `bootstrap`; it is not
generated. It imports `running`, then every other module, then starts the
server. The oracle and the smoke suite start it with
`-cmd '["python3","main.py"]'` instead of building a binary.

```sh
graft run --config examples/freeq-lite-py/graft.json \
  --spec examples/freeq-lite/spec/freeq-lite.md --repo R --root A --accept
go run ./examples/freeq-lite/oracle -cmd '["python3","main.py"]' -dir A
```

**One run** (claude sonnet, n=1, an anecdote): 7/7 regions converged on the
first attempt in about 80 s. The held-out oracle scored it **47/47** (with the
smoke overlap described in `../freeq-lite/README.md`).

**Not yet:** `--render` (sedum skeletons) supports only Go for now, and graft
refuses it for other languages. Moving the declaration split from `go/ast`
to the generator is the sedum-side half of M10.
