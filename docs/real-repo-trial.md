# Real-repo trial: one graft section inside an existing Go service

**Repo:** [benbjohnson/wtf](https://github.com/benbjohnson/wtf), a small but real Go web service. It has about 6,000 lines of non-test Go across the packages `wtf` (domain), `http` (gorilla/mux server, ego templates), `sqlite`, `inmem` and `cmd`. The trial ran on a scratch clone; nothing upstream was touched. Model: Claude Sonnet, n=1 per attempt (anecdotes). Cost: about $1.

**The feature:** one new spec section, "Dial stats". It defines `GET /dials/{id}/stats`, returning JSON with members, min, max and mean of the dial's membership values. Visibility and 404s must match the dial view.

## Before anything ran
The service needs `make`, which generates Go from its ego templates, before it builds at all. Those generated files are gitignored. graft's gates copy the working tree, so they see them once `make` has run. A fresh clone fails every gate until then. That's normal for real repos, but it's a precondition to state.

## Attempt A: graft as it was (sections land in the module root)
- **Result:** converged on attempt 2, and every gate passed (`go build`, `go vet`, `go test ./...`).
- **But the feature didn't work:**
  - **Wrong package.** On attempt 1 the model wrote `package http` into the root directory, and the build failed with two packages in one directory. On attempt 2 it adapted and put an HTTP handler, using `net/http` and `gorilla/mux`, into the **domain package `wtf`**. That breaks the repo's layering.
  - **Unreachable.** The handler was exported but **never registered on any route**, and graft can't edit the hand-written router. Nothing served the endpoint.
  - **"guarantee: checked" was true and meaningless.** The repo's existing tests can't know about a feature they predate.

## Attempt B: with what a real repo needs
Three things were supplied by the human, the way a user of graft would:
1. **Placement:** a new config key, `"files": {"Dial stats": "http/dial_stats.go"}`, added in this PR.
2. **A wiring hook:** one hand-written line in `registerDialRoutes`, `s.registerDialStatsRoutes(r)`. The conventions name the function the section must define.
3. **An acceptance test:** a hand-written `http/dial_stats_test.go` (stats for 3 members, an empty dial, an unknown dial → 404), run by the `go test ./...` gate.

**Result:**
- **Converged on the first attempt.** The 74-line `http/dial_stats.go` is in package `http` and follows the existing handlers' style.
- **The whole suite passes,** including the new test.
- **No hand-written file changed** (`git diff` is empty).
- **A re-run made no model calls** ("up to date, re-verified").
- **A hand edit inside the region was refused**, with exit 3 and the edit kept.

## What this says
- **The core guarantees hold in a real repo.** Hand-written code is untouched, re-runs are idempotent and hand edits are protected.
- **"Checked" is only as good as the gates.** Without an acceptance test written for the new section, graft converged a feature that nothing could reach. In an existing repo the user's job is placement, a wiring hook and a test; graft's job is everything inside the section.
- **Open: context cost.** The prompt includes every `.go` file under the root, recursively: here 277 KB of Go, including tests and generated templates, which is roughly 70k tokens per call. A config option to scope what the model is shown (say, the target package plus the interfaces it uses) is the next thing a real repo needs.
- **Open: wiring.** A hook line in a hand-written file is the honest minimum today. Whether graft should own small edits to hand-written files is a design question, not a bug.
