# graft — specification (v0)

graft regenerates software from versioned intent. It combines three existing
designs: **Provenance** (versioned intent records), **Sedum** (deterministic
rendering from a closed action catalog) and **Phoenix** (prose canonicalization,
selective invalidation, evals and risk-tiered evidence).

Background and evidence: the joint findings doc from #phoenix (Phoenix ×
Sedum + Provenance). This spec is its §6 made normative. Each section is
changed by PR, reviewed by the other implementer.

Words in **bold MUST** are requirements; everything else is rationale.

## 1. Principle

Intent is the durable asset; code is derived. **Everything derived pins the
version of what it came from**, and is stale exactly when that pin differs
from the current version.

## 2. Layers

| Layer | Owns | Guarantee | Implementation |
|---|---|---|---|
| Records | source of truth, stable ids, versions, pins, publishing | guaranteed | **imports** `github.com/livecodelife/provenance/record` |
| Rendering | closed catalog, deterministic templates, owned regions | guaranteed | **invokes** the `sedum` binary; never links against it |
| Convergence | open synthesis in declared regions + execution gate | *merely checked* | graft |

- Sedum is invoked, not imported: its packages are `internal/`, and its
  TOOL_BOUNDARIES says nothing links against it. graft's contract with sedum
  is its file formats and CLI: recordings, `grow --execute`, `grow --check`,
  markers, and `conformance/`.
- **A layer MUST NOT claim a stronger guarantee than it has.** Convergence
  output is never reported as "guaranteed", and does not live inside sedum.

## 3. Records

- **Identity is a stable id, assigned once at acceptance and stored. It is
  never recomputed from content.** (Phoenix's content-derived ids churned on
  rewording; its `canon_anchor` collides.)
- Each version has a content hash and an immutable snapshot
  `{version, content, reason, author, created_at, parent_hash}`.
- **`gen_hash` covers only the fields that drive generation.** A status flip
  is not a content change. Specs are excluded: they verify output, they do not
  drive it. **In render mode the skeleton is record content (M11a):** each
  skeleton file entry (path, imports, decls, stubs with their doc contracts) is
  the `shape` of the imprint that owns the file, and provenance's `gen_hash`
  covers it when present. A record without a shape hashes exactly as before.
  `--skeleton file.json` is an import: graft writes each entry into its
  imprint's shape as a new record version (only when it differs) and plans from
  the records. **`skeleton.json` stays the source of truth for now:** a shape
  revised in the records alone is overwritten by the next `--render` run that
  passes a skeleton. Planning from the records alone is a follow-up (M11b).
- Tiers: brief → blueprint → imprint. **Only imprints own code regions.**

## 4. Rendering and regions

- Every region carries an ownership marker (owning record id, record version,
  `gen_hash`) and a body hash.
- **A hand edit inside an owned region is detected, and a re-render over it is
  refused and fails the run.** (Sedum's behaviour; Phoenix's
  overwrite-then-report is rejected.)
- `seeded` regions are stubs a human finishes; **graft never rewrites them.**
- Regions a record no longer selects are pruned.
- **A region depends on the whole skeleton**, not just its owner's shape: the
  shared types it uses (`Client` in `running.go`) live in another record's
  file. So in render mode every region's evidence also pins a **skeleton hash**
  (§7). sedum (>= 0.14.0) reads the shape too, so its own markers carry the same
  `gen_hash` as graft's: there is one definition.

## 5. Escalation ladder (when no catalog action fits)

`_default` variant → declared `literal` kwarg → targeted re-prompt →
larger model → hand the record to a human. **Synthesis outside the catalog
happens only in a convergence-layer region, owned and marked like any other.**

## 6. Convergence

- A synthesis region is declared, owned and marked exactly like a rendered
  region, but is owned by the convergence layer.
- **A synthesis region is accepted only when its execution gate passes:**
  evals derived from the owning record, run against the built artifact.
- Evals carry a risk tier; the required evidence per tier is policy.
- **A moved skeleton hash re-verifies, it does not regenerate.** When only the
  skeleton hash moved, the existing body is run through the current gates with
  no model call and fresh evidence pins the new skeleton hash; only a body that
  fails is regenerated. A region whose own `gen_hash` moved (its owner's shape
  or intent changed) is regenerated as before.

### `graft run` exit codes

| code | meaning |
|---|---|
| 0 | every region applied; the summary line says how the changed statements were cross-referenced |
| 1 | a region did not converge within its attempts (later regions not tried) |
| 2 | refused before generating and before any record is written: usage, config, drafts needing a human decision, `--accept` not given, **or changed statements with no valid cross-reference answer** (§8) |
| 3 | failed during generation (model command, gate or I/O error) |

The exit 2 for un-cross-referenced statements is new in M14 (0.2.0): after a spec
change `graft run --accept` needs `--crossref crossref.json` (from `graft
crossref`), or `--no-crossref`, which accepts unchecked and says so on stdout.
The first accept has no other record to constrain and needs neither.

### `graft converge` exit codes

| code | meaning |
|---|---|
| 0 | applied (guarantee: checked) |
| 1 | checked, not applied: a gate failed or the tier is unsatisfied |
| 2 | refused before checking began (usage, config, record, tier, pin, path, hand edit, sedum overlap, marker in body) |
| 3 | failed after checking began (a gate could not start, cancelled, tree or record changed); evidence so far is kept |

### `graft signoff` exit codes

| code | meaning |
|---|---|
| 0 | sign-off recorded |
| 1 | declined at the prompt; nothing written |
| 2 | refused before writing (usage, config, record, path, no region, hand edit, region behind the record, no confirmation) |
| 3 | failed after the prompt: the record, skeleton or region changed, or the log could not be written |

### `graft draft --skeleton` exit codes

A model proposes the skeleton (each file's fixed decls and stubs); it never
applies it. The proposal is the skeleton JSON `--render` imports, written to
`--out` (and `--out.coverage.json`); acceptance is `graft run --render
--skeleton proposal.json --accept`, the existing import. Before it is written
it passes a strict parse, the language profile's `decl_check` (drafted decls are
types, consts and vars only; a drafted decl with logic is rejected, and
function and method kinds are rejected without any build), and a sedum render
into a scratch tree that the profile's build gate builds with placeholder
bodies. An invalid proposal goes back to the model with the checker's and the
build's output, `--retries` times (default 2, max 5). The coverage report is the
model's claim of which stub owns each statement; a statement nothing owns is
flagged UNOWNED (`--strict` makes that a retry, dangling claims included). A section is placed at its record's `affected_scope` when `--records DIR` names a repo where it has a record (a scope that is not exactly one file is refused), else at the file `accept` would derive; `"files"` in the config is refused. `--retries 0` means one call.

| code | meaning |
|---|---|
| 0 | proposal written; every statement is claimed by some stub or decl |
| 1 | no valid proposal within the retries; nothing written |
| 2 | refused before asking the model (usage, config, profile without `decl_check`, `--out` exists) |
| 3 | the model command, sedum, I/O or a tool (the decl checker or the build gate could not run) failed; never retried |
| 4 | proposal written, but some statements are UNOWNED or carry a DANGLING claim (a claim naming a stub or decl not in the proposal) |

### `graft crossref` exit codes

The cross-reference step (§8). It writes only `--out` and touches no record and
no tree.

| code | meaning |
|---|---|
| 0 | proposal written; every changed statement answered and every answer valid (no changed statement: no model call, an empty proposal) |
| 1 | no parseable proposal within the retries; nothing written |
| 2 | refused before asking the model (usage, config, drafts needing a human decision, `--out` exists) |
| 3 | the model command or I/O failed |
| 4 | proposal written, but some statements are UNANSWERED or carry a DANGLING claim (`--strict` makes that a retry). Advisory warnings never change the code |

## 7. Evidence

- One evidence format for all layers.
- **Evidence pins what it read: `gen_hash`, the region hash, and the hash of
  the specs it ran (`spec_hash`, if any). It is stale when any of those moves.**
  The record version is recorded but not compared: a version bump that changes
  none of them (a status flip, a reworded reason) is no reason to re-verify.
  This matches §3, where `records.Stale` compares `gen_hash` only.
- **In render mode evidence also pins `skeleton_hash`** (M11a): the hash of the
  generator name and every file entry of every planned imprint's shape, sorted
  by path (`records.SkeletonHash`; content only, never record ids or versions).
  Evidence is stale on a moved `skeleton_hash` when the current pin has one, so
  evidence from before M11a (no skeleton hash) is stale in render mode and is
  re-verified (§6), never trusted. Plain runs have none and are unaffected; the
  field is `omitempty`, so their evidence ids do not change.
- `spec_hash` is computed by the records layer (`records.Pin.SpecHash`) from
  provenance's per-spec hashes for the record's associated specs; evidence
  treats it as opaque. A record with no specs has an empty spec hash; a spec
  that cannot be read is an error (`ErrSpecMissing`), never "no specs".
- The log is append-only and hash-chained, with a sidecar head pinning its
  committed prefix. The head is a lower bound: entries written after it that
  chain correctly (a crash before the head moved) are accepted and the head is
  rolled forward; a trailing partial line is ignored and truncated by the next
  writer. Writers hold an exclusive file lock, readers a shared one. Policy is
  never evaluated on an unverified log.
- **What this detects: accidental damage** — an edited, reordered, or removed
  committed entry, a head that does not match, a torn write. **What it does
  not detect: a deliberate rewrite by anyone with write access** — restoring an
  older log+head pair, deleting both, or editing and rehashing (the hashes are
  unkeyed). Tamper-evidence requires anchoring the head outside the log
  (a signed head, or the head committed to the record store); that is an open
  question (§12), and graft MUST NOT describe the log as tamper-proof until then.
- **Human sign-off** (`graft signoff`, M12) records `human_signoff` evidence
  through the same append-only log. It shows the human the region body and what
  it pins, then pins the record's `gen_hash` and version, the region's body hash
  (the bytes on disk, never a caller-supplied value), the spec hash and, in
  render mode, the skeleton hash. There is ONE skeleton-hash function,
  `pipeline.SkeletonHashOf`: `graft run --render` calls it over the records
  its spec resolves to, and `graft signoff` calls it over the same set
  (`pipeline.SpecRecordIDs`, from the spec `graft run` was given, `--spec`),
  not over every record in the index, so a removed section's record does not
  change it. Render mode is detected (graft.json names a skeleton, or a record
  carries a shape), never chosen by a flag. A sign-off goes stale on the same
  moves as any evidence. It refuses a missing or hand-edited region, a region
  written for a `gen_hash` other than the record's current one, and a record,
  skeleton or region that changed while the human was being asked. It does not
  stand in for the gates: a tier still needs its executable kinds from a run.
  **`--by` is caller-supplied and NOT authenticated**; the entry says who typed
  a name, not who they are (a signature over the entry is open, §12).
  `graft converge` does not compare `skeleton_hash` (it has no skeleton); a
  render tree's regions are converged by `graft run --render`.
- The evidence log requires a unix platform (flock).
- An unknown risk tier is an error, never "requires nothing".

## 8. Drafters: propose, never apply

Two directions, one rule:
- code → records (Provenance's `discover`),
- intent prose → records (Phoenix's canonicalizer).

- **A draft is shown only with evidence that it validates** (a re-extraction
  receipt or a PASS from a reviewer); otherwise it says "no confident
  proposal".
- **A drafter never mints an id silently.** On re-run it matches against the
  stored `derived_from` links of accepted records (anchor only as a candidate
  key) and emits *revisions*; anything it cannot match confidently becomes a
  "new or split?" question for a human.

### Cross-references (M14)

A statement in one section can bind code another section's record owns without
naming it (a frozen-account rule stated in Freezing that Deposits and Transfers
must enforce). Regenerating only the records whose own statements changed would
silently drop it, so the step below sits between the drafter diff and accept.

- **What is asked.** The statements that are new or changed since the last
  accept are found without a model (clause hash not in the record's
  `derived_from`; every statement of a new section). ONE model call per spec
  change decides, for each, `local` or `constrains` named records. It sees the
  changed statements, every record's title, intent, constraints, file and
  imports, and the conventions (`--with-code` adds the candidate files'
  current source; off by default). With nothing changed it makes no call.
- **Propose, never apply.** `graft crossref` writes `crossref.json`. Each answer
  is a receipt: the statement quoted from the spec, the target record (an id,
  or a section heading for a section that is new in the same change), a reason,
  the code location (`file: Identifier`, both parts required, no `..` segment)
  that must change, and a quote from the target's record (its OWN statements,
  never text imported into it). Receipts are validated for FORM: the quoted text
  is re-derived from the spec, the target must exist and not be the statement's
  own record, the location must be the target's file (with `--root`, the
  identifier must occur in it, at accept too), and the quote must occur in the
  target's record and may not be empty. A target that is a section new in the
  same change has no record text yet, so its quote cannot be checked; its
  location is checked against the file accept will place for it (the `files`
  config included). In render mode the identifier must be a stub or decl of the
  target's file in the skeleton, else "needs a skeleton change". Answers belong
  to a statement by (section, clause hash): the same sentence in two sections is
  two statements. An answer that fails is UNANSWERED or DANGLING and is never
  accepted.
- **Accept copies.** `graft run --accept --crossref crossref.json` copies each
  accepted statement into the target's draft, revising that record (new version,
  reason "spec revised (cross-reference imports changed)") so its region
  regenerates. The link to the source (source record id, clause hash) lives in graft's
  index (`.graft/accepted.json`, `imports`), never in `derived_from`, so it
  cannot change how a re-run maps a section onto a record, and provenance needs
  no change. An import follows its source: while the source clause stands the
  copy stays with no model call; when the source is edited or removed the copy
  is dropped, the owner is revised, and an edited statement is a new changed
  statement that is asked about again. One live copy, never two. Renaming the
  source section's heading keeps its imports (the link is the record id). An
  import dropped for any reason but its source going makes the sentence a
  changed statement again: it is asked, never lost.
- **Order in render mode.** One change step runs: the cross-reference call, then
  accept (which writes the imports into the records), then the skeleton import
  and render, then the regions. The cross-reference is therefore BEFORE the
  skeleton import; the reverse is equivalent, since no region is generated
  between the two record writes, so the order is not observable in the output.
  No region is ever generated from a stale record.
- **No silent success.** `graft run --accept` refuses (exit 2, before any record
  is written) while a new or changed statement has no valid answer, unless
  `--no-crossref` is given, which prints how many statements were accepted
  unchecked. Every run prints each import, each `local` verdict, each dropped
  import and a summary line (`cross-references: M accepted into K record(s), J
  judged local, U unchecked, D dropped`).
- **A model-free advisory.** `graft crossref` also prints, for a `local` verdict
  whose statement names a command, status code or identifier another record's
  text uses, a warning naming that record. It never changes an answer or an exit
  code, and token matching alone is not enough (the #14 probe never names
  `JOIN`).
- **Known limits.** (1) A well-formed but WRONG `local` verdict is accepted and
  the rule is dropped silently: validation checks form, not truth. Every `local`
  verdict is printed so a human can check it; behavioural evals (§6) are what
  catch it otherwise. (2) A statement that contradicts the target's own
  statements is not detected: both stay in the record. (3) A shape the new rule
  needs (a new field, a new stub) is a skeleton change, proposed separately by
  `graft draft --skeleton`.

## 9. Catalog-gap loop

- The literal fraction per generator package is a first-class metric.
- A synthesis region that keeps converging to the same shape becomes a
  **proposed** catalog action (propose-never-apply, pointed at the package).

## 10. Measurement

- One bench, three arms: graft / baseline (same intent, same model, one call)
  / intent-only. Every rate with a 95% Wilson interval; overlapping intervals
  are not a verdict; excluded runs stay visible and are counted in nothing.
- Results split works / disagreed / broke, and failures are attributed to a
  layer: selection, rendering, or convergence.

## 11. Milestones

| | Milestone | Owner |
|---|---|---|
| M0 | Skeleton: module, CLI stub, CI | chad-bot-phoenix |
| M1 | Records: import `provenance/record`; pins + staleness | livecodelife's agent |
| M2 | Rendering: write recordings → `sedum grow --execute` → read markers; `grow --check` | livecodelife's agent |
| M3 | Evidence: one format, three-hash pin, staleness | chad-bot-phoenix |
| M4 | Prose drafter: canonicalizer port → draft records with receipts; identity mapping | chad-bot-phoenix |
| M5 | Convergence: synthesis regions + execution gate + risk-tiered evals | chad-bot-phoenix |
| M6 | Bench: three arms, Wilson intervals, per-layer attribution | both |
| M11a | The skeleton as record content: shape in the owning imprint (provenance `gen_hash`), skeleton hash in evidence, re-verify on `skeleton_hash` | livecodelife's agent |
| M11b | `graft draft --skeleton`: a model proposes a shape, validated (logic-free decls, render, build) with a coverage report; a human accepts through the import | livecodelife's agent |
| M12 | `graft signoff`: a human sign-off as evidence, pinned to gen_hash, body, spec and skeleton hash | livecodelife's agent |
| M14 | `graft crossref`: a propose-never-apply cross-reference step; `graft run --accept` refuses a spec change whose changed statements were not cross-referenced | livecodelife's agent |

## 12. Open questions

- Can a recurring synthesis shape be promoted to a catalog action
  automatically, or must a human author it?
- Which record tiers may the prose drafter emit without a human?
- Does A/B/C/D change classification survive as a *pre-regen blast-radius
  estimate* once `gen_hash` gives exact staleness?
- One log or two: a journal of operations over provenance's content snapshots?
- Where a record's risk tier lives: provenance records have none, so
  `graft converge --tier` is caller-supplied today.
- Cross-region requirements: built in M14 (§8, "Cross-references"). Still open:
  a wrong `local` verdict is a silent drop, which only per-record behavioural
  evals (§6) catch; a statement that contradicts its target's own statements is
  not detected; and whether a drafter could take over the question (the
  step asks a model per spec change) is untried. The freeq-lite probe (#14) is a
  test with a scripted model, not a measurement: how often a real model answers
  it correctly is unmeasured (the bench harness's graft arms run the step in
  evolve cases so a real run can measure it).
- Authenticating `graft signoff --by` (a signature over the entry, for example
  by a freeq DID key, as an optional field that leaves the pin unchanged).
- Anchoring the evidence head outside the log (signed by the owner's DID, or
  committed alongside the records) to make deliberate rollback detectable.
