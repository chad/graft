"""Gate: no dead functions (run in the tree's root).

Every function counts, including methods of running.py's classes and nested
defs. Each must be referenced somewhere outside its own body: a call, an
attribute use like client.send, HANDLERS["X"] = fn, or an import. These do NOT
count as a use:
- a reference from inside the function itself (recursion);
- a string literal;
- an assignment to "_" (`_ = f` only silences a checker).
Names the runtime calls itself (main, dunders, threading.Thread's run) count as used.
"""
import ast
import glob
import sys

ALWAYS = {"main", "run"}


def walk_outside(node, skip):
    """Yield every node under node, without descending into the node skip."""
    for child in ast.iter_child_nodes(node):
        if child is skip:
            continue
        yield child
        yield from walk_outside(child, skip)


trees = {f: ast.parse(open(f).read(), f) for f in sorted(glob.glob("*.py"))}


def is_underscore_sink(tree):
    """ids of Name nodes that are only the value of an assignment to `_`."""
    sink = set()
    for n in ast.walk(tree):
        if isinstance(n, ast.Assign) and all(isinstance(t, ast.Name) and t.id == "_" for t in n.targets):
            for v in ast.walk(n.value):
                sink.add(id(v))
    return sink


sinks = {f: is_underscore_sink(t) for f, t in trees.items()}


def used_outside(name, fn):
    for f, tree in trees.items():
        for n in walk_outside(tree, fn):
            if id(n) in sinks[f]:
                continue
            if isinstance(n, ast.Name) and n.id == name and isinstance(n.ctx, ast.Load):
                return True
            if isinstance(n, ast.Attribute) and n.attr == name:
                return True
            if isinstance(n, ast.alias) and (n.asname or n.name) == name:
                return True
    return False


dead = []
for f, tree in trees.items():
    for n in ast.walk(tree):
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
            name = n.name
            if name in ALWAYS or (name.startswith("__") and name.endswith("__")):
                continue
            if not used_outside(name, n):
                dead.append((f, n.lineno, name))
if dead:
    for f, l, n in dead:
        print("%s:%d: %s is never used; remove it (or use it)" % (f, l, n))
    sys.exit(1)
print("ok: no dead functions")
