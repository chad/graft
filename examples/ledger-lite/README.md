# ledger-lite: a harder yardstick, with rules that cross sections

ledger-lite is a small in-memory ledger served over HTTP. It covers accounts, deposits and withdrawals, transfers with idempotency keys, holds that are captured or released, a journal of every balance change, and frozen accounts. It was built as a harder case than freeq-lite, for two reasons:

- **Cross-section rules.** The Journal and Freezing sections come last, but they constrain code in every earlier section. Every balance change, by any operation, must append a journal entry. A frozen account must refuse withdrawals, outgoing transfers and new holds. A refused request of any kind must change nothing: no balance, id or journal entry. Code written for Transfers has to honour rules stated two sections later. This is the SPEC §12 silent-drop risk, made the main subject.
- **A small, stated overlap between what the model sees and what is scored.** The held-out oracle tags each check that exercises a behaviour some visible smoke check also exercises. That is **12 of 42**, listed by `oracle -list` and counted in every oracle result as `visible` / `visible_passed`. The bench doesn't record those two fields yet. The count was 9 at first; review found three more checks that smoke also catches, and they are now tagged too. Most oracle checks target what smoke doesn't touch:
  - refusals that must change nothing;
  - ids that refusals must not consume;
  - idempotent replays;
  - holds interacting with withdrawals and transfers;
  - frozen accounts that still receive money;
  - concurrency;
  - undefined methods answering 404 instead of Go's 405.

| file | what | seen by |
|---|---|---|
| `spec/ledger-lite.md` | the spec (7 sections) | the model |
| `conventions.md` | the architecture. The money rules (`Post`, `Debitable`) live in the core, since they span sections | the model |
| `conventions-min.md`, `graft-min.json` | a minimal-conventions variant: file layout only, no architecture | the model, in that variant |
| `skeleton.json` | the `--render` skeleton: core types, `main`, and stubs with doc contracts for every core helper | the model, under `--render` |
| `smoke/` | **visible** smoke suite, 9 checks (one or two per section), gated as `eval` | the model and the gate |
| `oracle/` | the **held-out** suite: 42 checks over HTTP | the bench only, never a model |
| `reference/` | a hand-written server that the oracle scores 42/42 | tests only |
| `case.json` | the bench case (`graft bench run --case examples/ledger-lite/case.json …`) | |

**The oracle has teeth.** `oracle/mutants_test.go` plants 25 bugs in copies of the reference, and each must fail a named check. "A refused request changes nothing" is checked as an invariant: every refusal in every check snapshots all known accounts and the journal before and after, and checks that refuse transfers or holds then probe the next ids. Four of the 25 bugs escaped the first version, before that invariant existed. Most bugs are cross-section drops:

- a withdrawal that ignores holds;
- a capture that isn't journaled;
- a refused transfer that still takes an id;
- a frozen account that accepts new holds;
- a key replay that moves money twice;
- a frozen account's refused withdrawal that still writes a journal entry.

Gates: `go build`, `go vet`, no dead functions, and the visible smoke suite. The dead-function gate is freeq-lite's generic one, referenced as `../freeq-lite/gates/unused/unused.go`, so the config only works from inside the repo layout. If you copy the example elsewhere, copy that gate too and fix the path.

## What it measured so far (n=1 each, anecdotes)

| model | conventions | mode | converged | attempts | oracle |
|---|---|---|---|---|---|
| sonnet | full | plain | 7/7 | all first | 42/42 |
| sonnet | full | `--render` | 7/7 | all first | 42/42 |
| haiku, `--samples 3` | full | plain | 7/7 | all first | 42/42 |
| haiku, `--samples 1` | **minimal** | plain | 7/7 | Journal took 3 | 42/42 |

All four trees were re-scored with the stricter oracle (refusal invariant and id probes), and all four still score 42/42.

Even with minimal conventions, Haiku put a journal into the core while writing Running, because it sees the whole spec as context. So **this case saturates too**, at least at n=1. The cross-section rules are harder to state, but not yet hard for these models. That is itself a finding, and it means a plain-vs-`--render` comparison here will most likely tie again. Levers that are still open:
- a weaker model, such as a local one;
- a spec revision (an evolve step) that changes a cross-section rule after the code exists;
- sections whose text does not name the rules of later sections.

Two things surfaced while building it:
- **Introductory sentences become a region.** A spec's introduction under the `#` title that contains statements such as "amounts are integers" becomes its own record and region, and in one run that region wrote the whole program. Keep the introduction descriptive and put rules in sections.
- **Core helpers must be exported methods.** The dead-function gate runs while only `running.go` exists, so helpers that only later sections call must be exported methods, which the gate counts as used. Otherwise the Running region cannot converge under `--render`.
