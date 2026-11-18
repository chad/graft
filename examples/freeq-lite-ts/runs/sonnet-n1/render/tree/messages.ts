// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-f410ebe8","gen":"sha256:e978822bd34dbcac243ce68fa2209b275830d4b3d3b32e5cafff1ffd2ea0e7bd","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"messages.ts","path":"./messages_synth.ts"},"kwargs":"sha256:69b737b066a1837e738f4f77055dbfeef294ff39b6464b6b38e50f36f8088973","body":"sha256:efad252291399da767ac71181d61d1ed27c76ed382e1094ac0d05316004b2ef1"}
import * as messages_synth from "./messages_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-f410ebe8","gen":"sha256:e978822bd34dbcac243ce68fa2209b275830d4b3d3b32e5cafff1ffd2ea0e7bd","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"messages.ts","name":"init"},"kwargs":"sha256:600117e682736dba22e80ca367a4d5149cb6928e7c6588c9bad752916db43d52","body":"sha256:e4cb9672a95ad6b2aa754c9be4a285c023a6487720f527f97d0548f18a6cef57"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (messagesInitBody).
export function init() {
  messages_synth.messagesInitBody();
}

// /sedum:setFunction
