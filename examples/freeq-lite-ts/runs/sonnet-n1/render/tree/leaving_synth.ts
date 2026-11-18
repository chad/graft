// graft:synth:begin {"record":"prov-2026-99ef460f","version":1,"gen":"sha256:28e1afe4042aca16b415dafddb8e0ba0975da4d466ff4db2ea69d0bc43cb26f5","tier":"low","body":"17e35efba5dd365f0a3330cefa74c99eff081339852bf5c1c893c30e52d4324d"}
import { handlers, Client_send, Server_disconnect } from "./running.ts";
import type { Server, Client, Message } from "./running.ts";

// QUIT [:<reason>]: send ERROR :Closing link, then leave with the given
// reason ("Client quit" when none). Server_disconnect frees the nick, removes
// the user from every channel, tells channel peers once, and closes the socket.
// A connection that drops without QUIT is handled by running.ts's read loop,
// which calls Server_disconnect with "Connection closed".
function leavingQuit(s: Server, c: Client, m: Message) {
  const given = m.params[0];
  const reason = given === undefined || given === "" ? "Client quit" : given;
  Client_send(c, "ERROR :Closing link");
  Server_disconnect(s, c, reason);
}

export function leavingInitBody() {
  handlers.set("QUIT", leavingQuit);
}
// graft:synth:end {"record":"prov-2026-99ef460f"}
