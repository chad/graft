# freeq-lite — the graft demo target

A simplified freeq: a Go chat server speaking a subset of IRC (registration,
channels, channel and private messages, keepalive, quit). No federation, no
identity, no media.

| Path | What | Who may see it |
|---|---|---|
| `spec/freeq-lite.md` | the intent: the only input to generation | graft, models, everyone |
| `oracle/` | the behaviour suite (32 checks over TCP) | the bench only, never a model |
| `reference/` | a hand-written server that exists to validate the oracle | nobody but the oracle's own test |

The oracle was written from the spec **before any code was generated**, imports
nothing from any implementation, and must score the reference 32/32
(`go test ./examples/freeq-lite/oracle`). Six deliberate mutations of the
reference (unsorted NAMES, echo to sender, QUIT broadcast per channel, no nick
validation, case-sensitive nicks, no line-length limit) each fail exactly one
check.

```sh
go run ./cmd/graft draft examples/freeq-lite/spec/freeq-lite.md   # 7 drafts, 35 statements, all receipts valid
go run ./examples/freeq-lite/oracle -bin ./path/to/server          # {"checks":32,"passed":…}
```

**Drafter finding.** The first wording of the spec lost five obligations to
CONTEXT (e.g. "A nick is 1 to 16 characters…", "A channel with no members no
longer exists"): the ported rubric does not recognise declarative bounds. The
drafter printed them as *not drafted* rather than dropping them silently, and
the spec was reworded ("A nick must be…"). Teaching the rubric these shapes is
a follow-up.
