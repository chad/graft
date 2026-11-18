// Gate: registry ownership and naming (run in the tree's root).
//
// - Only running.ts (and running_synth.ts) may use `handlers` freely. Every
//   other module may only call handlers.set("CMD", fn) with a literal command,
//   on the name `handlers` imported by that name from "./running.ts": no alias,
//   no namespace access, no get/has/delete, no rebinding.
// - A command is registered by one module only.
// - Every other module declares top-level functions named `init` or starting
//   with the module's own name, and no classes, so modules never collide.
//   <name>_synth.ts counts as module <name>.
import { modules, line, finish, ts } from "./lib.mjs";

const problems = [];
const owner = new Map(); // COMMAND -> module
for (const m of modules()) {
  if (m.mod === "running") continue;
  const { sf, file } = m;
  const bad = (node, msg) => problems.push(`${file}:${line(sf, node)}: ${msg}`);
  const allowed = new Set(); // identifier nodes that are the receiver of a legal handlers.set(...)
  const visit = (node) => {
    if (ts.isFunctionDeclaration(node) && node.parent === sf && node.name) {
      const n = node.name.text;
      if (n !== "init" && !n.startsWith(m.mod)) bad(node, `function ${n} must be named init or start with ${m.mod}`);
    }
    if (ts.isClassDeclaration(node) || ts.isEnumDeclaration(node) || ts.isModuleDeclaration(node)) {
      bad(node, "only running.ts declares classes");
    }
    if (ts.isImportSpecifier(node) && (node.propertyName?.text === "handlers" || (node.name.text === "handlers" && node.propertyName))) {
      bad(node, "import handlers under its own name");
    }
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      ts.isIdentifier(node.expression.expression) &&
      node.expression.expression.text === "handlers"
    ) {
      const arg = node.arguments[0];
      if (node.expression.name.text !== "set" || !arg || !ts.isStringLiteral(arg)) {
        bad(node, 'register with a literal command: handlers.set("CMD", fn)');
      } else {
        allowed.add(node.expression.expression);
        const cmd = arg.text.toUpperCase();
        if (owner.has(cmd) && owner.get(cmd) !== m.mod) bad(node, `${cmd} is already registered by ${owner.get(cmd)}`);
        owner.set(cmd, owner.get(cmd) ?? m.mod);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
  const sweep = (node) => {
    if (ts.isIdentifier(node) && node.text === "handlers" && !allowed.has(node) && !ts.isImportSpecifier(node.parent)) {
      bad(node, 'only running.ts may use handlers other than handlers.set("CMD", fn)');
    }
    if (ts.isPropertyAccessExpression(node) && node.name.text === "handlers") bad(node, "use `import { handlers } from \"./running.ts\"`, not a property");
    ts.forEachChild(node, sweep);
  };
  sweep(sf);
}
finish(problems, "registry ownership and naming");
