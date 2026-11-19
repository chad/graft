// Shared by the gates: read every module of the tree (the current directory)
// with the TypeScript compiler API. Nothing is type-checked: the code runs
// under Node's strip-types, which erases types, so what matters is syntax and
// how modules use each other.
import fs from "node:fs";
import ts from "typescript";

// A module is src-less: *.ts in the tree root. main.ts is graft's bootstrap.
export function modules() {
  return fs
    .readdirSync(".")
    .filter((f) => f.endsWith(".ts") && f !== "main.ts")
    .sort()
    .map((file) => {
      const text = fs.readFileSync(file, "utf8");
      const sf = ts.createSourceFile(file, text, ts.ScriptTarget.ES2022, true, ts.ScriptKind.TS);
      // <name>_synth.ts holds the helpers of module <name>.
      const mod = file.replace(/\.ts$/, "").replace(/_synth$/, "");
      return { file, mod, synth: file.endsWith("_synth.ts"), sf };
    });
}

export function line(sf, node) {
  return sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
}

export { ts };

export function finish(problems, ok) {
  if (problems.length) {
    console.log(problems.join("\n"));
    process.exit(1);
  }
  console.log("ok: " + ok);
}
