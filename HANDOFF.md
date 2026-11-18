# HANDOFF: M8 graft run --render (branch m8-run-render)

## Goal
`graft run --render`: sedum lays out the skeleton, the model converges only each record's `<stem>_synth.go` Body helpers. Full brief: BRIEF-M8-run-render.md (not committed, in the project folder).

## Definition of done
Tests TestRunRenderFakeModel, TestRunRenderRerunNoop, TestRunRenderEvolve, TestRunRenderRefusesHandEditOfSedumFile; one real Sonnet run scored by the oracle, recorded in examples/freeq-lite/README.md as an anecdote (n=1: score, model calls, attempts, model lines vs sedum lines); gofmt/vet/`go test -race ./...` clean; PR to chad/graft main; tell chad-bot-phoenix in #phoenix and the orchestrator; `complete` freeq handoff 01M7PPP58N0FM5HQ80HAMJSYVE. `git rm HANDOFF.md` before the PR is final.

## Done (WIP commit on this branch; see `git log`)
- internal/pipeline/render.go: `RenderConfig{Skeleton, Sedum, Generators}`, `Config.renderTree` (Plan -> write recording to a temp dir -> render.Render -> GrowCheck -> seed _synth.go only if missing), `sedumEnv` (copies generators + symlinks repo/provenance into root for the sedum calls, removes both after), `renderPrompt` (Declares signatures + read-only tree, region-file-only answer).
- internal/pipeline/run.go: `Config.Render *RenderConfig`; when set, renders before ReadRegions, then converges each record on its region path (`file`) instead of Entry.File; records with no region are skipped. `current()` takes the file. Non-render path unchanged; existing pipeline tests pass.

## Next
1. Interface changes agreed with chad-bot (brief's last section): replace `RenderConfig` with `Config.Skeleton *recording.Skeleton` and `Config.Sedum render.Sedum` (nil = whole-file path). Generators dir then needs a home (e.g. `Config.Generators` or a field on the Sedum value). Export `recording.IsSynthPath(p string) bool` (suffix `_synth.go`, internal `synthPath` already builds it) and use it in the pipeline. Add `Version(ctx)` use: record the sedum version in Progress output.
2. cmd/graft/run.go: `--render`, `--skeleton PATH` (default skeleton.json beside graft.json; graft.json `"skeleton"` key in convergeConfig in cmd/graft/converge.go), locate sedum on PATH, generators from `SEDUM_GENERATORS` or ~/sedum/generators.
3. examples/freeq-lite/skeleton-v2.json: v1 skeleton plus a topics.go file (section "freeq-lite › Topics", one `init` stub, like keepalive.go). Plan rejects a skeleton section with no record, so v1 cannot use a superset skeleton; that is why a second file is needed.
4. The four tests (skip when sedum is absent unless CI; reuse the TestMain provisioning pattern from internal/recording/main_test.go, copy it into internal/pipeline).
5. Real run + README anecdote (generate outside the repo).

## Open questions / gotchas
- Not yet run against real sedum at all: confirm GrowCheck passes with the provenance symlink in root, and that Render's ReadMarkers is happy with generators/ present.
- Accept sets affected_scope to `fileFor(section)` (one file); Plan requires scope == planned skeleton files, so skeleton file paths must equal fileFor(section) (they do for freeq-lite: running.go, registration.go, ...).
- A changed record leaves the old synth body in place and its owner pin stale; Run's `current()` then regenerates it (existing behaviour). The render prompt does not show the old body yet.
- Tests for hand-edit refusal: render.Render already refuses with ErrHandEdited before any model call; assert that error text.
