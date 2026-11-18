"""Entry point, written by graft and not generated.

It imports running, then every other module in name order (each registers its
handlers when imported), then starts the server: python3 main.py -addr host:port
"""
import glob
import importlib
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import running  # noqa: E402

for _f in sorted(glob.glob(os.path.join(HERE, "*.py"))):
    _name = os.path.basename(_f)[:-3]
    if _name not in ("main", "running"):
        importlib.import_module(_name)

if __name__ == "__main__":
    running.main(sys.argv[1:])
