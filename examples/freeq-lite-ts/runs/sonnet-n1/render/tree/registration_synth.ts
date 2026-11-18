// graft:synth:begin {"record":"prov-2026-32994e4a","version":1,"gen":"sha256:3aa4bbe0448cf0ecf514bb17d3274761dc4354a163009acbd374a6be7b2fabdf","tier":"low","body":"f288c763f86afe9a0efba6a0e5dd6c674941afbb5efdc3b932e791c7b8c7a8c3"}
import { handlers, Client_send, Client_prefix, Server_reply } from "./running.ts";
import type { Server, Client, Message } from "./running.ts";

const registrationNickRe = /^[A-Za-z][A-Za-z0-9_-]{0,15}$/;

function registrationValidNick(nick: string): boolean {
  return registrationNickRe.test(nick);
}

// Sends the welcome once both NICK and USER have been received.
// Re-checks the nick, since another connection may have taken it meanwhile.
function registrationTryComplete(s: Server, c: Client) {
  if (c.registered || c.nick === "" || c.user === "") return;
  const key = c.nick.toLowerCase();
  const holder = s.clients.get(key);
  if (holder !== undefined && holder !== c) {
    Client_send(c, ":freeq-lite 433 * " + c.nick + " :Nickname is already in use");
    c.nick = "";
    return;
  }
  s.clients.set(key, c);
  c.registered = true;
  Server_reply(s, c, "001", "Welcome to freeq-lite " + c.nick);
}

// After registration: change the nick and tell the user and every channel peer once.
function registrationChangeNick(s: Server, c: Client, newNick: string) {
  const oldPrefix = Client_prefix(c);
  const oldKey = c.nick.toLowerCase();
  const newKey = newNick.toLowerCase();

  if (s.clients.get(oldKey) === c) s.clients.delete(oldKey);
  s.clients.set(newKey, c);
  c.nick = newNick;

  const audience: Set<Client> = new Set();
  audience.add(c);
  for (const name of c.channels) {
    const ch = s.channels.get(name);
    if (!ch) continue;
    if (ch.members.get(oldKey) === c) ch.members.delete(oldKey);
    ch.members.set(newKey, c);
    for (const m of ch.members.values()) audience.add(m);
  }

  const line = ":" + oldPrefix + " NICK " + newNick;
  for (const p of audience) Client_send(p, line);
}

function registrationNick(s: Server, c: Client, m: Message) {
  const nick = m.params[0];
  if (nick === undefined || nick === "") return;
  if (!registrationValidNick(nick)) {
    Client_send(c, ":freeq-lite 432 * " + nick + " :Erroneous nickname");
    return;
  }
  const holder = s.clients.get(nick.toLowerCase());
  if (holder !== undefined && holder !== c) {
    Client_send(c, ":freeq-lite 433 * " + nick + " :Nickname is already in use");
    return;
  }
  if (c.registered) {
    registrationChangeNick(s, c, nick);
    return;
  }
  c.nick = nick;
  registrationTryComplete(s, c);
}

function registrationUser(s: Server, c: Client, m: Message) {
  if (c.registered) return;
  const user = m.params[0];
  if (user === undefined || user === "") return;
  c.user = user;
  registrationTryComplete(s, c);
}

// Commands before registration other than NICK, USER, PING and QUIT get 451 in
// running.ts's read loop, so nothing is registered for that here.
export function registrationInitBody() {
  handlers.set("NICK", registrationNick);
  handlers.set("USER", registrationUser);
}
// graft:synth:end {"record":"prov-2026-32994e4a"}
