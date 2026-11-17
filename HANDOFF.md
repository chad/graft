# HANDOFF: graft M1 (branch m1-records)

## Goal
graft's record layer, `internal/records`, on top of `github.com/livecodelife/provenance/record`.
Exported API is pinned and must not change: `Pin`, `Store{Current,Snapshot}`, `OpenDir`, `Stale`, `ErrNotFound`.

## Current task: freeq handoff 01M7NXJD78 "M1 fixes: PR #5 review follow-ups"
Definition of done (from the orchestrator's brief), then push to this branch:
1. `Snapshot` in `internal/records/records.go` re-implements FilesStore's file lookup and skips
   provenance's content-hash check, so a tampered `.versions` file returns no error.
   Call `d.fs.Snapshot(id, version)` first (it runs `UnmarshalSnapshot`, which calls `Verify`
   and checks the envelope id/version/filename), and only then return the raw file bytes.
   Add `TestSnapshotRejectsTamperedVersion` (copy a fixture to t.TempDir, edit the snapshot
   content so the hash no longer matches, expect an error).
2. The traversal case in `TestUnknownIDIsErrNotFound` passes even without `validID`, because its path
   resolves to nothing. Use an id that resolves to a real fixture file, e.g.
   `../../gen-moved/provenance/prov-2026-aaaa0001`, so removing `validID` fails the test.
   Also add a test that fails if the `r.ID != id` check in `Current` is removed
   (a fixture whose file name and inner `id:` differ, put in a new testdata dir or built in a temp dir).
3. gofmt, vet and `go test -race ./...` clean; CI green. Then comment on PR #5 with what changed and
   the new head SHA, and `complete` task 01M7NXJD783RN3462K1S5ECEZ9 with the SHA. #5 merges before #4.

## Done
- 0fc3433: M1 implementation, fixtures (testdata/base, gen-moved, status-only), 6 tests. PR https://github.com/chad/graft/pull/5, CI was green.
- Handoff 01M7NX55BX completed.
- Nothing for the follow-up task has been started; the tree was clean when this was written.

## Gotchas
- Fixtures were generated with provenance's own `Commit` (a throwaway generator, since deleted). To tamper
  in a test, copy a fixture dir to a temp dir first; never edit testdata in place.
- git identity is repo-local (`lcl-graft-records`); the VM has no global one.
- The M2 handoff (01M7NX5EZA) belongs to the other worker; ignore it.
- Do not touch `evidence.Pin`; it is a deliberate separate mirror (M5 adds the adapter).

## Open questions
None.
