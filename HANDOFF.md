# HANDOFF: M8 graft run --render (branch m8-run-render)

## Goal
`graft run --render`: sedum lays out the skeleton, the model converges only each record's `<stem>_synth.go` Body helpers. Brief: BRIEF-M8-run-render.md (not committed; read only its last section, which now includes an Addendum: do the real run with BOTH sonnet and haiku, report each as n=1).

## Definition of done
Four tests (done); real runs with Sonnet AND Haiku scored by the oracle, recorded in examples/freeq-lite/README.md as anecdotes (n=1 each: score, model calls, attempts, model lines vs sedum lines); gofmt/vet/`go test -race ./...` clean; `git rm HANDOFF.md`; PR to chad/graft main (`gh pr create --repo chad/graft --base main`); tell chad-bot-phoenix in #phoenix and the orchestrator; `complete` freeq handoff 01M7PPP58N0FM5HQ80HAMJSYVE.

## Done
- ae87918: Config.Skeleton/Sedum/Generators, recording.IsSynthPath, sedum version in Progress, `--render`/`--skeleton` flags (graft.json "skeleton" key), examples/freeq-lite/skeleton-v2.json (v1 + topics.go; chosen because Plan rejects a skeleton section with no record).
- 6d68ba1: seeded placeholders stay "pending" until converged (previously a seed looked current, so no model call was made); internal/pipeline/render_test.go (4 tests, use an echo fake model, skip without sedum/SEDUM_GENERATORS) and main_test.go (CI provisioning). Full `go test -race ./...` passed with SEDUM_GENERATORS=~/sedum/generators.

## Next
1. First real run (sonnet) failed on the Running region: `registry ownership` gate reports `running_synth.go:80: handlers may only be used as handlers["CMD"] = ... outside running.go`. The serve body legitimately dispatches via `handlers` and lives in running_synth.go. Fix: make examples/freeq-lite/gates/registry.go treat `<declarer stem>_synth.go` (running_synth.go) as part of the declaring file (see `declarer` and the check at ~line 59); add/adjust its test in examples/freeq-lite/gates. Also note in conventions.md that the Running region may read `handlers`. Re-run gates tests.
2. Rerun: `go build -o $SCRATCH/graft ./cmd/graft`; `SEDUM_GENERATORS=~/sedum/generators graft run --render --config examples/freeq-lite/graft.json --spec examples/freeq-lite/spec/freeq-lite.md --repo $W/repo --root $W/app --accept` with W outside the repo. Build `$W/app` to `$W/server`, score with `go run ./examples/freeq-lite/oracle -bin $W/server`. For haiku, copy graft.json to $W with `--model haiku` and a conventions copy (or sed the argv) and pass that --config.
3. Also record each real run's cost (total_cost_usd, per the brief Addendum; the claude CLI JSON output has it, so the model argv may need `--output-format json` handling, or sum it from the CLI's own report) in the README anecdote.
   Count: model calls/attempts (progress output "attempt N"), lines in *_synth.go (model) vs other .go (sedum).
4. README anecdotes (sonnet + haiku, n=1 each), plus a short `--render` usage section; also run the v1 -> v2 evolve once if cheap (skeleton-v2.json + spec-v2) and note which synth region regenerated.
5. gofmt -l ., go vet ./..., go test -race ./..., `git rm HANDOFF.md`, push, PR, announce in #phoenix, complete handoff.

## Gotchas
- sedum appends to .sedum/run.log every run (tests exclude it from byte-identical comparisons).
- sedumEnv temporarily copies generators/ and symlinks provenance into the root, removed after each render; graft's GrowCheck in tests needs the same (growCheck helper).
- Haiku can pass all gates yet fail the oracle (orchestrator note); that is expected data, not a bug.
