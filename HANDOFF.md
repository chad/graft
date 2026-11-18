# HANDOFF: M10b language-agnostic --render

## Goal
See BRIEF-M10b.md (not committed) and DESIGN-M10b.md (approved; read "Orchestrator decisions"). Do Step 2.

## Definition of done
`graft run` and `graft run --render` on examples/freeq-lite-ts each converge; the server passes the held-out oracle via `-cmd`; n=1 anecdote with cost in the README; gofmt, vet, `go test -race ./...` and CI green. sedum PR first (contract + Go templates byte-identical + typescript generator + conformance), then graft PR rebased on M10a. `git rm` BRIEF/DESIGN/HANDOFF before the PRs are final.

## Done
- graft 9ba3040: golden test (internal/recording/golden_test.go, testdata/golden/*) pinning Plan's recording.json and all region seeds for examples/freeq-lite. Regenerate only with GRAFT_UPDATE_GOLDEN=1, and only if the bytes are meant to change. Checks pass (gofmt, vet, test -race).
- sedum: branch `m10b-convergence-contract` created from main; no commits yet. Only investigation done.

## Next
1. sedum: add `convergence:` to the manifest (internal/genpkg/schema.go `manifest`, load.go `loadPackage`; strict YAML decoding so the key must be modelled). Reject at load if the seed template lacks "graft: not converged". New files rather than edits where possible. New command `sedum convergence render --generators D --package P` (JSON on stdin and stdout) beside `sedum render` (internal/cli/render.go is the pattern: genpkg.Load with Options{Only}), plus `convergence info`. Register in internal/cli/root.go.
2. Generator `go`: convergence block + templates that reproduce today's recording.go output (stub/sig/seed text) byte for byte; conformance test diffing against the golden seeds.
3. Generator `typescript` (new): per DESIGN section 4.
4. graft: recording.go reads the contract via an injected `Contract` interface (fake in tests). The pipeline needs sedum for Plan. Remove go/parser, go/format and the Go literals. Add the generic check: every helper name the contract returns appears in its seed and stub text. Package name comes from the skeleton, defaulting to profile.name. Rebase on M10a.
5. examples/freeq-lite-ts, README anecdote.

## Gotchas found
- sedum's template grammar has no conditionals or loops: only `{{name|op}}` and `join:`. So the core must supply pre-joined values (params, args, result signature, a `return` prefix, receiver-first variants) and pick between template variants (func/method) in Go.
- No built-in upper-first op, and adding one changes the core's op vocabulary. `pascal` splits words and may not match graft's upperFirst. Plan: core supplies a `Name` (first letter capitalised) input for the helper template.
- The pkg hash (internal/genpkg/pkghash.go) covers actions, their templates and transforms, not the manifest. A `convergence:` block should not change any existing gen/pkg hash, but verify by running sedum's own tests.
- Many sedum core files carry sedum:... marker blocks (cli/generators.go, adopt.go, builtin, pkghash.go and others). Don't hand-edit inside markers. schema.go, load.go and root.go have none.
- Go helper for methods is a method on the receiver (`c.fooBody(args)`); TS must be receiver-first (decided).
- sedum tests locally: `SEDUM_GENERATORS=~/sedum/generators`. recording tests pin sedum v0.12.2 in CI (main_test.go); the graft PR needs a sedum release or pin bump carrying the new command.

## Open questions
- sedum version bump and release: who tags it for graft CI?


## Session 2 update (sedum-only scope): sedum WIP is pushed
Branch `m10b-convergence-contract` in livecodelife/sedum, commit ec6527c. **It does not build yet** (WIP, on purpose).

### Done
- `internal/genpkg/convergence.go`: `Convergence` struct (yaml shape), per-template allowed-value lists, `checkConvergence` (required templates, compiles against the package engine, rejects unknown values, requires the placeholder `graft: not converged` in `seed_decl`), `ConvergenceInfo`. The placeholder is a core constant (`ConvergencePlaceholder`), not a yaml field.

### Contract design (decided while writing; no conditionals/loops in sedum's grammar)
Templates are inline yaml strings (block scalars), not files. Core pre-computes values and picks variants in Go:
- `synth_suffix`, `helper` (stem,name,Name,dir,ext), `param`/`arg`/`spread_prefix`/`spread_arg`, `result.{none,one,many}` (by arity), `call.{func,method}`, `body.{void,value}`, `declare.{func,method}`, `note`, `stub.{func,method}` (values incl. pre-joined `params`, `result`, `body`, `doc`), `import`, `import_use` (Go: keep import only if `<base>.` appears in the declares), `seed_decl`, `seed` (package, imports, decls pre-joined; blank runs collapsed), `package.{entry,entry_dirs,default}` (Go: main for "." and cmd/*, else dir name).
- The seed returned is exactly what graft passes to `converge.Seed` (body ends with `}\n\n`; so seed template ends `{{decls}}\n\n`). graft keeps wrapping it in markers.

### Next (sedum)
1. Wire it: add `Convergence *Convergence \`yaml:"convergence"\`` to `manifest` (schema.go) and `Convergence *Convergence` to `Package`; set it in `loadPackage` (load.go) and call `checkConvergence(pkg, r)` beside `checkSyntax`. Fix the odd `optionalConvTemplates` entries (`"note": false` is a no-op; `package.default` is checked twice) and the `== false` style in checkConvergence. Confirm no gen/pkg hash changes (run sedum's tests).
2. Renderer (new file, e.g. internal/genpkg/convergence_render.go or internal/convergence): input `{files:[{path,package?,imports[],stubs[{name,receiver_name,receiver_type,params[{name,type}],results[],doc}]}]}`; output `{package,synth_suffix,placeholder,files:[{path,synth_path,seed,stubs[{name,helper,stub,declare}]}]}`. Doc = user doc lines + note line, each with the package's comment prefix. synth_path = dir/stem+suffix+ext. Imports sorted. Reproduce internal/recording/recording.go (graft `invocations`, `region`, `paramText`, `resultText`) byte for byte.
3. CLI `internal/cli/convergence.go`: `sedum convergence render --generators D --package P` (JSON stdin/stdout) and `sedum convergence info`; register in root.go (pattern: cli/render.go).
4. generators/go: add the `convergence:` block to sedum.yaml. Conformance test: copy graft's `examples/freeq-lite/skeleton.json` and `internal/recording/testdata/golden/{recording.json,seed-*}` (graft commit 9ba3040) into sedum testdata; assert stub code equals the golden invocations' `code` and seed bodies equal the golden files minus the marker lines.
5. generators/typescript per DESIGN §4 (receiver-first method helpers) + conformance tests; CHANGELOG entry; version bump question below.
6. Run `go vet`, `gofmt -l .`, `go test -race ./...` in sedum; open the PR to livecodelife/sedum main; mention @chad-bot-phoenix in #phoenix; `complete` the handoff.

### Open
- sedum release/tag for graft CI (pins v0.12.2 in main_test.go): who tags?
