// graft:synth:begin {"record":"prov-2026-d6f7fd9a","version":1,"gen":"sha256:5920bc0fbefc3037cd277f8a23e404095d6fc496c6c9f50d782aefe90561615b","tier":"low","body":"ac2642371ec709a1bb317d98376c1f0351a6bb20b07634d01cf747ba3c458a06"}
import { handlers, Client_send, serverName } from "./running.ts";
import type { Server, Client, Message } from "./running.ts";

// PING works before and after registration: the not-registered rule in
// running.ts's read loop lets PING through, so no check is needed here.
function keepalivePing(_s: Server, c: Client, m: Message) {
  const token = m.params[0] ?? "";
  Client_send(c, ":" + serverName + " PONG " + serverName + " :" + token);
}

export function keepaliveInitBody() {
  handlers.set("PING", keepalivePing);
}
// graft:synth:end {"record":"prov-2026-d6f7fd9a"}
