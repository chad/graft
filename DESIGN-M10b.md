# DESIGN-M10b: language-agnostic `--render`

## Problem
`internal/recording` hardcodes Go: go/parser (`checkDecl`), `package` clauses, `{name}.go` templates (`fileEntry`), the `<fn>Body` helper naming, the `panic("graft: not converged")` seed, `_synth.go` paths, gofmt via `format.Source`. `pipeline/render.go` and the prompts also assume `.go`.

## 1. The contract (declared by the generator package, in `sedum.yaml`)
A new optional `convergence:` block. Sedum core knows no language; it only renders text templates with the engine it already has (`{{x|transform}}`, `join:`).

```yaml
convergence:
  synth_suffix: "_synth"          # synth path = dir/<stem><suffix><ext>
  placeholder: "graft: not converged"   # marker string; graft matches on it
  helper: "{{stem}}{{name|pascal}}Body" # helper name for a stub
  stub: stub.tmpl                 # template: stub that delegates to the helper
  seed: seed.tmpl                 # template: whole synth file (package/imports/placeholder bodies)
  declare: declare.tmpl           # template: one helper signature (no body); graft shows these to the model
  param: "{{name}} {{type}}"      # per-param text, joined with ", "
  result: ...                     # how a results list renders (template over list)
```
Template inputs (all opaque strings, no parsing): `file`, `stem`, `dir`, `module/package`, `imports[]`, stub `{name, receiver_name, receiver_type, params[{name,type,spread}], results[], doc, helper}`.
Placeholder lives only in `seed.tmpl` and must contain the exact string `graft: not converged`; sedum load rejects a convergence block whose seed lacks it.

**Declarations.** skeleton.json stays the same shape (`kind`, `name`, `receiver`, `code`; stubs with params/results as opaque strings). graft stops parsing `code`: it forwards it to the generator's existing `set*` actions. The generator's `syntax:` check (sedum already validates files before writing) replaces `checkDecl`. Duplicate-name detection stays in graft, on (dir, receiver, name), which needs no parsing. Cost: Go loses graft's kind/name-vs-code cross-check; sedum's syntax check still catches broken code, but not a name mismatch. Mitigation: Go generator keeps its checker; a mismatch surfaces at `grow --check`.

**Commands, not templates, where formatting matters.** Seed/stub text must be formatter-clean. Go templates are authored gofmt-clean and `syntax: go` verifies; no formatter runs in graft. If a language needs a formatter, the generator's `syntax:` hook does it (TS: none needed, Node strip-types doesn't care).

## 2. Where each piece lives
- **sedum core (PR):** parse/validate `convergence:`; new read-only command `sedum convergence render --package <name> < input.json > output.json` that applies the templates and returns per-file `{synth_path, seed, stubs[{name, code, helper, declare}]}`, plus `convergence info` (suffix, placeholder, extensions). graft never reimplements templating.
- **generators/go:** a `convergence:` block + 3 template files reproducing today's output.
- **generators/typescript (new):** `sedum.yaml`, file templates, set* actions (setFunction/setClass/setType/setConst/setMethod/addImport), convergence templates, conformance tests in sedum's existing style.
- **graft:** `recording` calls `sedum convergence render` (via the `render.Sedum` interface) for stubs, seeds, synth paths, helper names; `fileEntry` takes extension and layout from the package (`convergence info` / package `extensions`) instead of `.go`; `IsSynthPath` uses the suffix; `Placeholder` comes from the generator. go/parser, go/format and Go literals leave recording.go. The M10a `language` profile supplies prompt fence, build and run; this is the same data graft already needs, so I rebase on it and do not add a second source. Package name for a record's recording comes from the profile's generator name (not hardcoded `"go"`).

## 3. Byte-identical Go
Before refactoring, add a golden test: Plan+render of examples/freeq-lite skeleton (and v2) writes recording.json and the synth seeds to testdata. The refactor must keep those bytes and the existing recording/pipeline tests unmodified. Go generator templates are diffed against the old string-building output in a sedum conformance test. Risk to flag: Plan is currently pure and offline (no sedum call); calling `sedum convergence render` makes it need the binary. Alternative: cache the contract as a static `convergence.json` read straight from the generators dir by graft (no process), keeping Plan pure but making graft implement templating. I recommend the sedum command, and Plan takes an injected `Contract` interface so tests use a fake.

## 4. TypeScript specifics
- Layout: ESM, one module per skeleton file `src/<stem>.ts`; synth `src/<stem>_synth.ts` (keeps the `_synth` suffix so `IsSynthPath` logic is uniform).
- Stub: `export function f(a: string): number { return fBody(a); }` with `import { fBody } from "./<stem>_synth.ts";` (explicit `.ts` extension, required by strip-types). Methods: `class C { m(x) { return mBody(this, x); } }` since a helper cannot be a method of the class from another module; helper takes the receiver as first argument. Seed: `export function <helper>(...): T { throw new Error("graft: not converged"); }`.
- Build/run: **`node --experimental-strip-types`** for run (Node 24 has it; no build step, no dist, no tsconfig dependency for the model). Gate with `tsc --noEmit` if `typescript` is installable (gate script checks; falls back to node's parse). Rationale: fewer moving parts and a faster loop; caveat: strip-types forbids enums/namespaces/parameter properties, which the conventions file states.
- Profile: `ext .ts, fence "ts", comment "//", synth_suffix "_synth", bootstrap package.json {"type":"module"}, run ["node","--experimental-strip-types","src/main.ts"]`.
- examples/freeq-lite-ts: same spec, TS skeleton, gates (tsc --noEmit, registry check), graft.json, held-out oracle via `-cmd`.

## Open questions for review
1. sedum command vs static `convergence.json` (see §3).
2. Methods as receiver-first helper functions in TS: acceptable?
3. Accept losing graft's name/kind-vs-code check for non-Go generators?
4. Confirm the shape of the M10a `language` profile before I depend on it.

## Orchestrator decisions (2026-11-18 ~21:15Z): APPROVED, implement as designed, with these answers
1. **sedum command**, not a static convergence.json. `--render` already requires sedum, so Plan losing offline purity costs nothing in practice. Plan takes an injected `Contract` interface, and tests use a fake. Golden test first (§3), before any refactor.
2. **TS methods as receiver-first helpers:** accepted. Say so in freeq-lite-ts conventions.md.
3. **Losing the kind/name-vs-code check for non-Go generators:** accepted, with one generic, parse-free replacement in graft. After rendering, every helper name the contract returned must appear in its synth seed and stub text. The Go generator keeps its syntax checker.
4. **M10a profile (chad-bot, landing first):**
   `{"name","ext","fence","comment","synth_suffix","preamble","bootstrap":{path:template},"build":[argv],"run":[argv]}`, with templating `{module}`, `{out}` and `{addr}`. An empty `build` means no build step. `-addr {addr}` is appended to `run` unless `{addr}` already appears in it. The oracle and smoke take `-cmd '<argv JSON>'`.
   - **The generator package comes from the skeleton** (agreed with chad-bot), defaulting to profile.name when the skeleton omits it.
   - **synth_suffix has two sources** (the profile and the generator's convergence block). Under `--render` the generator's value is authoritative, and graft errors at load if the profile disagrees. The orchestrator tells chad-bot.
   - TS profile: run `["node","--experimental-strip-types","src/main.ts"]`, build empty, bootstrap package.json `{"type":"module"}`.
5. **PR order:** the sedum PR first (contract + Go templates byte-identical + typescript generator + conformance). Then the graft PR, rebased on M10a once it merges. examples/freeq-lite-ts needs only the plain path working before the graft render switch lands, so build it early to surface profile issues.
6. Keep this file. It moves to the next worker. Don't commit it to main; `git rm` it before the PR is final.
