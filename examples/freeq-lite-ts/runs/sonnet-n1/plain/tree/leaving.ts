// graft:synth:begin {"record":"prov-2026-778d8eb7","version":1,"gen":"sha256:28e1afe4042aca16b415dafddb8e0ba0975da4d466ff4db2ea69d0bc43cb26f5","tier":"low","body":"2ed27d1de8546fa99237bda482cf55d4f906513313a6dd51e634e32f3b7eccae"}
import { handlers, Server_disconnect, type Client, type Server, type Message } from "./running.ts";

function leavingQuit(s: Server, c: Client, m: Message): void {
  const reason = m.params.length > 0 && m.params[0] !== "" ? m.params[0] : "Client quit";
  Server_disconnect(s, c, reason);
}

// A connection that drops without QUIT is handled by running.ts: its socket
// "close" handler calls Server_disconnect with "Connection closed".
export function init(): void {
  handlers.set("QUIT", leavingQuit);
}
// graft:synth:end {"record":"prov-2026-778d8eb7"}
