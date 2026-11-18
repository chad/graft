# HANDOFF: M7 review fixes on PR #15 (branch m7-recordings)

## Goal
Fix every finding in the orchestrator's review of PR #15 (latest comment). Handoff 01M7PDF8ET1Y9ZSJYX0FR7ZBJH.

## Definition of done
Review findings all fixed; gofmt, vet, `go test -race ./...` clean with SEDUM_GENERATORS=~/sedum/generators; `git rm HANDOFF.md` (notes go in the PR body); push; say in #phoenix what changed; `complete` the handoff. Do NOT touch .github/workflows.

## Done (WIP commit, production code only; recording tests are STALE and fail)
- `internal/records/records.go`: Pin now carries Type, Status, AffectedScope.
- `internal/converge/region.go`: added exported `Seed(comment, Owner, body)` (wraps renderRegion; additive, converge behaviour unchanged).
- `internal/recording/recording.go`: rewritten Plan.
  - Each region is its own `<stem>_synth.go`, whole file = one graft:synth region (package clause, imports, helpers), seeded up front with `panic("graft: not converged")` Body helpers (`Region.Seed`, `Region.Path`, `Region.Tier`). Imports in the seed are the file's imports whose package name appears in the helper signatures.
  - `Input.Tier` is required (written into the marker).
  - Imprints only (type imprint; status open or implemented); `affected_scope` must equal the planned files (sorted); every record needs a file; `Complete: true` in the recording.
  - `File.Record` (id) or `File.Section`, exactly one; `File.Package` override; `Stub.Doc` becomes the stub's doc comment.
  - `validateSkeleton`: duplicate paths, bad/duplicate imports, decl code parsed with go/parser (kind, name, receiver must match), duplicate decl/stub/helper names per dir (init exempt), receiver_name/receiver_type both-or-neither, `_synth.go` collisions.
- `examples/freeq-lite/skeleton.json`: `fallback.go` -> `everything_else.go`; `const serverName`; `Client.conn net.Conn`; doc contracts on stubs: Prefix returns `nick!user@freeq-lite` WITHOUT the leading ':' and every sender adds it; Reply builds the full `:freeq-lite CODE target :text` line (target `*` before a nick); Send only appends CRLF; Disconnect closes c.conn.

## Next (all in internal/recording/recording_test.go)
1. `writeRecords`: write fixtures as `type: imprint`, `implements: <some parent id>`, `status: open`, `affected_scope: [<the file path whose section matches>]` (read the skeleton to map section -> path). Check the records store accepts an imprint without a real parent; if not, write a parent blueprint too. Set `Input.Tier` (use a tier name from the test policy, e.g. "low").
2. Replace `fill` with: write each `Region.Seed` to `Region.Path` (package must build BEFORE any fill: add a `go build` assertion right after grow + seed), then fill via `converge.Seed` or, better, the real `converge.Converger` with a `go build` CommandGate per region (see converge_test.go for Policy/Log/Records setup). Then assert `grow --check` and a regrow still exit 0 after fill, gofmt clean.
3. Tighten `TestHandlerBodiesAreConvergenceRegions`: every stub (not any one) is a pure delegation; Region.Path is `<stem>_synth.go`, not a sedum-owned file; seed is a single region owned by the record.
4. `TestGrowCheckCleanAfterGrow`: with scope set it must report N current, and a hand edit to a sedum file must make it fail.
5. Tests for the new validation (imprint-only, scope mismatch, record with no file, dup names, decl name/kind mismatch, stub receiver half-set).
6. Oracle half of TestFreeqLiteBlueprintsPlan still skips (PR #11 not merged); read the spec from examples/ once #11 lands instead of the testdata copy.
7. PR body: say version isn't checked in TestRegionsOwnedByRecords (v2 markers carry no version, render.go:28); list the design (separate synth file) and the sedum CI step still needed from chad-bot. `git rm HANDOFF.md`, push, post in #phoenix, `complete`.

## Open questions
- Tier source: I made it a required `Input.Tier`; a per-record tier may be wanted later.
- Status accepted: open and implemented only.

## Gotchas
- Local run needs `SEDUM_GENERATORS=~/sedum/generators`.
- `go test ./internal/recording` currently fails ("Input.Tier is required") because tests are not updated yet.
