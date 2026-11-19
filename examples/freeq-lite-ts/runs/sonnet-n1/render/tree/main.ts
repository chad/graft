// Entry point, written by graft and not generated.
//
// It imports running, then every other module in name order and calls its
// init() (which registers that module's commands), then starts the server:
//   node --experimental-strip-types main.ts -addr host:port
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const running = await import("./running.ts");
const names = fs
  .readdirSync(here)
  .filter((f) => f.endsWith(".ts") && !f.endsWith("_synth.ts") && f !== "main.ts" && f !== "running.ts")
  .sort();
for (const f of names) {
  const m = await import("./" + f);
  if (typeof m.init === "function") {
    try {
      m.init();
    } catch (e) {
      // Under --render a module whose region is not converged yet still holds
      // its placeholder; the server must start so the other regions can be
      // checked. A real error is never swallowed.
      if (!String(e).includes("graft: not converged")) throw e;
    }
  }
}
running.main(process.argv.slice(2));
