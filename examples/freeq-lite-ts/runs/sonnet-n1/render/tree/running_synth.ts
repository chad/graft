// graft:synth:begin {"record":"prov-2026-4f5ccee6","version":1,"gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","tier":"low","body":"6bb4232eab7f3d196249627854923f3937f761d2176496a948241f93ff43105a"}
import * as net from "node:net";
import { Client, handlers, serverName, Client_send, Server_reply, Server_disconnect } from "./running.ts";
import type { Server, Message } from "./running.ts";

const runningMaxLine = 512;
const runningGone: WeakSet<Client> = new WeakSet();

// Parses one IRC line: optional ":prefix", command, space-separated params, final ":trailing".
// The command is kept exactly as the client sent it.
function runningParse(line: string): Message | null {
  let rest = line;
  if (rest.startsWith(":")) {
    const sp = rest.indexOf(" ");
    if (sp < 0) return null;
    rest = rest.slice(sp + 1);
  }
  const params: string[] = [];
  let command = "";
  let first = true;
  while (rest.length > 0) {
    rest = rest.replace(/^ +/, "");
    if (rest.length === 0) break;
    if (!first && rest.startsWith(":")) {
      params.push(rest.slice(1));
      break;
    }
    const sp = rest.indexOf(" ");
    const tok = sp < 0 ? rest : rest.slice(0, sp);
    rest = sp < 0 ? "" : rest.slice(sp + 1);
    if (first) {
      command = tok;
      first = false;
    } else {
      params.push(tok);
    }
  }
  if (command === "") return null;
  return { command, params };
}

// The read loop's dispatch: the not-registered rule, then handlers, then 421 for unknown commands.
function runningDispatch(s: Server, c: Client, m: Message) {
  const upper = m.command.toUpperCase();
  if (!c.registered) {
    if (upper !== "NICK" && upper !== "USER" && upper !== "PING" && upper !== "QUIT") {
      Server_reply(s, c, "451", "You have not registered");
      return;
    }
  }
  const h = handlers.get(upper);
  if (!h) {
    if (c.registered) {
      Client_send(c, ":" + serverName + " 421 " + c.nick + " " + m.command + " :Unknown command");
    }
    return;
  }
  try {
    h(s, c, m);
  } catch {
    // a failing handler must not take the connection or the server down
  }
}

function runningHandleLine(s: Server, c: Client, raw: Buffer) {
  let end = raw.length;
  if (end > 0 && raw[end - 1] === 13) end--;
  if (end > runningMaxLine) return; // over-long lines are ignored
  if (end === 0) return;
  const m = runningParse(raw.subarray(0, end).toString("utf8"));
  if (m) runningDispatch(s, c, m);
}

export function runningServeBody(s: Server, sock: net.Socket) {
  const c = new Client(sock);
  let buf: Buffer = Buffer.alloc(0);
  let discarding = false;
  sock.setNoDelay(true);

  sock.on("data", (chunk: Buffer) => {
    try {
      if (runningGone.has(c)) return;
      let data: Buffer = chunk;
      if (discarding) {
        const nl = data.indexOf(10);
        if (nl < 0) return;
        data = data.subarray(nl + 1);
        discarding = false;
      }
      buf = buf.length ? Buffer.concat([buf, data]) : data;
      for (;;) {
        if (runningGone.has(c)) {
          buf = Buffer.alloc(0);
          return;
        }
        const nl = buf.indexOf(10);
        if (nl < 0) {
          if (buf.length > runningMaxLine + 2) {
            // already too long: drop it and skip to the next newline
            buf = Buffer.alloc(0);
            discarding = true;
          }
          return;
        }
        const line = buf.subarray(0, nl);
        buf = buf.subarray(nl + 1);
        runningHandleLine(s, c, line);
      }
    } catch {
      // never throw out of a socket event handler
    }
  });

  const gone = () => {
    try {
      Server_disconnect(s, c, "Connection closed");
    } catch {
      // ignore
    }
  };
  sock.on("end", gone);
  sock.on("close", gone);
  sock.on("error", gone);
}

export function runningSendBody(c: Client, line: string) {
  const sock = c.socket;
  if (sock.destroyed || sock.writableEnded || !sock.writable) return;
  try {
    sock.write(line + "\r\n");
  } catch {
    // socket already gone
  }
}

export function runningReplyBody(s: Server, c: Client, code: string, text: string) {
  const target = c.nick === "" ? "*" : c.nick;
  Client_send(c, ":" + serverName + " " + code + " " + target + " :" + text);
}

export function runningPrefixBody(c: Client): string {
  return c.nick + "!" + c.user + "@" + serverName;
}

export function runningDisconnectBody(s: Server, c: Client, reason: string) {
  if (runningGone.has(c)) return;
  runningGone.add(c);

  if (c.nick !== "") {
    const key = c.nick.toLowerCase();
    if (s.clients.get(key) === c) s.clients.delete(key);
  }

  const peers: Set<Client> = new Set();
  for (const name of c.channels) {
    const ch = s.channels.get(name);
    if (!ch) continue;
    for (const [k, m] of ch.members) {
      if (m === c) ch.members.delete(k);
    }
    for (const m of ch.members.values()) peers.add(m);
    if (ch.members.size === 0) s.channels.delete(name);
  }
  c.channels.clear();

  if (c.registered) {
    const line = ":" + runningPrefixBody(c) + " QUIT :" + reason;
    for (const p of peers) Client_send(p, line);
  }

  try {
    c.socket.end();
  } catch {
    // ignore
  }
}
// graft:synth:end {"record":"prov-2026-4f5ccee6"}
