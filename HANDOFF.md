# HANDOFF: PR #19 review fixes (branch m8-run-render)

Goal: fix REVIEW-PR19.md findings 1, 2, 3, Lows 4, 5, 8, 9, README notes for 6, 7. No real model runs.

Definition of done: fixes + tests, gofmt/vet/`go test -race ./...` (with sedum, SEDUM_GENERATORS=~/sedum/generators) clean, pushed, orchestrator told which commit fixes which finding.

Done (all checks clean at cc8f1e0):
- 12de4b5: finding 2 (registry gate read-only for declarer `_synth.go`, tests for alias/clear/write/etc.), nit 8e (conventions.md).
- fe68330: finding 3 (LoadSkeleton in CLI), nits 8b (--skeleton without --render), 8c (converge rejects "skeleton"); CLI test.
- b4b3cf4: finding 1 (pending from region body via sentinel; placeholder answers refused before Converge; tests: tier change keeps seeds pending, placeholder model refused, no evidence), finding 4 (generators/provenance check), finding 5 (copy to generators.partial then rename; test), nit 8a (IsSynthPath used in renderTree), 8d (comment).
- cc8f1e0: README notes for 6, 7, 9.

Next: nothing required; wait for review. Open: commits group several findings per commit (files interleave), not one per finding.

Gotchas: echoModel in render_test.go now replaces the placeholder panic (otherwise refused). Do not rename Config.Skeleton/Sedum/Generators or recording.IsSynthPath (bench uses them). HANDOFF.md should be dropped before merge.
