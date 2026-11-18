"""Gate: registry ownership and naming (run in the tree's root).

- Only running.py may touch HANDLERS. Every other module may only assign
  HANDLERS["CMD"] = <name>, written exactly like that (no alias, no
  `running.HANDLERS`, no rebinding, no read).
- A command is registered by one module only.
- Every other module defines only top-level functions named <module>_*,
  and no classes, so modules never collide.
"""
import ast
import glob
import sys

problems = []
owner = {}  # command -> module that registers it
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
    allowed = set()  # ids of the HANDLERS Name in HANDLERS["X"] = ... targets
    for node in ast.walk(tree):
        if isinstance(node, ast.Assign):
            for t in node.targets:
                if isinstance(t, ast.Subscript) and isinstance(t.value, ast.Name) and t.value.id == "HANDLERS":
                    allowed.add(id(t.value))
                    key = t.slice.value if isinstance(t.slice, getattr(ast, "Index", ())) else t.slice  # 3.8 compat
                    if isinstance(key, ast.Constant) and isinstance(key.value, str):
                        cmd = key.value.upper()
                        if cmd in owner and owner[cmd] != f:
                            problems.append("%s:%d: %s is already registered by %s" % (f, node.lineno, cmd, owner[cmd]))
                        owner.setdefault(cmd, f)
                    else:
                        problems.append("%s:%d: register with a literal command: HANDLERS[\"CMD\"] = fn" % (f, node.lineno))
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and node.id == "HANDLERS" and id(node) not in allowed:
            problems.append("%s:%d: only running.py may use HANDLERS other than HANDLERS[\"CMD\"] = fn" % (f, node.lineno))
        if isinstance(node, ast.Attribute) and node.attr == "HANDLERS":
            problems.append("%s:%d: use `from running import HANDLERS`, not an attribute" % (f, node.lineno))
        if isinstance(node, ast.alias) and node.name == "HANDLERS" and node.asname:
            problems.append("%s:%d: HANDLERS must not be imported under another name" % (f, node.lineno))
if problems:
    print("\n".join(problems))
    sys.exit(1)
print("ok: registry ownership and naming")
