// graft:synth:begin {"record":"prov-2026-439c923a","version":1,"gen":"sha256:d4c1df0bcfd7bf6b70d0d6f6d1a0e4ddb2921e04c6e7cd94a7f5daf8cdc3de54","tier":"low","body":"cda996251ca431a7db6e42af8d1f52d87d4951081971bcf88adc2052596c91ca"}
import {
  handlers,
  Client_send,
  Client_prefix,
  Server_reply,
  type Client,
  type Server,
  type Message,
  type Channel,
} from "./running.ts";

function channelsValidName(name: string): boolean {
  if (name.length < 2 || name.length > 32) return false;
  if (name[0] !== "#") return false;
  for (let i = 0; i < name.length; i++) {
    const code = name.charCodeAt(i);
    if (code <= 0x20 || code === 0x7f || name[i] === ",") return false;
  }
  return true;
}

function channelsSendNames(s: Server, c: Client, ch: Channel): void {
  const nicks: string[] = [];
  for (const m of ch.members.values()) nicks.push(m.nick);
  nicks.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
  Server_reply(s, c, "353", `= ${ch.name} :${nicks.join(" ")}`);
  Server_reply(s, c, "366", `${ch.name} :End of /NAMES list`);
}

function channelsJoin(s: Server, c: Client, m: Message): void {
  const name = m.params[0];
  if (name === undefined || name === "") {
    Server_reply(s, c, "461", "JOIN :Not enough parameters");
    return;
  }
  if (!channelsValidName(name)) {
    Server_reply(s, c, "403", `${name} :No such channel`);
    return;
  }
  const key = name.toLowerCase();
  let ch = s.channels.get(key);
  if (ch && ch.members.get(c.nick.toLowerCase()) === c) return;
  if (!ch) {
    ch = { name, members: new Map() };
    s.channels.set(key, ch);
  }
  ch.members.set(c.nick.toLowerCase(), c);
  c.channels.add(key);
  const line = `:${Client_prefix(c)} JOIN ${ch.name}`;
  for (const member of ch.members.values()) Client_send(member, line);
  channelsSendNames(s, c, ch);
}

function channelsPart(s: Server, c: Client, m: Message): void {
  const name = m.params[0];
  if (name === undefined || name === "") {
    Server_reply(s, c, "461", "PART :Not enough parameters");
    return;
  }
  const key = name.toLowerCase();
  const ch = s.channels.get(key);
  if (!ch || ch.members.get(c.nick.toLowerCase()) !== c) {
    Server_reply(s, c, "442", `${name} :You're not on that channel`);
    return;
  }
  const reason = m.params[1];
  const line =
    reason !== undefined
      ? `:${Client_prefix(c)} PART ${ch.name} :${reason}`
      : `:${Client_prefix(c)} PART ${ch.name}`;
  for (const member of ch.members.values()) Client_send(member, line);
  ch.members.delete(c.nick.toLowerCase());
  c.channels.delete(key);
  if (ch.members.size === 0) s.channels.delete(key);
}

function channelsNames(s: Server, c: Client, m: Message): void {
  const name = m.params[0];
  if (name === undefined || name === "") {
    Server_reply(s, c, "461", "NAMES :Not enough parameters");
    return;
  }
  const ch = s.channels.get(name.toLowerCase());
  if (!ch) {
    Server_reply(s, c, "366", `${name} :End of /NAMES list`);
    return;
  }
  channelsSendNames(s, c, ch);
}

export function init(): void {
  handlers.set("JOIN", channelsJoin);
  handlers.set("PART", channelsPart);
  handlers.set("NAMES", channelsNames);
}
// graft:synth:end {"record":"prov-2026-439c923a"}
