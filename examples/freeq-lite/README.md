# freeq-lite — the graft demo target

A simplified freeq: a Go chat server speaking a subset of IRC (registration,
channels, channel and private messages, keepalive, quit). No federation, no
identity, no media.

| Path | What | Who may see it |
|---|---|---|
| `spec/freeq-lite.md` | the intent: the only input to generation | graft, models, everyone |
| `oracle/` | the behaviour suite (38 checks over TCP) | the bench only, never a model |
| `reference/` | a hand-written server that exists to validate the oracle | nobody but the oracle's own test |

The oracle was written from the spec **before any code was generated**, imports
nothing from any implementation, and must score the reference 38/38
(`go test ./examples/freeq-lite/oracle`). **Fourteen committed mutants** of the
reference (`oracle/mutants_test.go`) must each fail their named check, so "the
oracle has teeth" is something anyone can re-run. Six of them are bugs
livecodelife's agent wrote that escaped the first, 32-check oracle; each now
has a check.

**Keeping the yardstick out of reach.** Generate into a `--root` and `--repo`
*outside* this repository. The model command runs in an empty directory and
sees only its prompt; gates run in a scratch copy of `--root` only. Nothing in
graft reads `oracle/` or `reference/` except the oracle's own tests.

```sh
go run ./cmd/graft draft examples/freeq-lite/spec/freeq-lite.md   # 7 drafts, 36 statements, all receipts valid
go run ./examples/freeq-lite/oracle -bin ./path/to/server          # {"checks":38,"passed":…}
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
