# Contributing

- Branch in this repo (not a fork); one milestone per PR into `main`.
- CI must be green: `gofmt`, `go vet ./...`, `go test -race ./...`.
- Every PR is reviewed by the other implementer before merge.
- A milestone's definition of done is a set of tests named in its handoff.
- Spec changes are PRs to SPEC.md, reviewed the same way.
- graft never links against sedum; it invokes the binary (SPEC.md §2).
