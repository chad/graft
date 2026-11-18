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
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("needs node")
	}
	if _, err := os.Stat("node_modules/typescript"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("the gates need `npm ci` in examples/freeq-lite-ts/gates")
		}
		t.Skip("run `npm ci` in examples/freeq-lite-ts/gates")
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
		"read":          imp + `export function init(): void { handlers.get("X"); }`,
		"alias":         `import { handlers as h } from "./running.ts";` + "\nexport function init(): void { h.set(\"X\", () => {}); }",
		"namespace":     `import * as r from "./running.ts";` + "\nexport function init(): void { r.handlers.set(\"X\", () => {}); }",
		"rebind":        imp + `const m = handlers; export function init(): void { m.set("X", () => {}); }`,
		"non-literal":   imp + `export function init(): void { const k = "X"; handlers.set(k, () => {}); }`,
		"bad name":      imp + `export function helper(): void {}` + "\nexport function init(): void { handlers.set(\"X\", () => {}); }",
		"class":         imp + `export class C {}`,
		"duplicate cmd": imp + `export function init(): void { handlers.set("JOIN", () => {}); }`,
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
		"dead":      `export function aDead(): void {}`,
		"recursion": `export function aRec(n: number): number { return n ? aRec(n - 1) : 0; }`,
		"string":    `export function aStr(): void {}` + "\nexport const s = \"aStr\";",
		"sink":      `export function aSink(): void {}` + "\nlet _: unknown;\n_ = aSink;",
		"void":      `export function aVoid(): void {}` + "\nvoid aVoid;",
		"nested":    `export function aOuter(): void { function inner(): void {} }` + "\nexport function init(): void { aOuter(); }",
	} {
		if out, pass := gate(t, "unused.mjs", map[string]string{"running.ts": running, "a.ts": src}); pass {
			t.Errorf("%s accepted: %s", name, out)
		}
	}
	if out, _ := gate(t, "unused.mjs", map[string]string{"running.ts": running, "a.ts": `export function aDead(): void {}`}); !strings.Contains(out, "aDead") {
		t.Errorf("the dead function is not named: %s", out)
	}
}
