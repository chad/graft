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
Under `--render` the skeleton is record content: `skeleton.json` is imported
into each imprint's shape (a new record version, only when it differs) and the
tree is planned from the records. `skeleton.json` is still the source of truth,
so a shape edited in the records alone is overwritten by the next run that
passes it. A shape (skeleton) edit re-verifies every region with no model call,
and only the regions that fail (a region whose own shape moved, or whose body no
longer passes the gates) regenerate.

**Existing repos.** In a repo whose packages live in directories, place a section's region with `"files": {"<section>": "pkg/file.go"}` in `graft.json`. A key is the section's full title, including the top heading, or its last part alone when that is unambiguous. A new section never takes over an existing file, and once accepted its file is fixed. See [docs/real-repo-trial.md](docs/real-repo-trial.md) for a trial in a real Go service, and for what a user still has to supply: placement, a wiring hook and an acceptance test.

**Signing off.** A tier that requires `human_signoff` is satisfied only by
evidence a person records, with `graft signoff`:

```sh
graft signoff --config graft.json --records DIR --log FILE \
  --record ID --path FILE --by "your name" [--note TEXT] [--render]
```

It shows the region's body and everything it will pin, then asks (a prompt on
a terminal; `--yes` without one). The sign-off pins the record's `gen_hash`
and version, the region's body hash, the spec hash and, with `--render`, the
skeleton hash, and is appended to the same hash-chained evidence log as every
other evidence, so it goes stale the moment any of those moves: edit the
record, the spec, the body or the skeleton and the tier is unsatisfied again
until you sign off again. It refuses a hand-edited or missing region and a
region written for an older `gen_hash`. It does not run or skip the gates: the
tier still needs its executable evidence. **`--by` is whatever you type; graft
does not authenticate it**, and the log is not tamper-proof (SPEC §7).

Examples: [`examples/freeq-lite`](examples/freeq-lite) (Go: spec, smoke suite,
held-out oracle, bench), [`examples/freeq-lite-py`](examples/freeq-lite-py)
(Python), [`examples/freeq-lite-ts`](examples/freeq-lite-ts) (TypeScript,
plain and `--render`) and [`examples/ledger-lite`](examples/ledger-lite) (a
harder case: rules that cross sections, a stated 12/42 smoke–oracle overlap).

MIT licensed.
