# HANDOFF: M9 best-of-N (branch m9-best-of-n)

## Goal
`graft run --samples N`: N concurrent model answers per attempt, Check all, Commit the smallest-diff satisfied one; closest-candidate feedback if none pass. Then measure Sonnet --samples 1 vs Haiku --samples 3 on freeq-lite v1 (n=3/arm, held-out oracle, cost) and write README section "Cost: Haiku best-of-N vs Sonnet". Full brief: BRIEF-M9-best-of-n.md (uncommitted, in the repo folder).

## Definition of done
Brief tests 1-5 pass; gofmt/vet/`go test -race ./...` clean; measurement + README; PR to chad/graft main; mention @chad-bot-phoenix in #phoenix; complete the freeq handoff; `git rm HANDOFF.md` before the PR is final.

## Done (commit 0058083, WIP)
- internal/converge/converge.go: `Check` / `Commit` / `Trial` / `ErrNotSatisfied`; `Converge` = Check+Commit (unsatisfied returns Decision, nil as before). All existing converge tests pass.
- TestConvergeCheckCommitEquivalent (test 1) passes.
- internal/pipeline/samples.go: `attemptSamples` (concurrent calls, Check all, smallest diff wins with lowest-index tie-break, closest-candidate feedback, `diffLines` LCS), `Config.Samples`, `Outcome.Samples/Calls`, per-region "sampled N, k satisfied, chose #i (diff d lines)" progress line.
- cmd/graft/run.go: `--samples` flag, prints total model calls.
- internal/pipeline/samples_test.go: tests 2-5 + TestDiffLines.

## Next
1. Tests 2-5 in samples_test.go currently FAIL (test 1 and TestDiffLines pass). Symptoms: out[1].Calls == 0 with Samples 3 and Attempts 1 (so Calls is not being counted: check that the sampling branch in run.go is actually taken and that `o` is updated via the pointer, and that the `continue` after the branch doesn't skip the `out = append(out, o)` ordering), and the winner was the BIG body, not the small one. Debug these first; the bug is likely in the run.go branch wiring or in the test's seqModel (it counts `k` only for extra.go prompts).
2. Samples 0/1 path must stay byte-identical (it increments o.Calls only).
3. Measurement: logging wrapper script OUTSIDE the repo around `claude -p --output-format json ...` appending total_cost_usd; argv `claude -p --tools "" --strict-mcp-config --model <sonnet|haiku>`; generate outside the repo per README recipe; 3 runs per arm; record oracle score, calls, attempts per region, cost, wall time. Optional Arm C (Haiku x3 --render). Smoke is never reported as a score.
4. README section + PR.

## Open questions
- Smoke-count ranking key: skipped; TODO left in `closer()` (samples.go) naming the parse point (eval gate detail), since chad-bot's gate may not print a count yet.
- I added "checked over refused" as a tie-break after gates passed in `closer()` (not in the brief).
- Partial model-call failures: failed calls are dropped, error only if all N fail (brief silent).
- A same-diff tie test is not possible with an identical-prompt fake (index->body mapping is racy); not written.

## Gotchas
- `go build` needs network on first run (module download).
- Do not touch examples/freeq-lite/smoke/.
- Never post absolute paths/secrets on freeq.
