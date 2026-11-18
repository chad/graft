freeq-lite is one TypeScript program for Node 24: standard library only (`node:` modules), ES modules, run directly with `node --experimental-strip-types`. Every file is a module in the tree's root directory, and no directories are allowed.

Because Node only erases the types, never use syntax it cannot erase: no `enum`, no `namespace`, no constructor parameter properties (`constructor(private x: T)`), no decorators. Import with explicit extensions: `import { handlers } from "./running.ts"`, `import * as net from "node:net"`. Use named exports; there is no default export.

The entry point is `main.ts`. graft writes it into a fresh tree, it is not generated, and you must not write it. It imports `running.ts`, then every other module in name order and calls its `init()`, then calls `running.main(process.argv.slice(2))`. The server is started as `node --experimental-strip-types main.ts -addr host:port`. Node runs one thread, so there are no locks; never block the event loop.

Architecture, so that each file can be written and checked on its own:

- `running.ts` owns the core and nothing about specific commands. It exports:
  - `serverName = "freeq-lite"`, and `main(argv: string[]): void`, which parses `-addr` (default `:6667`; `host:port` or `:port`) and serves TCP with `net.createServer`.
  - `class Server` with `clients: Map<string, Client>` (registered clients by lower-cased nick) and `channels: Map<string, Channel>` (by lower-cased name); `type Channel = { name: string; members: Map<string, Client> }` keeps the display name and the members.
  - `class Client` per connection: `nick`, `user`, `registered`, `channels: Set<string>` (lower-cased names) and `socket: net.Socket`.
  - `type Message = { command: string; params: string[] }` and a parser for IRC lines (optional `:prefix`, space-separated params, a final `:trailing` param).
  - The command registry `handlers: Map<string, (s: Server, c: Client, m: Message) => void>`. The read loop upper-cases the command and calls the handler. Unknown commands, the not-registered rule and line-length limits are applied here, by the spec's rules.
  - Helpers every other module uses, written as plain functions that take the object first (not methods): `Client_send(c, line)`, `Server_reply(s, c, code, text)` producing `:freeq-lite <code> <nick-or-*> <text>`, `Client_prefix(c)`, and `Server_disconnect(s, c, reason)`, which performs the leaving rules. `Server_serve(s, sock)` runs one connection.
- Every other module exports `init(): void`, which registers its commands with `handlers.set("CMD", fn)` (a literal command, `handlers` imported by that name from `./running.ts`). A module may declare only top-level functions named `init` or starting with its own name (for example `channelsValidName`), so modules never collide, and never classes. It must not read `handlers` (no `get`, `has`, aliases or `running.handlers`).
- Leave no function that nothing uses, because a gate rejects dead functions. If a statement in your section is already handled by another module (for example, unknown commands are answered by `running.ts`'s read loop), do not write a function for it; say in a comment where it is handled.
- Never write to stdout, and never throw out of a socket event handler.
