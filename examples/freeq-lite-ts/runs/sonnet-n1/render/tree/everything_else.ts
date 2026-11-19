// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-0aaad2ff","gen":"sha256:c45008cb6427a53a72c2df88232b1c35e7ffa1bb1a073ee8d7c0220fa9bd2d2f","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"everything_else.ts","path":"./everything_else_synth.ts"},"kwargs":"sha256:d46183bc72917960a44fa93f9b11cafd8f735e08cc0593d59e0e85fddcc417f1","body":"sha256:9b5fbe8d59fc601a1cf5c617195562c4f34746cfd1bad6057a997625686680dc"}
import * as everything_else_synth from "./everything_else_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-0aaad2ff","gen":"sha256:c45008cb6427a53a72c2df88232b1c35e7ffa1bb1a073ee8d7c0220fa9bd2d2f","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"everything_else.ts","name":"init"},"kwargs":"sha256:7b2bfcb02661d75f2ea8c2702f9060319caa038e725ce4c8f1c7001aa87e223a","body":"sha256:075309e89f1a040acf03b486cd2003ded78c4c9d140cec4def8deab897286f6f"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (everything_elseInitBody).
export function init() {
  everything_else_synth.everything_elseInitBody();
}

// /sedum:setFunction
