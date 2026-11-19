// Gate: no dead functions (run in the tree's root).
//
// Every function counts: declarations, methods and function-valued consts,
// nested ones included. Each must be referenced somewhere outside its own
// body: a call, a property use like c.send, a shorthand, handlers.set("X", fn)
// or an import. These do NOT count as a use:
// - a reference from inside the function itself (recursion);
// - a string literal;
// - `_ = f` or `void f`, which only silence a checker.
// `main` and `init` count as used: main.ts calls them. So do the functions
// running.ts exports: they are the core's API for the other modules, which may
// not be written yet when running.ts is checked (and under --render the
// skeleton fixes them).
import { modules, line, finish, ts } from "./lib.mjs";

const ALWAYS = new Set(["main", "init"]);
const mods = modules();

// Every function-like declaration: [name, node, file, sf].
const fns = [];
for (const m of mods) {
  const visit = (node) => {
    if ((ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node)) && node.name && ts.isIdentifier(node.name)) {
      fns.push({ name: node.name.text, node, m });
    } else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer &&
               (ts.isArrowFunction(node.initializer) || ts.isFunctionExpression(node.initializer))) {
      fns.push({ name: node.name.text, node, m });
    }
    ts.forEachChild(node, visit);
  };
  visit(m.sf);
}

const isSink = (id) => {
  let n = id;
  while (n.parent) {
    const p = n.parent;
    if (ts.isVoidExpression(p)) return true;
    if (ts.isBinaryExpression(p) && p.operatorToken.kind === ts.SyntaxKind.EqualsToken && ts.isIdentifier(p.left) && p.left.text === "_" && p.right === n) return true;
    if (ts.isExpressionStatement(p) || ts.isBlock(p) || ts.isSourceFile(p)) return false;
    n = p;
  }
  return false;
};

const used = (fn) => {
  for (const m of mods) {
    let found = false;
    const visit = (node) => {
      if (found || node === fn.node) return; // do not look inside the function itself
      if (ts.isIdentifier(node) && node.text === fn.name && !isDeclName(node) && !isSink(node)) found = true;
      ts.forEachChild(node, visit);
    };
    visit(m.sf);
    if (found) return true;
  }
  return false;
};
// A declaration's own name is not a use of it.
const isDeclName = (id) => {
  const p = id.parent;
  return (
    (ts.isFunctionDeclaration(p) || ts.isMethodDeclaration(p) || ts.isVariableDeclaration(p) || ts.isParameter(p) || ts.isPropertyDeclaration(p)) &&
    p.name === id
  );
};

const exportedCore = (fn) =>
  fn.m.mod === "running" &&
  ts.isFunctionDeclaration(fn.node) &&
  fn.node.parent === fn.m.sf &&
  (ts.getCombinedModifierFlags(fn.node) & ts.ModifierFlags.Export) !== 0;

const problems = [];
for (const fn of fns) {
  if (ALWAYS.has(fn.name) || exportedCore(fn)) continue;
  if (!used(fn)) problems.push(`${fn.m.file}:${line(fn.m.sf, fn.node)}: ${fn.name} is never used; remove it (or use it)`);
}
finish(problems, "no dead functions");
