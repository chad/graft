"""Gate: every module compiles and imports cleanly (run in the tree's root).

Importing catches what compiling does not: a missing name in a `from running
import ...`, or an error raised while a module registers its handlers.
main.py is compiled but not imported, because importing it would start the server.
"""
import glob
import os
import py_compile
import subprocess
import sys

mods = sorted(glob.glob("*.py"))
bad = []
for f in mods:
    try:
        py_compile.compile(f, doraise=True)
    except py_compile.PyCompileError as e:
        bad.append(str(e))
if bad:
    print("\n".join(bad))
    sys.exit(1)
names = [f[:-3] for f in mods if f != "main.py"]
names.sort(key=lambda n: (n != "running", n))  # running first, like main.py
code = "import importlib, sys\nsys.path.insert(0, '.')\n" + "".join(
    "importlib.import_module(%r)\n" % n for n in names)
r = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=60)
if r.returncode != 0:
    print("importing the modules failed:\n" + r.stderr[-1500:])
    sys.exit(1)
print("ok: %d modules compile and import" % len(mods))
