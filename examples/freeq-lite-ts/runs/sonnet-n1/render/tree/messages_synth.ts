// graft:synth:begin {"record":"prov-2026-f410ebe8","version":1,"gen":"sha256:e978822bd34dbcac243ce68fa2209b275830d4b3d3b32e5cafff1ffd2ea0e7bd","tier":"low","body":"b0988e88e62e292dd2dd8bb2dd1694478f86fa73fd8adfcf8b558e6c6f2af11d"}
import { handlers, Client_send, Client_prefix, serverName } from "./running.ts";
import type { Server, Client, Message } from "./running.ts";

// PRIVMSG <target> :<text>. The not-registered rule and unknown commands are
// handled by running.ts's read loop.
function messagesPrivmsg(s: Server, c: Client, m: Message) {
  const target = m.params[0];
  const text = m.params[1];
  if (target === undefined || target === "" || text === undefined || text === "") {
    Client_send(c, ":" + serverName + " 412 " + c.nick + " :No text to send");
    return;
  }

  if (target[0] === "#") {
    const key = target.toLowerCase();
    const ch = s.channels.get(key);
    if (!ch) {
      Client_send(c, ":" + serverName + " 401 " + c.nick + " " + target + " :No such nick/channel");
      return;
    }
    if (!c.channels.has(key)) {
      Client_send(c, ":" + serverName + " 404 " + c.nick + " " + target + " :Cannot send to channel");
      return;
    }
    const line = ":" + Client_prefix(c) + " PRIVMSG " + ch.name + " :" + text;
    for (const member of ch.members.values()) {
      if (member !== c) Client_send(member, line);
    }
    return;
  }

  const dest = s.clients.get(target.toLowerCase());
  if (!dest) {
    Client_send(c, ":" + serverName + " 401 " + c.nick + " " + target + " :No such nick/channel");
    return;
  }
  if (dest === c) return; // a message to oneself is accepted and delivered to no one
  Client_send(dest, ":" + Client_prefix(c) + " PRIVMSG " + dest.nick + " :" + text);
}

export function messagesInitBody() {
  handlers.set("PRIVMSG", messagesPrivmsg);
}
// graft:synth:end {"record":"prov-2026-f410ebe8"}
