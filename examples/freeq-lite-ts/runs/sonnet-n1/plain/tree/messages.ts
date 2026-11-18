// graft:synth:begin {"record":"prov-2026-4803adc3","version":1,"gen":"sha256:e978822bd34dbcac243ce68fa2209b275830d4b3d3b32e5cafff1ffd2ea0e7bd","tier":"low","body":"e703b04eef57961f807733dad7600c4c6aad9383c5fd0547657c7cca51c869c7"}
import {
  handlers,
  Client_send,
  Client_prefix,
  Server_reply,
  type Client,
  type Server,
  type Message,
} from "./running.ts";

function messagesPrivmsg(s: Server, c: Client, m: Message): void {
  const target = m.params[0];
  if (target === undefined || target === "") {
    Server_reply(s, c, "411", ":No recipient given (PRIVMSG)");
    return;
  }
  const text = m.params[1];
  if (text === undefined || text === "") {
    Server_reply(s, c, "412", ":No text to send");
    return;
  }

  if (target.startsWith("#")) {
    const ch = s.channels.get(target.toLowerCase());
    if (!ch) {
      Server_reply(s, c, "401", `${target} :No such nick/channel`);
      return;
    }
    if (ch.members.get(c.nick.toLowerCase()) !== c) {
      Server_reply(s, c, "404", `${target} :Cannot send to channel`);
      return;
    }
    const line = `:${Client_prefix(c)} PRIVMSG ${ch.name} :${text}`;
    for (const member of ch.members.values()) {
      if (member !== c) Client_send(member, line);
    }
    return;
  }

  const dest = s.clients.get(target.toLowerCase());
  if (!dest) {
    Server_reply(s, c, "401", `${target} :No such nick/channel`);
    return;
  }
  if (dest === c) return;
  Client_send(dest, `:${Client_prefix(c)} PRIVMSG ${dest.nick} :${text}`);
}

export function init(): void {
  handlers.set("PRIVMSG", messagesPrivmsg);
}
// graft:synth:end {"record":"prov-2026-4803adc3"}
