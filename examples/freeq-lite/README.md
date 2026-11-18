# freeq-lite — the graft demo target

A simplified freeq: a Go chat server speaking a subset of IRC (registration,
channels, channel and private messages, keepalive, quit). No federation, no
identity, no media.

| Path | What | Who may see it |
|---|---|---|
| `spec/freeq-lite.md` | the intent: the only input to generation | graft, models, everyone |
| `smoke/` | a small **visible** smoke suite (12 checks, a few per section), written from the spec alone | the model, and a gate during generation |
| `oracle/` | the behaviour suite (47 checks over TCP) | the bench only, never a model |
| `reference/` | a hand-written server that exists to validate the oracle | nobody but the oracle's own test |

The oracle was written from the spec **before any code was generated**, imports
nothing from any implementation, and must score the reference 47/47
(`go test ./examples/freeq-lite/oracle`). **Thirty committed mutants** of the
reference (`oracle/mutants_test.go`) must each fail their named check, so "the
oracle has teeth" is something anyone can re-run. Eleven of them are bugs
livecodelife's agent wrote that escaped earlier versions of the oracle (six
against the 32-check oracle, five more against the 38-check one); each now has
a check.

**Keeping the yardstick out of reach.** Generate into a `--root` and `--repo`
*outside* this repository. The model command runs in an empty directory and
sees only its prompt; gates run in a scratch copy of `--root` only. Nothing in
graft reads `oracle/` or `reference/` except the oracle's own tests.

```sh
go run ./cmd/graft draft examples/freeq-lite/spec/freeq-lite.md   # 7 drafts, 36 statements, all receipts valid
go run ./examples/freeq-lite/oracle -bin ./path/to/server          # {"checks":47,"passed":…}
```

**Spec clarification (before any measurement).** Review found one ambiguity:
a private message's delivered nick, as typed or as registered. The spec now says
"as they registered it", and adds that a sender never receives their own
message back.

**Drafter finding.** The first wording of the spec lost five obligations to
CONTEXT (e.g. "A nick is 1 to 16 characters…", "A channel with no members no
longer exists"): the ported rubric does not recognise declarative bounds. The
drafter printed them as *not drafted* rather than dropping them silently, and
the spec was reworded ("A nick must be…"). Teaching the rubric these shapes is
a follow-up.

## Generating it

```sh
go build -o graft ./cmd/graft
graft run --config examples/freeq-lite/graft.json \
  --spec examples/freeq-lite/spec/freeq-lite.md \
  --repo /tmp/fl/repo --root /tmp/fl/app --accept
(cd /tmp/fl/app && go build -o ../server .)
go run ./examples/freeq-lite/oracle -bin /tmp/fl/server
```

`graft run` drafts the spec, accepts the 7 drafts as provenance imprints in
`--repo`, and converges one region per record into `--root` (`running.go`,
`registration.go`, … `everything_else.go`). The model command
(`claude -p --tools "" --strict-mcp-config` in `graft.json`) runs in an empty directory
**with its tools disabled**, so it sees only the prompt (the conventions, the
spec, its section's statements and the files already written) and cannot read
the oracle or the reference from disk. It does inherit graft's environment,
which it needs for its API key. A region lands only if `go build` and
`go vet` pass; evidence for each is pinned to the record's gen_hash and the
region's hash. Re-running with an unchanged spec regenerates nothing, but
re-verifies every region under the current gates (no model call), so a changed
gate is never skipped.

Runs so far (claude sonnet, **one sample each: anecdotes, not measurements**):

| spec / oracle | regions converged | lines | oracle |
|---|---|---|---|
| first wording, 32-check oracle | 7/7, all first attempt | 617 | 31/32 (the PRIVMSG-nick ambiguity) |
| clarified spec, 38-check oracle | 7/7, all first attempt | 608 | **38/38** |

Measurement means the bench arms (graft / one-call baseline / intent-only) at
n≥5 with Wilson intervals; that is the next step.

## Evolving it: change one section, regenerate one region

`spec-v2/` is the same spec plus a **Topics** section (`TOPIC` set/query,
331/332/403/442). The oracle's `-v2` flag adds 4 checks for it (51 total today;
the reference scores 51/51, and a committed mutant covers topic cleanup).

```sh
graft run --config examples/freeq-lite/graft.json --spec examples/freeq-lite/spec/freeq-lite.md    --repo R --root A --accept
graft run --config examples/freeq-lite/graft.json --spec examples/freeq-lite/spec-v2/freeq-lite.md --repo R --root A --accept
```

First run (claude sonnet, n=1, an anecdote; scored by the oracle as it was then, 38 + 4 checks):

| | v1 → v2 with graft |
|---|---|
| drafts mapped | 7 unchanged records (no new versions), 1 new record (Topics) |
| model calls in the v2 step | **1** (`topics.go`) |
| existing files after the v2 step | **7 of 7 byte-identical** |
| v2 oracle, v1 server | 38/42 (the 4 topic checks fail, as they should) |
| v2 oracle, v2 server | **42/42** |

**What this does and does not show.** Topics is an *additive* change: a new
section whose behaviour lives in a new file. For such a change the seven
untouched files are byte-identical **by construction**, since their records did
not change, so graft never rewrites them. It is guaranteed, not earned. Their
evidence is not simply trusted either: every run re-verifies each up-to-date
region under the current gates (#12).

**A change that spans regions is not handled yet.** livecodelife's agent
probed this by adding one line to Topics: "a joiner receives 332 before 353".
That behaviour belongs to JOIN, which `channels.go` owns. With only build and
vet as gates, the model wrapped `handlers["JOIN"]` from `topics.go` and
re-implemented JOIN, against the conventions, and nothing flagged it. The
config now has a third gate, `gates/registry.go`, which refuses any file that
registers a command another file owns. Re-running the probe:

1. Attempt 1 wrapped JOIN again, and the registry gate refused it, naming
   `channels.go`.
2. Attempt 2 then converged by **leaving the JOIN rule out**.

So absorption is now blocked, but a cross-region requirement can still be
dropped silently. Catching that needs either the drafter to notice that a
statement concerns another section's command, so a human can move it into
Channels (whose record would then be revised and regenerated), or per-record
behavioural evals (SPEC §6). Both are open. Until then, graft's change claims
cover changes whose behaviour stays inside the edited sections.

The v1 reference is the v1 spec exactly: it implements Topics only with `-v2`,
and answers `TOPIC` with 421 otherwise. The oracle's own test checks the v1
reference against every v1 check, and the v2 reference against all 51.

## Rendering the skeleton: `graft run --render`

`--render` lets sedum lay out the skeleton (types, signatures, file structure)
and asks the model only for the body helpers in each record's
`<stem>_synth.go`. `skeleton.json` (v1) and `skeleton-v2.json` (v1 + `topics.go`)
describe the layout; pass one with `--skeleton` or the `"skeleton"` key in
`graft.json`. Needs `sedum` on PATH and `SEDUM_GENERATORS` pointing at its
generators.

```sh
SEDUM_GENERATORS=~/sedum/generators graft run --render --config examples/freeq-lite/graft.json \
  --skeleton examples/freeq-lite/skeleton.json --spec examples/freeq-lite/spec/freeq-lite.md --repo R --root A --accept
```

Real runs, **n=1 each, anecdotes** (oracle: 47 checks; model argv wrapped to
sum `total_cost_usd`):

| model | oracle | model calls / attempts | cost | model lines (`*_synth.go`) | sedum lines (other `.go`) |
|---|---|---|---|---|---|
| sonnet, run A | 47/47 | 7 / 7 (all attempt 1) | not recorded | 539 | 207 |
| sonnet, run B | 46/47 (`unknown_command_421_as_sent` fails) | 7 / 7 (all attempt 1) | $1.08 | 554 | 207 |
| haiku | 47/47 | 7 / 7 (all attempt 1) | $0.07 | 630 | 207 |

Every region passed all gates on attempt 1 in all three runs in the table.
(The Sonnet failure described below came from an earlier run, before the gate
fix; it is not a row here.) Two Sonnet runs
of the same input differ by one oracle check, so single scores say little;
and a run can pass every gate and still fail the oracle.

An earlier Sonnet run (not in the table) failed on the Running region: the
registry gate rejected `running_synth.go` for reading `handlers`. The gate now
lets the declaring file's `_synth.go` read `handlers` (index reads and `range`
only: no aliasing, writes, `clear` or passing the map on), and `conventions.md`
says the Running region may read `handlers`.

Notes on `--render`:

- `.sedum/run.log` is append-only and grows on every run, even a no-op one, so
  a committed tree shows it as modified after a re-run. Git-ignore
  `.sedum/run.log` (or ignore it when checking that a re-run is byte-identical).
- Dropping a section (for example v2 back to v1) removes nothing: `topics.go`
  and `topics_synth.go` stay in the tree, with no error and no model call.
  Delete the files yourself if the section is really gone.
- A `_synth.go` that still contains the seeded `graft: not converged` panic is
  never accepted: it counts as pending (regenerated) and a model answer that
  contains it is refused before any check or evidence.

v1 to v2 with `--render` (haiku, n=1, `--skeleton skeleton-v2.json`, spec-v2):
only Topics (`topics_synth.go`) was generated (1 call, about $0.01); the other
7 regions were re-verified as up to date, and the existing files were
byte-identical. The v2 server scored 51/51 on `oracle -v2`.

## Gates beyond build and vet

`graft.json` runs two structural gates written for this architecture:

- `gates/registry.go`: a file may register only its own commands, and outside
  the declaring file `handlers` may only appear as `handlers["CMD"] = ...`.
- `gates/unused`: every package-level function must be reachable from
  `main`, `init`, start-up initializers or tests, using a type-checked call
  graph. Its limits are listed in its header.

`conventions.md` tells the model about the dead-function rule. One run with
both gates (claude sonnet, **n=1, an anecdote**) converged 7/7 regions on the
first attempt with no gate retries, and the 47-check oracle scored it 47/47.
Three of chad-bot's trees generated before the convention existed each fail
the dead-function gate on one unused helper, which in those runs would have
cost a retry; livecodelife's agent reports a Haiku tree that passes it.

## The visible smoke suite

`smoke/smoke.go` is a handful of behaviour checks per spec section (12 in
all), written from the spec alone with its own scenarios. Unlike the oracle it
is **shown to the model** (`"visible"` in `graft.json`) and **runs as a gate**
(kind `eval`): each region's gate checks its own section and every section
generated before it, so a region that drops a rule its section's smoke checks
cover cannot land. It is deliberately small and **never a score**: the
held-out oracle stays the only measure. It must pass 12/12 on the reference
(v1 and v2), which its own test checks.

**Overlap with the oracle, stated plainly.** About 11 of the oracle's 47
behaviours are also exercised, in different scenarios, by the visible smoke
checks. An oracle score from a run that used the smoke gate is therefore not
fully held out: read it as "47/47, of which roughly 11 behaviours the model
could see a check for".

**What this changes in the bench.** Only the graft arms see the smoke suite
and get its gate's retries; the one-call baseline does not. graft vs baseline
then compares the pipeline *including* visible tests and test-driven retries
against a single call without them, a deliberate difference that the
fixture digest records (the visible files are part of it) and the arm table
names.

One run with the smoke gate (claude sonnet, n=1, an anecdote): 7/7 regions
converged on the first attempt, and the held-out oracle scored it 47/47
(with the overlap above).

## Cost: Haiku best-of-N vs Sonnet

`graft run --samples N` draws N candidates per region in one round and keeps
the one that ranks best (gates first, then the smoke-check count). The
question measured here: can Haiku with `--samples 3` (arm B) match Sonnet with
`--samples 1` (arm A) at lower cost? Each run generates freeq-lite v1 from
the spec into a fresh tree; the oracle (held out, never shown to the model) is
run on the result. Cost is the sum of `total_cost_usd` over every model call.
Smoke checks are a gate and a hint, never a score.

**Post-smoke (current main, visible smoke gate on), n=3 per arm:**

| run | oracle /47 | calls | region attempts | cost USD | wall |
|---|---|---|---|---|---|
| A-1 Sonnet x1 | 47 | 7 | 7 | 1.041 | 118s |
| A-2 Sonnet x1 | 47 | 7 | 7 | 1.002 | 92s |
| A-3 Sonnet x1 | 47 | 7 | 7 | 0.991 | 137s |
| B-1 Haiku x3 | 40 (run failed) | 24 | 9 | 0.212 | 403s |
| B-2 Haiku x3 | 47 | 21 | 7 | 0.186 | 285s |
| B-3 Haiku x3 | 47 | 21 | 7 | 0.189 | 223s |

B-1: the Leaving region did not converge in 3 attempts, so graft exited with
an error; the oracle was run on the partial tree and failed 7 checks, all
quit/disconnect behaviour. Oracle scores from these runs overlap the smoke
suite on about 11 behaviours (see above).

**Pre-smoke (binaries built before #21: no smoke gate), n=3 per arm:**

| run | oracle /47 | calls | cost USD | wall |
|---|---|---|---|---|
| A-1 Sonnet x1 | 46 | 7 | 0.855 | 98s |
| A-2 Sonnet x1 | 47 | 9 | 1.149 | 176s |
| A-3 Sonnet x1 | 47 | 7 | 0.869 | 93s |
| B-1 Haiku x3 | 4 | 21 | 0.171 | 226s |
| B-2 Haiku x3 | 46 | 21 | 0.168 | 206s |
| B-3 Haiku x3 | 47 | 21 | 0.174 | 226s |

**What this does and does not show.** Haiku x3 cost about $0.19 per run
against about $1.00 for Sonnet x1, roughly a fifth, and in 4 of 6 Haiku runs
(2 of 3 post-smoke) reached 47/47. But n=3 cannot separate close arms, and
Haiku's variance is large: one pre-smoke run scored 4/47 and one post-smoke
run failed to converge. Sonnet scored 46 or 47 in all six runs. Best-of-3
lowers Haiku's cost per attempt-round but does not remove the occasional bad
region, and it is slower (about 2-4x wall). Read this as "Haiku x3 is a
plausible cheap option that sometimes fails", not as a ranking.
