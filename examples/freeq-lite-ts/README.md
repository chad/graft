# freeq-lite-ts: the same yardstick, in TypeScript

freeq-lite generated as a **TypeScript** program (Node 24, run with
`node --experimental-strip-types`, no build step). The spec, the visible smoke
suite and the held-out oracle are the ones in `../freeq-lite`, unchanged. Only
the language profile, the conventions, the skeleton and the gates differ.

| file | what |
|---|---|
| `graft.json` | the `language` profile (TypeScript), the gates and the model |
| `conventions.md` | the architecture: `running.ts` owns the core and `handlers`; every other module exports `init()` that registers its commands |
| `skeleton.json` | the `--render` layout: classes, the registry, `main`, and the stubs (`generator: typescript`) |
| `gates/` | `check.mjs` (every module parses and imports under strip-types), `registry.mjs` (only `running.ts` reads `handlers`), `unused.mjs` (no dead functions) |
| `case.json` | the bench case: shared spec and oracle, run as `node --experimental-strip-types main.ts -addr …` |

The gates read the code with the TypeScript compiler API, so install it once:
`(cd examples/freeq-lite-ts/gates && npm ci)`. Nothing is type-checked: Node
erases the types, so the gates check syntax, imports and structure, and the
smoke suite and oracle check behaviour. (`tsc --noEmit` would need
`@types/node` on the generation machine; it is a fair extra gate.)

`main.ts` and `package.json` (`{"type":"module"}`) are written into a fresh
tree by the profile's `bootstrap`; they are not generated.

## Plain and `--render`

```sh
# plain: the model writes each module
graft run --config examples/freeq-lite-ts/graft.json \
  --spec examples/freeq-lite/spec/freeq-lite.md --repo R --root A --accept
# --render: sedum lays out the skeleton, the model writes only the helpers
graft run --render --config examples/freeq-lite-ts/graft.json \
  --spec examples/freeq-lite/spec/freeq-lite.md --repo R --root A --accept
go run ./examples/freeq-lite/oracle -cmd '["node","--experimental-strip-types","--no-warnings","main.ts"]' -dir A
```

`--render` needs sedum with `sedum convergence` (the `typescript` generator
package is in sedum's `generators/`; set `SEDUM_GENERATORS`). In TypeScript a
helper in another module cannot be a method of its class, so the skeleton
declares methods as `Client_send(c, line)`-style functions that take the
object first, and each stub file imports its helpers module as a namespace
(`import * as running_synth from "./running_synth.ts"`).

## One run each (sonnet, n=1, an anecdote)

Both modes were run on freeq-lite with `claude -p --model sonnet` (no tools),
cost measured with `../freeq-lite/cost/claudecost.sh`, then scored by the
held-out oracle through `-cmd`:

| mode | regions | model calls | wall | cost | oracle |
|---|---|---|---|---|---|
| plain | 7/7 on the first attempt | 7 | 99 s | $1.03 | **47/47** |
| `--render` | 7/7 on the first attempt | 7 | 93 s | $1.31 | **47/47** |

The generated trees are in `runs/sonnet-n1/{plain,render}/tree` with
`oracle.json` and graft's log. The smoke overlap described in
`../freeq-lite/README.md` applies.

This is the last of several runs while the gates and conventions were
settled, and the earlier ones are part of the honest picture:

- A first `--render` run failed on the Running region because `unused.mjs`
  flagged running.ts's exported helpers, which only later modules call; the
  gate now treats running.ts's exports as the core's API.
- A `--render` run after that converged 6/7 and scored 45/47: the Everything
  else region could not fix the `421` line that Running had written with a
  stray colon (`:WIBBLE`), and its attempts wrote workarounds the registry
  gate rejected. `conventions.md` now states the format of that line.
- An earlier `--render` also failed every Running attempt because a seeded
  `init()` throws at startup, before the other regions exist. The bootstrap
  `main.ts` now lets a seeded placeholder throw out of `init()` and continues
  (any other error is rethrown).
- The plain run recorded here converged 7/7 and scored 47/47. No count is kept of any other plain runs made while the gates were settled, so this is one run, not a rate.

The oracle is no longer strictly held out for this example. It scored a 45/47
`--render` run, and `conventions.md` was then changed to state the `421` line
format it had missed, so the final runs above were made with the oracle's
findings in view. Treat 47/47 as "the pipeline can reach it", not as a blind
test.

n=1 per mode is a demonstration that the pipeline and `--render` work for a
second language, not a measurement of either mode's reliability.

**Safety.** The gates *import* generated code (`gates/check.mjs`) and the smoke
suite and oracle *run* it, unsandboxed, as the user running graft. Use a
disposable machine or container if that matters to you.
