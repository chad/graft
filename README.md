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
Python. `--render` (sedum skeletons) works for any language whose sedum
generator package declares a `convergence:` block (`go` and `typescript` do):
graft knows no language there, it reads stubs, helper signatures, seeds, synth
file names and extensions from `sedum convergence render`. A skeleton may name
its generator package (`"generator": "typescript"`); it defaults to the
profile's name. This needs a sedum that has `sedum convergence`.

**Existing repos.** In a repo whose packages live in directories, place a section's region with `"files": {"<section>": "pkg/file.go"}` in `graft.json`. See [docs/real-repo-trial.md](docs/real-repo-trial.md) for a trial in a real Go service, and for what a user still has to supply: placement, a wiring hook and an acceptance test.

Examples: [`examples/freeq-lite`](examples/freeq-lite) (Go: spec, smoke suite,
held-out oracle, bench), [`examples/freeq-lite-py`](examples/freeq-lite-py)
(Python), [`examples/freeq-lite-ts`](examples/freeq-lite-ts) (TypeScript,
plain and `--render`) and [`examples/ledger-lite`](examples/ledger-lite) (a
harder case: rules that cross sections, a stated 12/42 smoke–oracle overlap).

MIT licensed.
