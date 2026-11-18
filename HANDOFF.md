# HANDOFF: M7 records → sedum recordings (branch m7-recordings)

## Goal
Deterministic `Plan(records) → sedum recording` in `internal/recording`, laying out the Go skeleton with sedum's `go` generator. No model calls. `Write(w, Recording)` emits sedum's on-disk JSON. Brief: BRIEF-M7-recordings.md (uncommitted, in the project folder).

## Definition of done
The 6 tests from the brief, plus gofmt / vet / `go test -race ./...` clean, a sedum install step in CI (not there yet), a PR to chad/graft main, a mention of chad-bot-phoenix in #phoenix, and `complete` on freeq handoff 01M7PDF8ET1Y9ZSJYX0FR7ZBJH.

## Done (this WIP commit)
- `internal/recording/recording.go`: `Plan(Input{Pins, Skeleton}) (Planned{Recording, Regions}, error)`, `Write`, `LoadSkeleton`. Deterministic: records are sorted by id, imports are sorted, and map keys are sorted by the JSON encoder.
- **Design decision taken (option A from the old handoff; nobody ruled on it, so revisit if the orchestrator or chad-bot disagrees):** sedum renders fixed decls (type/var/const/func) from the skeleton. Each open-bodied function is a *stub* that only delegates to `<file stem><Name>Body`, and `Planned.Regions` lists those helper signatures per file/record. Converge must declare them in its synth region. Converge is untouched.
- `internal/recording/testdata/freeqlite.skeleton.json`: a 7-file freeq-lite skeleton (running.go owns Server/Client/Message/handlers/main plus stubs; six other files each hold an `init()` stub). I could not get the 7 drafted blueprints (they were never committed on any branch), so the test uses 7 synthetic real provenance records that it writes itself.
- `internal/recording/recording_test.go`: the 6 brief tests plus a bad-input test. The tests copy sedum's generators from `$SEDUM_GENERATORS` or `~/sedum/generators` (the release binary does not carry them; they skip locally if absent, and fail under `CI`).
- Found with real sedum: a fixed `func` decl is action `setFunction` (fixed).

## Update (orchestrator confirmed option A; fixtures now from the drafter)
- Skeleton is `examples/freeq-lite/skeleton.json` (files keyed by spec *section*); records come from running `internal/drafter` on `internal/recording/testdata/freeq-lite.md` (a copy of PR #11's spec) inside the test: 7 valid blueprints written as real provenance records, ids derived from section.
- All tests pass locally with `-race`; gofmt and vet clean; CI gained a sedum install step (unverified until CI runs it).
- Remaining: oracle half of TestFreeqLiteBlueprintsPlan skips until PR #11 merges (oracle is not on main). Then rebase, build the skeleton and run the oracle: connect must work (exit 0 or 1, not 2). When #12 lands, move skeleton.json beside its conventions.md (already examples/freeq-lite/).
