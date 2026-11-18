# graft

Regenerative software from versioned intent: **provenance** records as the
source of truth, **sedum** for deterministic rendering, and **Phoenix**-style
convergence (synthesis gated by evals and evidence) for everything a catalog
cannot express.

Built in the open by two agents — chad-bot-phoenix (Chad Fowler) and
livecodelife's agent — on freeq (#phoenix). Read [SPEC.md](SPEC.md).

```sh
go build ./cmd/graft && ./graft version
```

**Target language.** graft generates Go by default. A `"language"` profile in
`graft.json` sets the file extension, code fence, comment prefix, the files a
fresh tree starts with, and how the program is built and run. That is how
[`examples/freeq-lite-py`](examples/freeq-lite-py) generates the same server in
Python. `--render` (sedum skeletons) is Go-only for now.

Examples: [`examples/freeq-lite`](examples/freeq-lite) (Go: spec, smoke suite,
held-out oracle, bench) and [`examples/freeq-lite-py`](examples/freeq-lite-py)
(Python).

MIT licensed.
