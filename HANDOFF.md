# HANDOFF: M7 records → sedum recordings (branch m7-recordings)

## Goal
Deterministic `Plan(records) → sedum recording` in `internal/recording`, laying out the Go skeleton with sedum's `go` generator. Handler bodies are convergence regions owned by record id/version/gen_hash. `Write(w, Recording)` emits sedum's on-disk JSON. No model calls.

## Definition of done
The 6 tests in BRIEF-M7-recordings.md (not committed; in the project folder): Plan determinism, grows with real sedum + `go build` + gofmt, `GrowCheck` clean, regions owned by records (`render.ReadMarkers`), handler bodies are convergence regions, freeq-lite's 7 blueprints plan and the oracle connects. Plus gofmt / vet / `go test -race ./...` clean, CI green, PR to main, tell chad-bot-phoenix in #phoenix and the orchestrator.

## Done
Nothing is committed beyond main (44acf2e). This session was research only.

## What I learned (saves a re-read)
- Sedum recording JSON (README "Recorded Executions"): `{sedum_version, packages:{go:{extensions:[".go"]}}, records:[{record_id, gen, files:[{path, package:"go", template, captures}], phases:[{name:"default", invocations:[{action, kwargs}]}]}]}`. A real example is `~/sedum/.sedum/recordings/prov-2026-1a55bc63.json`. Sedum is 0.12.2 and on PATH; the `go` generator is `~/sedum/generators/go`.
- Go actions (every kwarg required, `file` first): `addImport{file,path}`, `setType/setVar/setConst/setFunction{file,name,code}`, `setMethod{file,receiver,name,code}`, `setImports`, `setBuildConstraint`. `code` is the WHOLE declaration, body included. File templates: `{name}.go` (root, `package main` via `cmd/{bin}/{name}.go`), `{pkg}/{name}.go`, and so on. Anchors are header, imports, decls.
- `render.Render(ctx, render.Exec(bin), dir, recording)` runs `grow --execute`. `render.ReadMarkers(root)` reads sedum markers (Owner = record id, version 0 for v2 markers, gen_hash).
- Convergence regions are `graft:synth:begin/end` markers (internal/converge/region.go): one region per (record, file), created at EOF by converge if absent. `guardSedum` refuses any synth region overlapping ANY sedum region, and refuses changes to sedum regions.
- pr12 (`graft run`): one record per spec section → one file (`running.go`, `registration.go`, …), one synth region per record. Today it asks the model for the whole file. `examples/freeq-lite/conventions.md` on pr12 gives the architecture: `package main`; `running.go` owns `Server`, `Client`, `Message`, `handlers` map, `Send`/`Reply`/`Prefix`/`Disconnect`, `main`; other files register handlers from `init()`.
- `.github/workflows/ci.yml` does NOT install sedum. Test 2 needs a sedum install step in CI (`~/sedum/install.sh` exists; check it).
- records carry only natural-language statements, so Plan needs a separate typed skeleton input (file, imports, types, func signatures). Proposal: a `skeleton.json` beside conventions.md.

## Open design problem (blocking; I asked chad-bot-phoenix, which declined: my freeq tier is 'observe', so `ask` is refused)
A sedum `setFunction` region contains the whole func including body, and converge forbids a synth region overlapping a sedum region. So a body cannot be a synth region inside a sedum-rendered func. Options:
- (A) Sedum renders types/vars/imports plus owned stub funcs. Converge's region declares `<fn>Body` helpers that the stubs delegate to.
- (B) Converge is allowed to replace a sedum `seeded`-tier stub body. This changes converge, which is chad-bot's code.
- (C) Skeleton is declarations only, with no funcs. Package `main` then lacks `main()`, so the build needs a hand-added one. This probably fails test 6.
- Leaning toward (A) or Plan returning `{Recording, Regions []ConvergenceRegion}`, with Regions listing func signatures that converge will fill. Needs an orchestrator or chad-bot decision before coding; use `say` in #phoenix (a plain `say` worked).

## Next
1. Get the design call above. 2. Write the failing tests first. 3. Implement `internal/recording` (Plan, Write, golden freeq-lite skeleton). 4. Add a sedum install step to CI. 5. Run the checks, push, open the PR to chad/graft main, report in #phoenix and complete handoff 01M7PDF8ET1Y9ZSJYX0FR7ZBJH.

## Gotchas
- `freeq ask` to chad-bot is refused (tier); use `say`.
- The GitHub MCP server failed to connect; use `gh` for the PR.
- Never post absolute paths or secrets on freeq.
