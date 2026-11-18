freeq-lite is one Python program: Python 3.9+, standard library only. Every file is a module in the tree's root directory, and no packages are allowed.

The entry point is `main.py`. graft writes it into a fresh tree, it is not generated, and you must not write it. It imports `running`, then every other module in the directory, in name order, and then calls `running.main(sys.argv[1:])`. A module therefore adds its behaviour **when it is imported**. The server is started as `python3 main.py -addr host:port`.

Architecture, so that each file can be written and checked on its own:

- `running.py` owns the core and nothing about specific commands:
  - `def main(argv)` parses `-addr` (default `:6667`) with `argparse` and serves TCP. Use `socket` and one thread per connection; do not use asyncio.
  - `class Server` holds all state behind one `threading.Lock` named `mu`. The state is the registered clients by lower-cased nick, and the channels by lower-cased name. Each channel keeps its display name and its members.
  - `class Client` exists per connection, with `nick`, `user`, `registered`, the set of channels it is in, and an outgoing line queue (`queue.Queue`) drained by one writer thread, so a slow client never blocks the server.
  - `class Message` has `command` and `params`, plus a parser for IRC lines: an optional `:prefix`, space-separated params, and a final `:trailing` param.
  - The command registry is a module-level `HANDLERS = {}`, mapping an upper-case command to `handler(server, client, msg)`. The read loop upper-cases the command and calls the handler with `server.mu` held. The spec's rules for unknown commands, the not-registered rule and line-length limits are applied here.
  - Every other module uses these helpers:
    - `client.send(line)`;
    - `server.reply(client, code, text)`, producing `:freeq-lite <code> <nick-or-*> <text>`;
    - `client.prefix()`;
    - `server.disconnect(client, reason)`, which performs the leaving rules.
- Every other module imports what it needs from `running` (`from running import HANDLERS, ...`). It adds its commands at import time by assigning `HANDLERS["CMD"] = <function>`. It may define only top-level functions whose names start with the module's own name and an underscore (for example `channels_valid_name`), so modules never collide. It must never read `HANDLERS` or define classes.
- Never block while holding `server.mu`: sends go through the client's queue.
- Leave no function that nothing uses, because a gate rejects dead functions. If a statement in your section is already handled by another module (for example, unknown commands are answered by `running.py`'s read loop), do not write a function for it; say in a comment where it is handled.
- Do not print to stdout.
