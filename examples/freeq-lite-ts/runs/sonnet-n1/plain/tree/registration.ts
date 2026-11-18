// graft:synth:begin {"record":"prov-2026-49ddf625","version":1,"gen":"sha256:3aa4bbe0448cf0ecf514bb17d3274761dc4354a163009acbd374a6be7b2fabdf","tier":"low","body":"e753392805a9ead4ba780ece2c370c4e7d4dce9b10dc9b9ea57942adf5ba683b"}
import {
  handlers,
  Client_send,
  Client_prefix,
  Server_reply,
  type Client,
  type Server,
  type Message,
} from "./running.ts";

const NICK_RE = /^[A-Za-z][A-Za-z0-9_-]{0,15}$/;

function registrationValidNick(nick: string): boolean {
  return NICK_RE.test(nick);
}

// Called after NICK or USER while the client is unregistered: finishes
// registration once both have been received.
function registrationTryComplete(s: Server, c: Client): void {
  if (c.registered || c.nick === "" || c.user === "") return;
  const key = c.nick.toLowerCase();
  const owner = s.clients.get(key);
  if (owner && owner !== c) {
    // Someone registered this nick in the meantime.
    Client_send(c, `:freeq-lite 433 * ${c.nick} :Nickname is already in use`);
    c.nick = "";
    return;
  }
  c.registered = true;
  s.clients.set(key, c);
  Server_reply(s, c, "001", `:Welcome to freeq-lite ${c.nick}`);
}

function registrationChangeNick(s: Server, c: Client, newNick: string): void {
  const oldKey = c.nick.toLowerCase();
  const newKey = newNick.toLowerCase();
  const line = `:${Client_prefix(c)} NICK ${newNick}`;

  const audience = new Set<Client>();
  audience.add(c);
  for (const name of c.channels) {
    const ch = s.channels.get(name);
    if (!ch) continue;
    for (const m of ch.members.values()) audience.add(m);
    ch.members.delete(oldKey);
    ch.members.set(newKey, c);
  }

  s.clients.delete(oldKey);
  s.clients.set(newKey, c);
  c.nick = newNick;

  for (const m of audience) Client_send(m, line);
}

function registrationNick(s: Server, c: Client, m: Message): void {
  const nick = m.params[0];
  if (nick === undefined || nick === "") {
    Server_reply(s, c, "431", ":No nickname given");
    return;
  }
  if (!registrationValidNick(nick)) {
    Client_send(c, `:freeq-lite 432 * ${nick} :Erroneous nickname`);
    return;
  }
  const owner = s.clients.get(nick.toLowerCase());
  if (owner && owner !== c) {
    Client_send(c, `:freeq-lite 433 * ${nick} :Nickname is already in use`);
    return;
  }
  if (c.registered) {
    registrationChangeNick(s, c, nick);
    return;
  }
  c.nick = nick;
  registrationTryComplete(s, c);
}

function registrationUser(s: Server, c: Client, m: Message): void {
  if (c.registered) {
    Server_reply(s, c, "462", ":You may not reregister");
    return;
  }
  if (m.params.length < 4 || m.params[0] === "") {
    Server_reply(s, c, "461", "USER :Not enough parameters");
    return;
  }
  c.user = m.params[0];
  registrationTryComplete(s, c);
}

export function init(): void {
  handlers.set("NICK", registrationNick);
  handlers.set("USER", registrationUser);
}
// graft:synth:end {"record":"prov-2026-49ddf625"}
