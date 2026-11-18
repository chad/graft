# freeq-lite — the graft demo target

A simplified freeq: a Go chat server speaking a subset of IRC (registration,
channels, channel and private messages, keepalive, quit). No federation, no
identity, no media.

| Path | What | Who may see it |
|---|---|---|
| `spec/freeq-lite.md` | the intent: the only input to generation | graft, models, everyone |
| `oracle/` | the behaviour suite (43 checks over TCP) | the bench only, never a model |
| `reference/` | a hand-written server that exists to validate the oracle | nobody but the oracle's own test |

The oracle was written from the spec **before any code was generated**, imports
nothing from any implementation, and must score the reference 43/43
(`go test ./examples/freeq-lite/oracle`). **Twenty-one committed mutants** of the
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
go run ./examples/freeq-lite/oracle -bin ./path/to/server          # {"checks":43,"passed":…}
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
