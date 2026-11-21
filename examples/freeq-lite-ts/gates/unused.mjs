// Gate: no dead functions (run in the tree's root).
//
// Every function counts: declarations, methods and function-valued consts,
// nested ones included. Each must be referenced somewhere outside its own
// body: a call, a property use like c.send, a shorthand, handlers.set("X", fn)
// or an import. These do NOT count as a use:
// - a reference from inside the function itself (recursion);
// - a string literal;
// - `_ = f` or `void f`, which only silence a checker.
// A name used only as an object or class key ({ fname: 1 }, a property
// declaration's name, an interface member) is not a use either: it names a
// property, not the function. Class methods and function-valued class fields
// are functions like any other; constructors, accessors and the well-known
// protocol methods the runtime calls (toString, toJSON, valueOf, …) count as
// used.
// `main` and `init` count as used: main.ts calls them. So do the functions
// running.ts exports: they are the core's API for the other modules, which may
// not be written yet when running.ts is checked (and under --render the
// skeleton fixes them).
import { modules, line, finish, ts } from "./lib.mjs";

const ALWAYS = new Set(["main", "init"]);
// Protocol methods the runtime calls by name: exempt only as methods (class
// or object-literal members), never as top-level functions.
const PROTOCOL = new Set(["toString", "toJSON", "valueOf", "toLocaleString", "then", "catch", "finally", "next", "return", "throw"]);
const isMember = (fn) => ts.isMethodDeclaration(fn.node) || ts.isPropertyDeclaration(fn.node) || ts.isPropertyAssignment(fn.node);
const mods = modules();

// Every function-like declaration: [name, node, file, sf].
const fns = [];
for (const m of mods) {
  const visit = (node) => {
    if ((ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node)) && node.name && ts.isIdentifier(node.name)) {
      fns.push({ name: node.name.text, node, m });
    } else if ((ts.isVariableDeclaration(node) || ts.isPropertyDeclaration(node) || ts.isPropertyAssignment(node)) && ts.isIdentifier(node.name) && node.initializer &&
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
// A declaration's own name, or a key, is not a use of the function.
const isDeclName = (id) => {
  const p = id.parent;
  return (
    (ts.isFunctionDeclaration(p) || ts.isMethodDeclaration(p) || ts.isVariableDeclaration(p) || ts.isParameter(p) ||
     ts.isPropertyDeclaration(p) || ts.isPropertyAssignment(p) || ts.isPropertySignature(p) || ts.isMethodSignature(p) ||
     ts.isGetAccessorDeclaration(p) || ts.isSetAccessorDeclaration(p) || ts.isEnumMember(p) || ts.isLabeledStatement(p)) &&
    p.name === id ||
    // A destructuring key ({ aFn: x } = src) names a property, not the function.
    (ts.isBindingElement(p) && p.propertyName === id)
  );
};

const exportedCore = (fn) =>
  fn.m.mod === "running" &&
  ts.isFunctionDeclaration(fn.node) &&
  fn.node.parent === fn.m.sf &&
  (ts.getCombinedModifierFlags(fn.node) & ts.ModifierFlags.Export) !== 0;

const problems = [];
for (const fn of fns) {
  if (ALWAYS.has(fn.name) || exportedCore(fn) || (PROTOCOL.has(fn.name) && isMember(fn))) continue;
  if (!used(fn)) problems.push(`${fn.m.file}:${line(fn.m.sf, fn.node)}: ${fn.name} is never used; remove it (or use it)`);
}
finish(problems, "no dead functions");
