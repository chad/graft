// Gate: every module parses and imports cleanly (run in the tree's root).
//
// Importing under Node's strip-types catches what parsing does not: a missing
// ".ts" extension, an unresolved import, an enum, namespace or parameter
// property (which strip-types cannot erase), or an error thrown at load time.
// main.ts is not imported, because importing it would start the server.
import { spawnSync } from "node:child_process";
import { modules, finish, ts } from "./lib.mjs";

const problems = [];
const mods = modules();
for (const m of mods) {
  for (const d of m.sf.parseDiagnostics ?? []) {
    const { line } = m.sf.getLineAndCharacterOfPosition(d.start ?? 0);
    problems.push(`${m.file}:${line + 1}: ${ts.flattenDiagnosticMessageText(d.messageText, " ")}`);
  }
}
finish(problems, "");
const names = mods.map((m) => m.file).sort((a, b) => (a === "running.ts" ? -1 : b === "running.ts" ? 1 : a < b ? -1 : 1));
const code = names.map((n) => `await import(${JSON.stringify("./" + n)});`).join("\n");
const r = spawnSync("node", ["--experimental-strip-types", "--no-warnings", "--input-type=module", "-e", code], {
  encoding: "utf8",
  timeout: 60000,
});
if (r.status !== 0) {
  console.log("importing the modules failed:\n" + (r.stderr || r.error?.message || "").slice(-1500));
  process.exit(1);
}
console.log(`ok: ${mods.length} modules parse and import`);
