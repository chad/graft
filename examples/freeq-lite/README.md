# freeq-lite — the graft demo target

A simplified freeq: a Go chat server speaking a subset of IRC (registration,
channels, channel and private messages, keepalive, quit). No federation, no
identity, no media.

| Path | What | Who may see it |
|---|---|---|
| `spec/freeq-lite.md` | the intent: the only input to generation | graft, models, everyone |
| `oracle/` | the behaviour suite (46 checks over TCP) | the bench only, never a model |
| `reference/` | a hand-written server that exists to validate the oracle | nobody but the oracle's own test |

The oracle was written from the spec **before any code was generated**, imports
nothing from any implementation, and must score the reference 46/46
(`go test ./examples/freeq-lite/oracle`). **Twenty-nine committed mutants** of the
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
go run ./examples/freeq-lite/oracle -bin ./path/to/server          # {"checks":46,"passed":…}
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
331/332/403/442). The oracle's `-v2` flag adds 4 checks for it (50 total today;
the reference scores 50/50, and a committed mutant covers topic cleanup).

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
reference against every v1 check, and the v2 reference against all 50.
