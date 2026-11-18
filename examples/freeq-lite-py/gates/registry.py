"""Gate: registry ownership and naming (run in the tree's root).

- Only running.py may read HANDLERS; every other module may only assign
  HANDLERS["CMD"] = <name>.
- Every other module defines only top-level functions named <module>_*,
  and no classes, so modules never collide.
"""
import ast
import glob
import sys

problems = []
for f in sorted(glob.glob("*.py")):
    mod = f[:-3]
    if mod in ("main", "running"):
        continue
    tree = ast.parse(open(f).read(), f)
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and not node.name.startswith(mod + "_"):
            problems.append("%s:%d: function %s must be named %s_..." % (f, node.lineno, node.name, mod))
        if isinstance(node, ast.ClassDef):
            problems.append("%s:%d: class %s: only running.py defines classes" % (f, node.lineno, node.name))
    assigned = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Assign):
            for t in node.targets:
                if isinstance(t, ast.Subscript) and isinstance(t.value, ast.Name) and t.value.id == "HANDLERS":
                    assigned.add(id(t.value))
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and node.id == "HANDLERS" and id(node) not in assigned \
                and not isinstance(node.ctx, ast.Store):
            problems.append("%s:%d: only running.py may read HANDLERS; assign HANDLERS[\"CMD\"] = fn" % (f, node.lineno))
        if isinstance(node, ast.Name) and node.id == "HANDLERS" and isinstance(node.ctx, ast.Store):
            problems.append("%s:%d: HANDLERS must not be rebound" % (f, node.lineno))
if problems:
    print("\n".join(problems))
    sys.exit(1)
print("ok: registry ownership and naming")
