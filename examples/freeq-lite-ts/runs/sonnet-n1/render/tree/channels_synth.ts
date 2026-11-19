// graft:synth:begin {"record":"prov-2026-e821bd18","version":1,"gen":"sha256:d4c1df0bcfd7bf6b70d0d6f6d1a0e4ddb2921e04c6e7cd94a7f5daf8cdc3de54","tier":"low","body":"76a325b5c32a045b76ee003cfb941eeeea9745d84935168ea159180b8ea5b2ca"}
import { handlers, Client_send, Client_prefix, serverName } from "./running.ts";
import type { Server, Client, Message } from "./running.ts";

// A channel name starts with '#', is 2 to 32 characters long, and has no
// spaces, commas or control characters.
function channelsValidName(name: string): boolean {
  if (name.length < 2 || name.length > 32) return false;
  if (name[0] !== "#") return false;
  for (let i = 0; i < name.length; i++) {
    const code = name.charCodeAt(i);
    if (code <= 0x20 || code === 0x7f || name[i] === ",") return false;
  }
  return true;
}

// Sends the 353 and 366 lines for a channel (or only 366 when it does not exist).
function channelsSendNames(s: Server, c: Client, name: string) {
  const ch = s.channels.get(name.toLowerCase());
  if (ch) {
    const nicks: string[] = [];
    for (const m of ch.members.values()) nicks.push(m.nick);
    nicks.sort();
    Client_send(c, ":" + serverName + " 353 " + c.nick + " = " + ch.name + " :" + nicks.join(" "));
    Client_send(c, ":" + serverName + " 366 " + c.nick + " " + ch.name + " :End of /NAMES list");
  } else {
    Client_send(c, ":" + serverName + " 366 " + c.nick + " " + name + " :End of /NAMES list");
  }
}

function channelsJoin(s: Server, c: Client, m: Message) {
  const name = m.params[0];
  if (name === undefined || name === "") return;
  if (!channelsValidName(name)) {
    Client_send(c, ":" + serverName + " 403 " + c.nick + " " + name + " :No such channel");
    return;
  }
  const key = name.toLowerCase();
  if (c.channels.has(key)) return;
  let ch = s.channels.get(key);
  if (!ch) {
    ch = { name, members: new Map() };
    s.channels.set(key, ch);
  }
  ch.members.set(c.nick.toLowerCase(), c);
  c.channels.add(key);
  const line = ":" + Client_prefix(c) + " JOIN " + ch.name;
  for (const member of ch.members.values()) Client_send(member, line);
  channelsSendNames(s, c, ch.name);
}

function channelsPart(s: Server, c: Client, m: Message) {
  const name = m.params[0];
  if (name === undefined || name === "") return;
  const key = name.toLowerCase();
  const ch = s.channels.get(key);
  if (!ch || !c.channels.has(key)) {
    Client_send(c, ":" + serverName + " 442 " + c.nick + " " + name + " :You're not on that channel");
    return;
  }
  const reason = m.params[1];
  let line = ":" + Client_prefix(c) + " PART " + ch.name;
  if (reason !== undefined) line += " :" + reason;
  for (const member of ch.members.values()) Client_send(member, line);
  for (const [k, member] of ch.members) {
    if (member === c) ch.members.delete(k);
  }
  c.channels.delete(key);
  if (ch.members.size === 0) s.channels.delete(key);
}

function channelsNames(s: Server, c: Client, m: Message) {
  const name = m.params[0];
  if (name === undefined || name === "") return;
  channelsSendNames(s, c, name);
}

export function channelsInitBody() {
  handlers.set("JOIN", channelsJoin);
  handlers.set("PART", channelsPart);
  handlers.set("NAMES", channelsNames);
}
// graft:synth:end {"record":"prov-2026-e821bd18"}
