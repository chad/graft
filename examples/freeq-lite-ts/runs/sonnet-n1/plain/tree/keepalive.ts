// graft:synth:begin {"record":"prov-2026-b5ca7e1a","version":1,"gen":"sha256:5920bc0fbefc3037cd277f8a23e404095d6fc496c6c9f50d782aefe90561615b","tier":"low","body":"2210d9f2078c8b9c11693fd62126e0108a6024a0cad0993bc9d61b17b398a91e"}
import { handlers, Client_send, serverName, type Client, type Server, type Message } from "./running.ts";

function keepalivePing(s: Server, c: Client, m: Message): void {
  void s;
  const token = m.params.length > 0 ? m.params[m.params.length - 1] : "";
  Client_send(c, `:${serverName} PONG ${serverName} :${token}`);
}

export function init(): void {
  handlers.set("PING", keepalivePing);
}
// graft:synth:end {"record":"prov-2026-b5ca7e1a"}
