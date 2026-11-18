# HANDOFF: M9 best-of-N (branch m9-best-of-n)

## Goal
`graft run --samples N` best-of-N per region, then measure Sonnet --samples 1 (Arm A) vs Haiku --samples 3 (Arm B) on freeq-lite v1, n=3/arm, held-out oracle, cost; README section "Cost: Haiku best-of-N vs Sonnet". Brief: BRIEF-M9-best-of-n.md (uncommitted, repo folder).

## Definition of done
Brief tests 1-5 pass; gofmt/vet/`go test -race ./...` clean; measurement + README; `git rm HANDOFF.md`; PR to chad/graft main; mention @chad-bot-phoenix in #phoenix; complete freeq handoff 01M7QYSFRYREJFW0S0HE5KBW1S.

## Done
- Code complete and rebased onto main incl. #21 (6198296). Tests 1-5 pass, gofmt/vet/race clean after rebase. Smoke ranking key parsed in closer() (samples.go). Sampling branch wired into run.go attempt loop.
- Measurement harness in the session scratchpad dir `m/`: `claudecost.sh` (logging wrapper, appends total_cost_usd to $COSTFILE), `arm.sh <name> <model> <samples> <run#>` (generates outside the repo, builds, runs the oracle, appends a line to `results.txt`), prebuilt `graft`/`oracle` binaries, `fl/` copy of examples/freeq-lite without oracle/reference. Per-run logs in `m/runs/<arm>-<n>/` (log, cost, oracle.json).
- A background loop was launched: B-2, B-3, then A-1..A-3, then appends DONE to results.txt. It may still be running; check results.txt before starting anything (do not start a second copy).

## Results so far
- B-1 Haiku x3: oracle 4/47, 21 calls, $0.171, 226s.
- B-2 Haiku x3: oracle 46/47, 21 calls, $0.168, 206s.
- B-3, A-1..A-3: pending (see results.txt).
- CAVEAT: these binaries were built BEFORE the rebase onto #21, so the runs are pre-smoke (no smoke gate/visible checks). Orchestrator wants post-#21 runs reported as the main result and pre-smoke ones listed separately. Rebuild graft from the branch (`go build -o m/graft ./cmd/graft`) and check how the config enables the smoke gate (examples/freeq-lite/smoke/, graft.json/case.json on main) before rerunning; the m/fl copy is stale and needs refreshing from the repo.

## Next
1. Wait for/collect pre-smoke results; refresh m/fl from the rebased repo, rebuild, run post-#21 arms (A and B, n=3; optional C: Haiku x3 --render).
2. Per run record: oracle score, calls, attempts per region (grep "attempt" in log), cost, wall time.
3. README section with table + honest paragraph (n=3 cannot separate close arms; B-1 vs B-2 already shows high variance: 4/47 vs 46/47). Smoke is never reported as a score.
4. git rm HANDOFF.md, push, open PR, mention @chad-bot-phoenix in #phoenix, complete the handoff.

## Gotchas
- Never post absolute paths/secrets on freeq. Don't touch examples/freeq-lite/smoke/.
- Sonnet runs cost ~$1 each.

## Update: all 6 pre-smoke runs finished (binaries built before #21; oracle passed /47)
| run | oracle | calls | attempts (regions) | cost USD |
|---|---|---|---|---|
| A-1 Sonnet x1 | 46 | 7 | 7 | 0.855 |
| A-2 Sonnet x1 | 47 | 9 | 9 | 1.149 |
| A-3 Sonnet x1 | 47 | 7 | 7 | 0.869 |
| B-1 Haiku x3 | 4 | 21 | 7 | 0.171 |
| B-2 Haiku x3 | 46 | 21 | 7 | 0.168 |
| B-3 Haiku x3 | 47 | 21 | 7 | 0.174 |
Wall times are in results.txt in the scratchpad m/ dir. Remaining: post-#21 reruns (or report these as pre-smoke), README, PR.
