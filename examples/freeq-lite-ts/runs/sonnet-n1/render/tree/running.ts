// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"running.ts","path":"./running_synth.ts"},"kwargs":"sha256:534697d5638a2a0f6e13d1ca7f7280d8eba79eb88016c81c352acc0b3a06818e","body":"sha256:a31748b153fe1dbe31acf48fbec20bca4f542c5d227b0fbee2bf509a8e0a5a6b"}
import * as running_synth from "./running_synth.ts";

// /sedum:addImport
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"running.ts","path":"node:net"},"kwargs":"sha256:b618d8a0d07709fdbb21f38349f1bf8b2f7a34b36a920827518218fdc80cb8e7","body":"sha256:cbf8d5e0a1cbd534ae544fd9e80e7c98cd7271c3e4ea4268ad56be12bcbb1bcc"}
import * as net from "node:net";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setConst {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:51be1c18998d63cc558c253e211a6bd188d96cb251dad68c6ea88e03c2d5cc4c","id":{"file":"running.ts","name":"serverName"},"kwargs":"sha256:0562eeb9f8d4e466cafdf264f8ccefaa98c538e456706db7717e346676a7d321","body":"sha256:b6a9213c925d9c1708678069febcc84b79f517618d45560b8faf98e273ad7716"}
export const serverName = "freeq-lite";

// /sedum:setConst
// sedum:setType {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:ebb283283a745fdc09f0294b6e79bd879a830829cb964550ec6b4432fee5bb72","id":{"file":"running.ts","name":"Message"},"kwargs":"sha256:bce38f7efc86993cdd14a71292a81ba4003df7898dfbadb304805404ff5448b4","body":"sha256:160dbb208c34ebc49ad73e2d1c46115fd26d840bf345ce2bcb81943edd15d8e6"}
export type Message = { command: string; params: string[] };

// /sedum:setType
// sedum:setType {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:ebb283283a745fdc09f0294b6e79bd879a830829cb964550ec6b4432fee5bb72","id":{"file":"running.ts","name":"Channel"},"kwargs":"sha256:16b0dab5387f2c76b8802eb7464ad467c83159a43fcf24659d5a685c6ca128e7","body":"sha256:e7e10fad1075be650bf7b288cd722e82ccfdfc8db774909e467b7c5b74906cf7"}
export type Channel = { name: string; members: Map<string, Client> };

// /sedum:setType
// sedum:setType {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:ebb283283a745fdc09f0294b6e79bd879a830829cb964550ec6b4432fee5bb72","id":{"file":"running.ts","name":"Client"},"kwargs":"sha256:54c6410af395635d531ee6dd65b53ad828bb7bce340b80d176715bbccc32fa0b","body":"sha256:672036923a6e2aabb5f36cc400488c1e6b02955fadf304b28ab1020e995b9e6b"}
export class Client {
  nick = "";
  user = "";
  registered = false;
  channels: Set<string> = new Set();
  socket: net.Socket;
  constructor(socket: net.Socket) {
    this.socket = socket;
  }
}

// /sedum:setType
// sedum:setType {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:ebb283283a745fdc09f0294b6e79bd879a830829cb964550ec6b4432fee5bb72","id":{"file":"running.ts","name":"Server"},"kwargs":"sha256:8e4a5ea54805b882bb381da957125a3cd540aa772dba16ed92d085198566accf","body":"sha256:79d98a87576ab4a77b3dfefac8f4c6cd933a46dcee4f1a121f4aefa4983164db"}
export class Server {
  clients: Map<string, Client> = new Map();
  channels: Map<string, Channel> = new Map();
}

// /sedum:setType
// sedum:setConst {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:51be1c18998d63cc558c253e211a6bd188d96cb251dad68c6ea88e03c2d5cc4c","id":{"file":"running.ts","name":"handlers"},"kwargs":"sha256:e978a2c3b57873dc194961b7f8a7b71f09e7e73698f67cfc4b41b005623efee1","body":"sha256:0e187de2807fe57bcc1e2f12c43cdec27abf06c84d4e78ec178b4df3697dd403"}
export const handlers: Map<string, (s: Server, c: Client, m: Message) => void> = new Map();

// /sedum:setConst
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"main"},"kwargs":"sha256:4d06112ddc2314626b061570e66a77ee86e2ca6d944907843ab9ff489cee0f3c","body":"sha256:8e6ee4b6ea92a597551109f30a98c52fda40ca0d32046900f06acf7e305cf22e"}
export function main(argv: string[]): void {
  let addr = ":6667";
  const i = argv.indexOf("-addr");
  if (i >= 0 && i + 1 < argv.length) {
    addr = argv[i + 1];
  }
  const cut = addr.lastIndexOf(":");
  const host = addr.slice(0, cut) || undefined;
  const port = Number(addr.slice(cut + 1));
  const s = new Server();
  net.createServer((sock) => Server_serve(s, sock)).listen(port, host);
}

// /sedum:setFunction
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"Server_serve"},"kwargs":"sha256:c8e01e0e396026dcf715a0e8d31d52610485d7f4925d5658b125e969466d66b1","body":"sha256:ad37e926c79a879d062f2c383d34d5c30fcc4806eb52bdff3d5fb1e16560e7c7"}
// Server_serve runs one connection: it makes a Client for sock, buffers incoming data into lines (CRLF, or a bare LF), parses each into a Message, dispatches it through handlers by its upper-cased command, and calls Server_disconnect when the socket ends or errors. Never throw out of a socket event handler.
// serve is rendered by graft; its body is a convergence region (runningServeBody).
export function Server_serve(s: Server, sock: net.Socket) {
  running_synth.runningServeBody(s, sock);
}

// /sedum:setFunction
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"Client_send"},"kwargs":"sha256:760e2ce21146e1b528dcc7381d3cba56bdf033e89c0a8bcee450edee5882d78c","body":"sha256:32429a1ec06b3cb28400b61df6c103dfce4c18e6966560fa1b43a3a0a0b5f9df"}
// Client_send writes one protocol line to c's socket. line carries no trailing CRLF and is already complete, including any leading ':'; Client_send only appends CRLF, and does nothing once the socket is closed.
// send is rendered by graft; its body is a convergence region (runningSendBody).
export function Client_send(c: Client, line: string) {
  running_synth.runningSendBody(c, line);
}

// /sedum:setFunction
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"Server_reply"},"kwargs":"sha256:927dd0f15628850830ca99a0b39a599eefa4ea3fbf567f5291e72b1b0fd4c6dd","body":"sha256:87a34e9c9867478527aced24f6cb41f4c49010fecb32b2a92dd3291eb8416d6c"}
// Server_reply sends c a line from the server itself, built in full: ":" + serverName + " " + code + " " + target + " :" + text, where target is c.nick, or "*" before c has a nick.
// reply is rendered by graft; its body is a convergence region (runningReplyBody).
export function Server_reply(s: Server, c: Client, code: string, text: string) {
  running_synth.runningReplyBody(s, c, code, text);
}

// /sedum:setFunction
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"Client_prefix"},"kwargs":"sha256:167ea728eafe9418aaf1cd965b578ea807f1235842e98890a6f0eddb1856b124","body":"sha256:01675427cd75d9b0095c55cfc635efccbc4956f6b6de3b25219f6d0637397abb"}
// Client_prefix returns c's prefix, nick!user@freeq-lite, WITHOUT the leading ':'. Every sender of a prefixed line adds the ':' itself (":" + Client_prefix(c) + " JOIN ..."), including QUIT.
// prefix is rendered by graft; its body is a convergence region (runningPrefixBody).
export function Client_prefix(c: Client): string {
  return running_synth.runningPrefixBody(c);
}

// /sedum:setFunction
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-4f5ccee6","gen":"sha256:250c5657476f47898442dcc6ff2868dff54bada34c32e3fd418930251fd3758f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"running.ts","name":"Server_disconnect"},"kwargs":"sha256:777ac14de5314bdfcb3403368942c838953a366475e4d569a3b0856e684fa742","body":"sha256:d9dd2ebbdc0ed6353bfa0bbf0897e182ac0072fdd280dbcff77582e05522e09b"}
// Server_disconnect removes c from the server and from every channel, tells the channel peers ":" + Client_prefix(c) + " QUIT :" + reason (once each), then closes c's socket.
// disconnect is rendered by graft; its body is a convergence region (runningDisconnectBody).
export function Server_disconnect(s: Server, c: Client, reason: string) {
  running_synth.runningDisconnectBody(s, c, reason);
}

// /sedum:setFunction
