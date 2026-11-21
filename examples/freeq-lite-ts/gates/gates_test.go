// Package gates tests the freeq-lite-ts gate scripts (they are JavaScript on
// the TypeScript compiler API; the test is Go so that `go test ./...` runs it).
// It needs node and the gates' own dependency: `npm ci` in this directory.
package gates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const running = `export type Message = { command: string; params: string[] };
export class Client { nick = ""; send(line: string): void { void line; } }
export const handlers: Map<string, (c: Client, m: Message) => void> = new Map();
export function main(argv: string[]): void {
  const c = new Client();
  c.send("x");
  for (const h of handlers.values()) h(c, { command: "X", params: argv });
}
`

func gate(t *testing.T, script string, files map[string]string) (string, bool) {
	t.Helper()
	// In CI (which installs Node 24) a missing prerequisite is a failure, so
	// these tests can never pass by skipping.
	skip, skipf := t.Skip, t.Skipf
	if os.Getenv("CI") != "" {
		skip, skipf = t.Fatal, t.Fatalf
	}
	node, err := exec.LookPath("node")
	if err != nil {
		skip("needs node")
	}
	if _, err := os.Stat("node_modules/typescript"); err != nil {
		// Install the gates' one dependency once, the way a user would.
		if out, err := exec.Command("npm", "ci", "--silent", "--no-audit", "--no-fund").CombinedOutput(); err != nil {
			skipf("run `npm ci` in examples/freeq-lite-ts/gates (%v: %s)", err, out)
		}
	}
	if err := exec.Command(node, "--experimental-strip-types", "--no-warnings", "-e", "0").Run(); err != nil {
		skip("needs a Node with --experimental-strip-types (22.6 or newer)")
	}
	dir := t.TempDir()
	for n, src := range files {
		os.WriteFile(filepath.Join(dir, n), []byte(src), 0o644)
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`), 0o644)
	self, _ := filepath.Abs(script)
	cmd := exec.Command(node, self)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func TestCheck(t *testing.T) {
	good := map[string]string{
		"running.ts": running,
		"channels.ts": `import { handlers } from "./running.ts";
export function init(): void { handlers.set("JOIN", () => {}); }
`,
	}
	if out, ok := gate(t, "check.mjs", good); !ok {
		t.Fatalf("good tree rejected: %s", out)
	}
	for name, files := range map[string]map[string]string{
		"syntax error":     {"running.ts": running, "a.ts": "export function f( {"},
		"missing .ts ext":  {"running.ts": running, "a.ts": `import { handlers } from "./running"; export const x = handlers;`},
		"unresolved name":  {"running.ts": running, "a.ts": `import { nope } from "./running.ts"; export const x = nope;`},
		"enum":             {"running.ts": running, "a.ts": `export enum E { A }`},
		"param property":   {"running.ts": running, "a.ts": `export class C { constructor(public x: number) {} }`},
		"throws at import": {"running.ts": running, "a.ts": `throw new Error("boom");`},
	} {
		if out, ok := gate(t, "check.mjs", files); ok {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
}

func TestRegistry(t *testing.T) {
	ok := map[string]string{"running.ts": running, "channels.ts": `import { handlers } from "./running.ts";
function channelsJoin(): void {}
export function init(): void { handlers.set("JOIN", channelsJoin); }
`, "channels_synth.ts": `export function channelsInitBody(): void {}`}
	if out, pass := gate(t, "registry.mjs", ok); !pass {
		t.Fatalf("good tree rejected: %s", out)
	}
	imp := `import { handlers } from "./running.ts";` + "\n"
	for name, src := range map[string]string{
		"read":                 imp + `export function init(): void { handlers.get("X"); }`,
		"alias":                `import { handlers as h } from "./running.ts";` + "\nexport function init(): void { h.set(\"X\", () => {}); }",
		"namespace":            `import * as r from "./running.ts";` + "\nexport function init(): void { r.handlers.set(\"X\", () => {}); }",
		"element access":       `import * as r from "./running.ts";` + "\nexport function init(): void { r[\"handlers\"].get(\"JOIN\"); }",
		"element access 2":     `import * as r from "./running.ts";` + "\nexport function init(): void { r['handlers']; }",
		"element access alone": "export function init(): void { void globalThis[\"handlers\"]; }",
		"namespace only":       `import * as r from "./running.ts";` + "\nexport function init(): void { void r; }",
		"rebind":               imp + `const m = handlers; export function init(): void { m.set("X", () => {}); }`,
		"non-literal":          imp + `export function init(): void { const k = "X"; handlers.set(k, () => {}); }`,
		"bad name":             imp + `export function helper(): void {}` + "\nexport function init(): void { handlers.set(\"X\", () => {}); }",
		"class":                imp + `export class C {}`,
		"duplicate cmd":        imp + `export function init(): void { handlers.set("JOIN", () => {}); }`,
	} {
		files := map[string]string{"running.ts": running, "a.ts": src}
		if name == "duplicate cmd" {
			files["b.ts"] = src
		}
		if out, pass := gate(t, "registry.mjs", files); pass {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
}

func TestUnused(t *testing.T) {
	ok := map[string]string{"running.ts": running, "a.ts": `export function aUsed(): number { return 1; }
export function init(): void { aUsed(); }
`}
	if out, pass := gate(t, "unused.mjs", ok); !pass {
		t.Fatalf("good tree rejected: %s", out)
	}
	for name, src := range map[string]string{
		"dead":              `export function aDead(): void {}`,
		"recursion":         `export function aRec(n: number): number { return n ? aRec(n - 1) : 0; }`,
		"string":            `export function aStr(): void {}` + "\nexport const s = \"aStr\";",
		"sink":              `export function aSink(): void {}` + "\nlet _: unknown;\n_ = aSink;",
		"void":              `export function aVoid(): void {}` + "\nvoid aVoid;",
		"nested":            `export function aOuter(): void { function inner(): void {} }` + "\nexport function init(): void { aOuter(); }",
		"object key":        `export function aKey(): void {}` + "\nexport const o = { aKey: 1 };",
		"class key":         `export function aField(): void {}` + "\nexport class C { aField = 1; }",
		"dead method":       `export class C { aMethod(): void {} }` + "\nexport function init(): void { new C(); }",
		"dead field fn":     `export class C { aArrow = (): void => {}; }` + "\nexport function init(): void { new C(); }",
		"destructuring key": `export function aFn(): void {}` + "\nconst src = { aFn: 1 };\nexport const { aFn: x } = src;",
		"dead object arrow": `export const o = { aArrow: (): void => {} };` + "\nexport function init(): void { void o; }",
		"top-level then":    `export function then(): void {}`,
	} {
		if out, pass := gate(t, "unused.mjs", map[string]string{"running.ts": running, "a.ts": src}); pass {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
	// Uses that do count: a method called on an instance, a shorthand property,
	// a class field function called, and runtime protocol methods.
	for name, src := range map[string]string{
		"method call": `export class C { aMethod(): void {} }` + "\nexport function init(): void { new C().aMethod(); }",
		"shorthand":   `function aFn(): void {}` + "\nexport const o = { aFn };\nexport function init(): void { o.aFn(); }",
		"field call":  `export class C { aArrow = (): void => {}; }` + "\nexport function init(): void { new C().aArrow(); }",
		"protocol":    `export class C { toString(): string { return ""; } constructor() {} }` + "\nexport function init(): void { String(new C()); }",
	} {
		if out, pass := gate(t, "unused.mjs", map[string]string{"running.ts": running, "a.ts": src}); !pass {
			t.Errorf("%s rejected: %s", name, out)
		}
	}
	// running.ts's exported functions are the core's API and are not dead; its private ones are.
	core := `export function Core_api(): void {}
` + running
	if out, pass := gate(t, "unused.mjs", map[string]string{"running.ts": core}); !pass {
		t.Errorf("an exported core function was rejected: %s", out)
	}
	if out, pass := gate(t, "unused.mjs", map[string]string{"running.ts": "function hidden(): void {}\n" + running}); pass {
		t.Errorf("a private dead function in running.ts was accepted: %s", out)
	}
	if out, _ := gate(t, "unused.mjs", map[string]string{"running.ts": running, "a.ts": `export function aDead(): void {}`}); !strings.Contains(out, "aDead") {
		t.Errorf("the dead function is not named: %s", out)
	}
}

// The recorded runs were generated before the final gates were settled; they
// must still pass them.
func TestRecordedTreesPass(t *testing.T) {
	for _, mode := range []string{"plain", "render"} {
		tree := filepath.Join("..", "runs", "sonnet-n1", mode, "tree")
		ents, err := os.ReadDir(tree)
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{}
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(tree, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = string(b)
		}
		for _, g := range []string{"check.mjs", "registry.mjs", "unused.mjs"} {
			if out, ok := gate(t, g, files); !ok {
				t.Errorf("%s tree fails %s: %s", mode, g, out)
			}
		}
	}
}
