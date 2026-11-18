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

## State of the tests
`go vet ./...` and gofmt on the sources are clean. Last run of `go test -race ./internal/recording`: only `TestRecordingGrowsWithRealSedum` fails, because the test helper `fill` writes unformatted `fill_*.go` files and the test then gofmt-checks them. Fix the helper (run `gofmt` on its output, or `go/format.Source`), then re-run. The other tests passed in the last run, but check `TestGrowCheckCleanAfterGrow` (the earlier orphan failure came from synthetic pins with no provenance files, now fixed by `writeRecords`).

## Next
1. Fix `fill` formatting; get all tests green with `-race`.
2. TestFreeqLiteBlueprintsPlan currently builds the skeleton and then **skips** the oracle part. Oracle is not on main (it is in PR #11, flags in PR #12); once #11 merges, rebase and run the oracle against the built binary: connect must work (exit 0 or 1, not 2). `main` in the skeleton listens on `-addr` and calls the stub `serve`.
3. Add a sedum install step to `.github/workflows/ci.yml` (`~/sedum/install.sh`; `SEDUM_INSTALL_DIR`, `--no-model`), plus provide generators for the test (a sedum checkout, or set `SEDUM_GENERATORS`), and set CI so skips become failures.
4. Run gofmt, vet and `go test -race ./...`. Push, then `gh pr create --repo chad/graft --base main` (the GitHub MCP is down; use `gh`).
5. Mention @chad-bot-phoenix in #phoenix with the PR link; `complete` the handoff.

## Open questions
- Does converge accept the stub/helper split (option A), or does the orchestrator want a different one? Plan's `Regions` is the interface converge would consume.
- Should the skeleton be a `skeleton.json` beside conventions.md in the real flow (my proposal)? Who writes it for new specs?

## Gotchas
- `render.Exec.GrowCheck` runs `--records provenance`, so real record files must exist in the dir or every region is an orphan.
- `generators/` must be removed before `go build ./...` / gofmt, because the templates are not Go.
- `freeq ask` is refused at my tier; use `say`. Never post secrets or absolute paths on freeq.
