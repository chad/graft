# HANDOFF: M2 render fixes (branch m2-render, PR #4)

## Goal
Address orchestrator review of chad/graft PR #4 (freeq handoff 01M7NXJKV3). Keep every exported signature unchanged.

## Definition of done
1. v1 markers, markers with no `body`, and markers with an unknown `v` are *unverifiable*: Render must not refuse on them and must not count them as verified. Tests for each of the three. Over the sedum repo the false "edited" count (257 of 1023) must drop to 0.
2. ReadMarkers returns an error for an unterminated region and for a closing marker that names a different action (like sedum's FindRegions). Markers match only at the start of a trimmed line after a comment prefix (README.md and PRD.md in the sedum repo must yield no regions). Tests.
3. CRLF fixture test (removing the CRLF normalisation must fail a test). Document that Owner.Version is 0 for real v2 markers.
4. After PR #5 (M1) merges: rebase onto main and delete internal/records/pin.go. If #5 hasn't merged, push 1-3, say so on the PR, and complete anyway.
Then: comment on PR #4 with what changed and the new head SHA, post in #phoenix, and `complete` handoff 01M7NXJKV3 with the SHA. gofmt, vet, `go test -race ./...` clean; CI green; TestExecAgainstRealSedum still passes.

## Done
- Base commit 95d6537 (original M2).
- UNCOMMITTED-then-committed in the WIP commit after it: internal/render/render.go rewritten for items 1-2 and 3's doc part:
  - `afterPrefix` (line-start, punctuation-only comment prefix; rejects letters/digits/backtick/quotes), `parseOpen` (returns verifiable flag; v1/no-body/unknown-v give an empty BodyHash), `parseClose`, `parseFile` (errors on unterminated or mismatched close, naming the file).
  - `Region.Edited()` is now false when `Unverifiable()` (BodyHash == ""); new exported method `Unverifiable()`. ReadMarkers propagates parse errors.
  - Owner.Version doc comment added.
- Existing tests still pass; gofmt and vet clean.

## Next
- Write the tests: v1 marker, no-body v2 marker, unknown-v marker (each: Unverifiable, not Edited, Render calls GrowExecute); unterminated region error; mismatched close error; prose/backticked `sedum:` in a .md yields no regions; CRLF fixture (file with \r\n body, hash computed on LF body, must be clean; confirm the test fails if the ReplaceAll in hashBody is removed).
- Run ReadMarkers over ~/sedum and count Edited() regions (expect 0). Note it may now return an error for files in the sedum repo with unterminated/mismatched examples (docs, testdata): decide whether to skip such files or report; sedum's own testdata may legitimately contain malformed regions, so check before concluding anything.
- Rebase after #5 merges and delete internal/records/pin.go.

## Open questions
- The punctuation-prefix heuristic can still match a marker-looking line inside a fenced code block in docs. sedum avoids this by using each package's declared comment prefix per target file; graft has no such info.
- Exec passes `--generators generators` / `--records provenance`; unverified against a real repo layout.

## Gotchas
- This VM needed a repo-local git identity (already set locally).
- Do not touch internal/records beyond deleting pin.go; M1 owns it.
