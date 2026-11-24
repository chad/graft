# ledger-lite: a harder yardstick, with rules that cross sections

ledger-lite is a small in-memory ledger served over HTTP. It covers accounts, deposits and withdrawals, transfers with idempotency keys, holds that are captured or released, a journal of every balance change, and frozen accounts. It was built as a harder case than freeq-lite, for two reasons:

- **Cross-section rules.** The Journal and Freezing sections come last, but they constrain code in every earlier section. Every balance change, by any operation, must append a journal entry. A frozen account must refuse withdrawals, outgoing transfers and new holds. A refused request of any kind must change nothing: no balance, id or journal entry. Code written for Transfers has to honour rules stated two sections later. This is the SPEC §12 silent-drop risk, made the main subject.
- **A small, stated overlap between what the model sees and what is scored.** The held-out oracle tags each check that exercises a behaviour some visible smoke check also exercises. That is **12 of 42**, listed by `oracle -list` and counted in every oracle result as `visible` / `visible_passed`. Bench samples record them too (`visible`, `visible_passed`), so the strictly held-out part, checks minus visible, can always be read off. The count was 9 at first; review found three more checks that smoke also catches, and they are now tagged too. Most oracle checks target what smoke doesn't touch:
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

## ledger-lite-evolve: the revision that crosses sections (BENCH2)

The README above ends with the open lever "a spec revision that changes a cross-section rule after the code exists". This is that lever. `graft bench run --case examples/ledger-lite/case.json --evolve` builds `spec/` and then changes the same tree to `spec-v2/`; the result is scored by `oracle -v2` and recorded as `ledger-lite-evolve`.

**The change (two edits, one case).** Neither section names the code it affects.
- *Freezing:* a frozen account also refuses money coming in. Deposits to it and transfers to it answer 423 `account frozen`. (v1: it still received.)
- *Journal:* every entry carries `"balance"`, the account's balance right after the change (the account view's `balance`, not `available`).

Where the fix has to go, in the architecture `conventions.md` describes: `Post` and the `Entry` type are in the core (`running.go`); the deposit and transfer routes are in `deposits_and_withdrawals.go` and `transfers.go`. The records that change are Journal and Freezing, whose files hold only route registration. Nothing in either changed section is where the edit belongs. That is the point, and it is a prediction about the arms, not a result.

**What graft regenerates for this change** (`internal/pipeline/ledger_evolve_test.go`, real pipeline, fake models, real case files; it says nothing about what a real model writes):

| arm | model asked to write in the change step | left alone |
|---|---|---|
| graft | `freezing.go`, `journal.go` | `running.go`, `deposits_and_withdrawals.go`, `transfers.go`, `accounts.go`, `holds.go` |
| graft-render, `skeleton-v2.json` | `freezing_synth.go`, `journal_synth.go`, `running_synth.go` | `deposits_and_withdrawals_synth.go`, `transfers_synth.go`, `accounts_synth.go`, `holds_synth.go` |
| graft-render, `skeleton-v2-nocredit.json` | the same three | the same four |

So with graft, neither the core (where `Balance` goes in `Post`) nor the deposit and transfer code is regenerated. With `--render`, the shape of Running changes (`Entry.Balance`, `Post`'s contract), so Running is regenerated and the journal half is reachable. In every variant the Deposits and Transfers regions are left byte-identical, because their records did not change, so the credit gate (frozen receivers) is reachable only if a regenerated region reaches into their routes. The baseline arm sees the whole file and has no such boundary. Expect a low score on the frozen checks for the graft arms whatever the model, and read that as the pipeline's invalidation boundary, not as model skill. The step-1 score (below) separates it from a build defect.

| file | what |
|---|---|
| `spec-v2/ledger-lite.md` | the changed spec (two bullets differ) |
| `oracle -v2` (`oracle/v2.go`) | 50 checks: the 41 v1 checks that still hold, `frozen_refuses_incoming` (replaces `frozen_still_receives`), and 8 new ones. All 9 are held out: the smoke suite reads an entry's account, delta and kind only, and never sends money to a frozen account, so none of them is tagged `visible`. Without `-v2` the oracle is the 42-check v1 suite, unchanged |
| `reference-v2/` | a hand-written v2 server that `-v2` scores 100%; `reference/` stays v1 (42/42 without `-v2`). The diff between them (about 12 lines) is the change |
| `oracle/mutants_v2_test.go` | 14 v2 mutants, each failing a named `-v2` check, and the v1 mutants rerun on `reference-v2` under `-v2` |
| `skeleton-v2.json` | the render arm's skeleton for the change: `Entry.Balance`, `Post`'s contract updated, and a new `Creditable` stub (credits are gated the way `Debitable` gates debits) |
| `case.json` | the default case (`evolve` points at `spec-v2`, `-v2`, `skeleton-v2.json`) |
| `case-nocredit.json`, `skeleton-v2-nocredit.json` | the control, recorded as `ledger-lite-nocredit-evolve`: the same change, but the skeleton gets only `Entry.Balance` and `Post`'s contract, not `Creditable`, so the render arm is not told where the credit gate lives |

**`conventions.md` is unchanged for the evolve case and is silent on credits on purpose.** It says `Debitable` applies every rule for taking or reserving money and says nothing about money coming in; a conventions file that anticipated the change would hand every arm the answer.

**Not specified, not tested:** a keyed transfer replayed after its receiver was frozen (200 replay or 423). The spec does not say, and the oracle does not score it. Likewise the order of refusals when a transfer fails two ways at once (sender short of funds, receiver frozen).

**Reading a result.** Every evolve sample records the step-1 tree's score under the v1 oracle (`pre_checks`/`pre_passed`, shown by `graft bench report`), so "built it wrong before the change" is separate from "did not make the change". With `--render`, `change_calls` counts Running as well as Journal and Freezing and is not comparable with the graft arm's.
