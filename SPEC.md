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
  (§7). sedum's own markers keep carrying the `gen_hash` without the shape until
  sedum's provenance reads it (`records.Pin.SedumGen`).

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

### `graft converge` exit codes

| code | meaning |
|---|---|
| 0 | applied (guarantee: checked) |
| 1 | checked, not applied: a gate failed or the tier is unsatisfied |
| 2 | refused before checking began (usage, config, record, tier, pin, path, hand edit, sedum overlap, marker in body) |
| 3 | failed after checking began (a gate could not start, cancelled, tree or record changed); evidence so far is kept |

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

## 12. Open questions

- Can a recurring synthesis shape be promoted to a catalog action
  automatically, or must a human author it?
- Which record tiers may the prose drafter emit without a human?
- Does A/B/C/D change classification survive as a *pre-regen blast-radius
  estimate* once `gen_hash` gives exact staleness?
- One log or two: a journal of operations over provenance's content snapshots?
- Where a record's risk tier lives: provenance records have none, so
  `graft converge --tier` is caller-supplied today.
- Cross-region requirements: a new statement in one section can concern a
  command another section owns. The drafter should flag it so the owning
  record is revised, rather than letting the change be dropped or absorbed
  into the new region (freeq-lite probe, #14). Token matching is not enough:
  the probe statement ("when a user joins…") never names `JOIN`, while
  Registration's 451 rule names four commands it rightly constrains. Candidate
  design: a propose-never-apply cross-reference step that asks, per new or
  revised statement, which existing record's behaviour it constrains; it
  returns an answer with a receipt, and accepting it copies the statement into
  the owner's draft, which revises that record and regenerates its region.
  **Today a dropped rule still reports success** ("8/8 applied", exit 0).
  freeq-lite's dead-function gate catches the form seen in the probe (a helper
  written for another file to call, never called), but a rule dropped with no
  code at all still passes every gate.
- Anchoring the evidence head outside the log (signed by the owner's DID, or
  committed alongside the records) to make deliberate rollback detectable.
