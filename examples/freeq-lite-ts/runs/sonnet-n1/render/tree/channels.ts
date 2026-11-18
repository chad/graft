// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-e821bd18","gen":"sha256:d4c1df0bcfd7bf6b70d0d6f6d1a0e4ddb2921e04c6e7cd94a7f5daf8cdc3de54","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"channels.ts","path":"./channels_synth.ts"},"kwargs":"sha256:2ea52f28c4409ad14c83f4c0d792c2204dd2577982f4003969b65f74f582267e","body":"sha256:31b3b067fe5bfb76aadd04987136995513fdbbf298639f4c84026706956f1377"}
import * as channels_synth from "./channels_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-e821bd18","gen":"sha256:d4c1df0bcfd7bf6b70d0d6f6d1a0e4ddb2921e04c6e7cd94a7f5daf8cdc3de54","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"channels.ts","name":"init"},"kwargs":"sha256:fd41925b1c747ff730f61e87fd0d48fb47baf6e0b9bff28d4236efea358ff6dd","body":"sha256:aa16f5baed1db2f25a33691e7a159de5dc61a6df172d6fe7b9f42f8d7f7af557"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (channelsInitBody).
export function init() {
  channels_synth.channelsInitBody();
}

// /sedum:setFunction
