"""Gate: no dead functions (run in the tree's root).

A top-level function in any module, or a method of a class in running.py, must be
referenced somewhere in the tree other than its own definition. That covers
HANDLERS["X"] = fn, a call, or an attribute use like client.send. Names
the runtime calls itself (main, dunders, threading.Thread run) count as used.
"""
import ast
import glob
import sys

ALWAYS = {"main", "run"}
defs = []  # (file, line, name)
used = set()
for f in sorted(glob.glob("*.py")):
    tree = ast.parse(open(f).read(), f)
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            defs.append((f, node.lineno, node.name))
        if isinstance(node, ast.ClassDef):
            for m in node.body:
                if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    defs.append((f, m.lineno, m.name))
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load):
            used.add(node.id)
        elif isinstance(node, ast.Attribute):
            used.add(node.attr)
        elif isinstance(node, ast.alias):
            used.add(node.name)
        elif isinstance(node, ast.Constant) and isinstance(node.value, str):
            used.add(node.value)  # getattr(obj, "name") and similar
dead = [(f, l, n) for f, l, n in defs
        if n not in used and n not in ALWAYS and not (n.startswith("__") and n.endswith("__"))]
if dead:
    for f, l, n in dead:
        print("%s:%d: %s is never used; remove it (or use it)" % (f, l, n))
    sys.exit(1)
print("ok: no dead functions")
