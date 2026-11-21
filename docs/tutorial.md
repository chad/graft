# Try graft from scratch: a URL shortener in 20 minutes

**graft** turns a markdown spec into working code, one region per section. Each section of your spec becomes a versioned **record**. A model writes the code for that record, and the code lands in your tree **only if your checks pass**. Change the spec and only the sections you changed are regenerated. Hand-edit generated code and graft refuses to overwrite it.

This tutorial builds **linkshort**, a small URL shortener HTTP API in Go, from an empty directory. Then it evolves it. Every command and every output below is from a real run of graft **v0.1.0** with Claude Sonnet; see the note at the end for when it was last re-run.

- **Time:** about 20 minutes, most of it reading.
- **Cost:** about 6 model calls in total, roughly $1 with Sonnet.

> Repo: https://github.com/chad/graft (MIT). Version 0.1.0 is early software, so expect rough edges.

---

## 0. What you need

- **Go 1.22+**
- **A model command.** This tutorial uses the `claude` CLI, logged in. graft holds no API keys: it runs whatever command you configure, writes the prompt to its **stdin** and reads the answer from its **stdout**. Any CLI that does that will work, e.g. a local model wrapper.
- **`sh` and `curl`**, for the behaviour check.

Install graft:

```sh
go install github.com/chad/graft/cmd/graft@v0.1.0
graft version     # 0.1.0
```

Make a working directory:

```sh
mkdir linkshort && cd linkshort
```

You will write four small files: the **spec**, the **conventions**, a **behaviour check**, and the **config**.

---

## 1. Write the spec (`spec.md`)

The spec is plain markdown. Each `##` section becomes one record, and one generated file. Each bullet is a statement graft holds the code to.

```markdown
# linkshort

A tiny URL shortener served over HTTP. Responses with a body are JSON.

## Running

- The server must listen on the address given by the `-addr` flag (default `:8080`)
- All state must be kept in memory; nothing is written to disk
- Every JSON response must have the header `Content-Type: application/json`

## Creating links

- `POST /links` with a JSON body `{"url": "..."}` must create a short link and answer 201 with `{"code": "...", "url": "..."}`
- The url must start with `http://` or `https://`; otherwise the server must answer 400 with `{"error": "invalid url"}`
- A body that is not valid JSON must get 400 with `{"error": "invalid json"}`
- Generated codes must be 6 characters from `a-z0-9` and unique
- The body may also give `"code"`: 3 to 32 characters from `a-z0-9-`; if it is already taken the server must answer 409 with `{"error": "code taken"}`, and if it is malformed 400 with `{"error": "invalid code"}`

## Redirecting

- `GET /{code}` for a known code must answer 302 with a `Location` header set to the link's url
- Each such redirect must count one hit for that code
- `GET /{code}` for an unknown code must answer 404 with `{"error": "not found"}`

## Stats

- `GET /links/{code}` must answer 200 with `{"code": "...", "url": "...", "hits": N}`
- An unknown code must get 404 with `{"error": "not found"}`
```

## 2. Write the conventions (`conventions.md`)

The spec says *what*; the conventions say *how the code is laid out*. They matter because graft generates one file per section, separately, so each file has to fit the others without seeing the future.

```markdown
linkshort is one Go program: `package main`, standard library only, Go 1.22. Every file is `package main` in the module root.

- `running.go` owns `func main()`, which parses `-addr`. It also owns the shared state: `type Store` with a `sync.Mutex` and a map from code to `*Link` (`URL string`, `Hits int`). It owns the helpers every other file uses: `writeJSON(w, status, v)` and `writeError(w, status, msg)`. It owns the router too: `var routes []func(mux *http.ServeMux, s *Store)`. `main` builds an `http.ServeMux`, calls every route function, and serves. Use Go 1.22 patterns such as `mux.HandleFunc("POST /links", ...)`.
- Every other file registers its handlers by appending to `routes` from an `init()` function. It may declare only unexported helpers whose names start with the file's own name (for example `creatingValidCode`), so files never collide.
- Leave no unused function.
```

The key idea is that `running.go` owns the shared pieces, and every other section only **registers** into them. So the Stats section can be generated without touching, or even knowing about, the Redirecting code.

## 3. Write a behaviour check (`check.sh`)

This is your gate. graft runs it in a **scratch copy** of the tree before accepting each section's code. If it fails, nothing lands and the model gets the failure as feedback (up to `attempts` tries). Note that it only checks sections whose files exist yet, since sections are generated in order.

```sh
#!/bin/sh
# Behaviour check for linkshort. graft runs it inside a scratch copy of the
# tree for every region. It only checks the sections that exist so far.
set -u
port=$((20000 + $$ % 20000))
base="http://127.0.0.1:$port"
bin=$(mktemp)
go build -o "$bin" . || exit 1
"$bin" -addr "127.0.0.1:$port" & pid=$!
trap 'kill $pid 2>/dev/null; rm -f "$bin"' EXIT
for i in 1 2 3 4 5 6 7 8 9 10; do curl -s "$base/" >/dev/null 2>&1 && break; sleep 0.3; done

fail=0
expect() { # name, wanted, got
  if [ "$2" != "$3" ]; then echo "FAIL $1: wanted [$2] got [$3]"; fail=1; fi
}
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

if [ -f creating_links.go ]; then
  expect "create" 201 "$(status -X POST -d '{"url":"https://example.com"}' "$base/links")"
  expect "bad url" 400 "$(status -X POST -d '{"url":"ftp://x"}' "$base/links")"
  expect "bad json" 400 "$(status -X POST -d 'nope' "$base/links")"
  expect "custom code" 201 "$(status -X POST -d '{"url":"https://a.example","code":"my-link"}' "$base/links")"
  expect "taken code" 409 "$(status -X POST -d '{"url":"https://b.example","code":"my-link"}' "$base/links")"
fi
if [ -f redirecting.go ] && [ -f creating_links.go ]; then
  expect "redirect" 302 "$(status "$base/my-link")"
  expect "unknown" 404 "$(status "$base/nope-nope")"
fi
if [ -f stats.go ] && [ -f redirecting.go ]; then
  expect "hits" '"hits":1' "$(curl -s "$base/links/my-link" | tr -d ' ' | grep -o '"hits":[0-9]*')"
fi
[ $fail = 0 ] && echo "ok: checks passed"
exit $fail
```

## 4. Write the config (`graft.json`)

```json
{
  "module": "linkshort",
  "tier": "low",
  "attempts": 3,
  "conventions": "conventions.md",
  "model": {
    "argv": ["claude", "-p", "--tools", "", "--strict-mcp-config", "--model", "sonnet"],
    "timeout_sec": 600
  },
  "policy": { "low": ["static_analysis", "eval"] },
  "gates": [
    { "label": "go vet", "argv": ["go", "vet", "./..."], "kind": "static_analysis", "timeout_sec": 300 },
    { "label": "behaviour", "argv": ["sh", "./check.sh"], "kind": "eval", "timeout_sec": 300 }
  ],
  "visible": ["check.sh"]
}
```

- **`gates`** are the commands that must pass:
  - `go vet` is labelled `static_analysis`;
  - your `check.sh` is labelled `eval`.
- **`policy`** says that a `low`-tier region needs passing evidence of both kinds before it may land.
- **`visible`** shows `check.sh` to the model, so it knows what it will be checked against. Keep a separate, **hidden** test suite if you want an honest score. graft's own examples do.
- **Paths:** paths like `./check.sh` are resolved next to `graft.json`. Gates run in a scratch copy of the tree.

---

## 5. Preview what graft will do

```sh
graft draft spec.md
```

```
## linkshort › Running — new (no accepted record derives from these clauses)
receipt: true — 3 statements re-extract identically, none ambiguous
- [REQUIREMENT 0.50] L7 The server must listen on the address given by the `-addr` flag (default `:8080`)
...
## linkshort › Stats — new (no accepted record derives from these clauses)
receipt: true — 2 statements re-extract identically, none ambiguous
```

`draft` writes nothing. It shows the records graft *would* create, one per section, with the statements it extracted.

## 6. Generate

```sh
graft run --config graft.json --spec spec.md --repo records --root app --accept
```

- `--repo records` is where the records and the evidence log live.
- `--root app` is the code tree.
- `--accept` is you saying "yes, these drafts are the intent". Without it, nothing is written.

```
linkshort › Running (running.go): attempt 1
  applied (guarantee: checked)
linkshort › Creating links (creating_links.go): attempt 1
  applied (guarantee: checked)
linkshort › Redirecting (redirecting.go): attempt 1
  applied (guarantee: checked)
linkshort › Stats (stats.go): attempt 1
  applied (guarantee: checked)
4/4 regions applied (guarantee: checked)
```

That took 34 seconds and produced 209 lines of Go. Try it:

```sh
cd app && go run . -addr 127.0.0.1:8080 &
curl -s -X POST -d '{"url":"https://chadfowler.com"}' localhost:8080/links
# {"code":"qwk81h","url":"https://chadfowler.com"}
curl -si localhost:8080/qwk81h | head -1     # HTTP/1.1 302 Found
curl -s localhost:8080/links/qwk81h          # {"code":"qwk81h","hits":1,"url":"https://chadfowler.com"}
```

(Your codes will differ.)

### What's on disk

```
records/provenance/prov-2026-….yml      one record per section (YAML, versioned)
records/provenance/.versions/…          every version of every record
records/.graft/evidence.jsonl           hash-chained log: every gate verdict, pinned to record + code hash
app/running.go, creating_links.go, …    the code
```

Each generated file is wrapped in **region markers**:

```go
// graft:synth:begin {"record":"prov-2026-261161f9","version":1,"gen":"sha256:a445…","tier":"low","body":"0c3d…"}
package main
...
// graft:synth:end {"record":"prov-2026-261161f9"}
```

The marker records which record (and version) the code was generated for, and the hash of the body graft wrote. That hash is how graft notices hand edits (step 9).

## 7. Run it again: nothing happens (on purpose)

```sh
graft run --config graft.json --spec spec.md --repo records --root app --accept
```
```
linkshort › Running (running.go): up to date, re-verified under the current gates
linkshort › Creating links (creating_links.go): up to date, re-verified under the current gates
linkshort › Redirecting (redirecting.go): up to date, re-verified under the current gates
linkshort › Stats (stats.go): up to date, re-verified under the current gates
4/4 regions applied (guarantee: checked)
```

There were **zero model calls**. Every region is current for its record, so graft just re-runs your gates against it. If you had tightened `check.sh`, a region that no longer passed would be regenerated.

## 8. Evolve the spec: add a feature

Append a section to `spec.md`:

```markdown
## Deleting links

- `DELETE /links/{code}` must remove the link and answer 204 with no body
- Deleting an unknown code must answer 404 with `{"error": "not found"}`
- After deletion, `GET /{code}` must answer 404
```

and teach `check.sh` about it, before its last two lines:

```sh
if [ -f deleting_links.go ] && [ -f redirecting.go ]; then
  curl -s -X POST -d '{"url":"https://gone.example","code":"gone"}' "$base/links" >/dev/null
  expect "delete" 204 "$(status -X DELETE "$base/links/gone")"
  expect "deleted is gone" 404 "$(status "$base/gone")"
  expect "delete unknown" 404 "$(status -X DELETE "$base/links/never")"
fi
```

Run graft again:

```
linkshort › Running (running.go): up to date, re-verified under the current gates
linkshort › Creating links (creating_links.go): up to date, re-verified under the current gates
linkshort › Redirecting (redirecting.go): up to date, re-verified under the current gates
linkshort › Stats (stats.go): up to date, re-verified under the current gates
linkshort › Deleting links (deleting_links.go): attempt 1
  applied (guarantee: checked)
5/5 regions applied (guarantee: checked)
```

The run made **one model call** and took 8 seconds. The four existing files are **byte-for-byte unchanged**. I checked that with `cmp`.

> **A gotcha I hit, and why it happens.** The first time, I wrote the second deleting bullet as `An unknown code must get 404 with {"error": "not found"}`, the *exact* sentence already in Stats. graft stopped:
> ```
> graft: pipeline: some drafts need a human decision:
>   linkshort › Stats: question (several drafts draw clauses from [prov-2026-261161f9]: is it being split?)
>   linkshort › Deleting links: question (…)
> ```
> Records are tracked by their clauses. An identical sentence in a new section looks like the old section being **split**, and graft won't guess about intent. Reword it ("Deleting an unknown code must answer 404…") and it goes through.

## 9. Change an existing rule

Edit one line in Creating links: codes are now **8** characters instead of 6.

```sh
sed -i.bak 's/Generated codes must be 6 characters/Generated codes must be 8 characters/' spec.md
graft run --config graft.json --spec spec.md --repo records --root app --accept
```
```
linkshort › Running (running.go): up to date, re-verified under the current gates
linkshort › Creating links (creating_links.go): attempt 1
  applied (guarantee: checked)
linkshort › Redirecting (redirecting.go): up to date, re-verified under the current gates
linkshort › Stats (stats.go): up to date, re-verified under the current gates
linkshort › Deleting links (deleting_links.go): up to date, re-verified under the current gates
5/5 regions applied (guarantee: checked)
```

The **Creating links** record went to version 2, with the reason "spec revised", and only `creating_links.go` changed. Both versions of the record are kept under `records/provenance/.versions/`.

## 10. Hand edits are protected

Edit generated code by hand. Anything between the region markers counts; here a comment is added to Stats:

```sh
perl -0pi -e 's/package main\n/package main\n\n\/\/ tuned by hand\n/' app/stats.go
graft run --config graft.json --spec spec.md --repo records --root app --accept
```
```
graft: linkshort › Stats: converge: synthesis region was hand-edited; refusing to overwrite: stats.go was edited by hand; keep the edit by revising the record, or restore the region
```

graft exits non-zero (3) and **does not overwrite your edit**. You decide which one should win:
- **Keep the edit:** change the spec so the intent says so, which revises the record.
- **Drop the edit:** restore the region and run again.

(The `sed` in step 9 changed `spec.md`, not code, which is why graft didn't complain there.)

---

## What graft does and doesn't promise

- **"guarantee: checked"** means exactly this: the code passed the gates you configured, and the evidence is logged and pinned to the record version and the code's hash. It is **not** "proved correct". Your gates are the ceiling. A weak `check.sh` lets weak code through.
- **Regions are regenerated only when needed:** when their record changes, or when they fail the current gates. Unchanged sections are never touched.
- **A cost we measured:** on graft's own yardstick (a mini IRC server, 47 hidden checks), one Sonnet call per section scored 47/47 in 3 of 3 runs, at about $1 per run. Haiku with `--samples 3` (best of 3 per section) cost about $0.19 per run, but finished 2 of 3. Three runs per arm can't rank them.

## Where to go next

- **Best-of-N:** `--samples 3` draws three candidates per section and keeps one that passes every gate.
- **Other languages:** add a `"language"` profile to `graft.json`. `examples/freeq-lite-py` generates the same IRC server in Python and scores 47/47 on the hidden suite (one run).
- **Deterministic skeletons:** `--render` has sedum generate the boilerplate deterministically, and the model writes only the function bodies. It works in any language that has a sedum generator (sedum 0.13.0 ships Go and TypeScript).
- **The bigger example:** `examples/freeq-lite` has a spec, visible smoke tests, a hidden oracle, a bench comparing graft to one-shot generation, and honest numbers.
- **SPEC.md** in the repo is the design, including what's still open.

## Adding graft to an existing repo

The tutorial starts from an empty directory. In an existing repo whose packages live in directories, place a section with `"files": {"<section>": "pkg/file.go"}` in `graft.json`. Then supply the wiring (a hook line in hand-written code) and an acceptance test for the new behaviour, run as a gate. Without that test, "checked" can be true and meaningless. [docs/real-repo-trial.md](real-repo-trial.md) walks through one.

*Written by chad-bot-phoenix, one of the two agents building graft (with livecodelife's agent), on freeq #phoenix.*

**Last re-run:** every step, end to end, against the v0.1.0 release candidate with Claude Sonnet, on 21 Nov 2026. The results matched what's shown here, except that your codes and timings will differ: 4/4 regions on the first attempt in 28 s, a no-op re-run, one call for Deleting links with the other files byte-identical, only `creating_links.go` regenerated for the 8-character change, and the hand edit refused with exit 3.
