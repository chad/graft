// graft:synth:begin {"record":"prov-2026-3f163c19","version":1,"gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","tier":"low","body":"18a652ab4d16c692440262c1bd07f2c33dc791e266d9a8081f704be9e1f3c427"}
import * as net from "node:net";

export const serverName = "freeq-lite";

export type Channel = { name: string; members: Map<string, Client> };

export type Message = { command: string; params: string[] };

export class Client {
  nick: string = "";
  user: string = "";
  registered: boolean = false;
  channels: Set<string> = new Set();
  socket: net.Socket;
  closed: boolean = false;

  constructor(socket: net.Socket) {
    this.socket = socket;
  }
}

export class Server {
  clients: Map<string, Client> = new Map();
  channels: Map<string, Channel> = new Map();
}

export const handlers: Map<string, (s: Server, c: Client, m: Message) => void> = new Map();

const MAX_LINE = 512;

// Commands allowed before registration. Unknown commands are answered here
// (421 after registration); other modules only register their own handlers.
const PRE_REGISTRATION = new Set(["NICK", "USER", "PING", "QUIT"]);

export function Client_send(c: Client, line: string): void {
  if (c.closed || c.socket.destroyed || !c.socket.writable) return;
  try {
    c.socket.write(line + "\r\n");
  } catch {
    // ignore write failures; the close event cleans up
  }
}

export function Server_reply(s: Server, c: Client, code: string, text: string): void {
  void s;
  Client_send(c, `:${serverName} ${code} ${c.nick === "" ? "*" : c.nick} ${text}`);
}

export function Client_prefix(c: Client): string {
  return `${c.nick}!${c.user}@${serverName}`;
}

export function Server_disconnect(s: Server, c: Client, reason: string): void {
  if (c.closed) return;
  Client_send(c, "ERROR :Closing link");
  c.closed = true;

  if (c.registered && c.nick !== "") {
    const key = c.nick.toLowerCase();
    if (s.clients.get(key) === c) s.clients.delete(key);
  }

  const peers = new Set<Client>();
  for (const name of c.channels) {
    const ch = s.channels.get(name);
    if (!ch) continue;
    ch.members.delete(c.nick.toLowerCase());
    if (ch.members.size === 0) {
      s.channels.delete(name);
    } else {
      for (const m of ch.members.values()) if (m !== c) peers.add(m);
    }
  }
  c.channels.clear();

  const line = `:${Client_prefix(c)} QUIT :${reason}`;
  for (const p of peers) Client_send(p, line);

  try {
    c.socket.end();
    setTimeout(() => {
      if (!c.socket.destroyed) c.socket.destroy();
    }, 1000).unref();
  } catch {
    // ignore
  }
}

function runningParse(line: string): Message | null {
  let rest = line;
  if (rest.startsWith(":")) {
    const sp = rest.indexOf(" ");
    if (sp < 0) return null;
    rest = rest.slice(sp + 1);
  }
  rest = rest.replace(/^ +/, "");
  if (rest === "") return null;
  const params: string[] = [];
  let command = "";
  let first = true;
  while (rest !== "") {
    if (!first && rest.startsWith(":")) {
      params.push(rest.slice(1));
      break;
    }
    const sp = rest.indexOf(" ");
    const word = sp < 0 ? rest : rest.slice(0, sp);
    rest = sp < 0 ? "" : rest.slice(sp + 1).replace(/^ +/, "");
    if (first) {
      command = word;
      first = false;
    } else {
      params.push(word);
    }
  }
  return { command, params };
}

function runningDispatch(s: Server, c: Client, line: string): void {
  const m = runningParse(line);
  if (!m) return;
  const cmd = m.command.toUpperCase();
  if (!c.registered && !PRE_REGISTRATION.has(cmd)) {
    Server_reply(s, c, "451", ":You have not registered");
    return;
  }
  const h = handlers.get(cmd);
  if (!h) {
    if (c.registered) Server_reply(s, c, "421", `${m.command} :Unknown command`);
    return;
  }
  try {
    h(s, c, m);
  } catch {
    // never throw out of the read loop
  }
}

export function Server_serve(s: Server, sock: net.Socket): void {
  const c = new Client(sock);
  let pending: Buffer = Buffer.alloc(0);
  let discarding = false;

  sock.setNoDelay(true);
  sock.on("error", () => {
    // the close event follows and performs the leaving rules
  });
  sock.on("close", () => {
    try {
      Server_disconnect(s, c, "Connection closed");
    } catch {
      // ignore
    }
  });
  sock.on("data", (chunk: Buffer) => {
    try {
      let data: Buffer = chunk;
      if (discarding) {
        const nl = data.indexOf(10);
        if (nl < 0) return;
        data = data.subarray(nl + 1);
        discarding = false;
      }
      pending = pending.length ? Buffer.concat([pending, data]) : data;
      for (;;) {
        if (c.closed) return;
        const nl = pending.indexOf(10);
        if (nl < 0) break;
        let end = nl;
        if (end > 0 && pending[end - 1] === 13) end--;
        const raw = pending.subarray(0, end);
        pending = pending.subarray(nl + 1);
        if (raw.length > MAX_LINE || raw.length === 0) continue;
        runningDispatch(s, c, raw.toString("utf8"));
      }
      if (pending.length > MAX_LINE + 1) {
        pending = Buffer.alloc(0);
        discarding = true;
      }
    } catch {
      // never throw out of a socket event handler
    }
  });
}

export function main(argv: string[]): void {
  let addr = ":6667";
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if ((a === "-addr" || a === "--addr") && i + 1 < argv.length) {
      addr = argv[++i];
    } else if (a.startsWith("-addr=")) {
      addr = a.slice(6);
    } else if (a.startsWith("--addr=")) {
      addr = a.slice(7);
    }
  }
  const idx = addr.lastIndexOf(":");
  const host = idx > 0 ? addr.slice(0, idx).replace(/^\[|\]$/g, "") : "";
  const port = Number(idx >= 0 ? addr.slice(idx + 1) : addr);

  const server = new Server();
  const listener = net.createServer((sock) => Server_serve(server, sock));
  listener.on("error", (e) => {
    process.stderr.write(`${serverName}: ${String(e)}\n`);
    process.exit(1);
  });
  if (host === "") listener.listen(port);
  else listener.listen(port, host);
}
// graft:synth:end {"record":"prov-2026-3f163c19"}
