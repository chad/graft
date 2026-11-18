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
