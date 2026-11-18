// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-d6f7fd9a","gen":"sha256:5920bc0fbefc3037cd277f8a23e404095d6fc496c6c9f50d782aefe90561615b","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"keepalive.ts","path":"./keepalive_synth.ts"},"kwargs":"sha256:075cb7b581cf83fb232f360782efaf8095b3bdb734b88c8292fd603f36e2f432","body":"sha256:b336d8bd7e67a9b0edb741186fc0c19492b7096f406db9f8223e4d88033f6057"}
import * as keepalive_synth from "./keepalive_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-d6f7fd9a","gen":"sha256:5920bc0fbefc3037cd277f8a23e404095d6fc496c6c9f50d782aefe90561615b","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"keepalive.ts","name":"init"},"kwargs":"sha256:07793e6290f493b000c6217afc5cef35b33485944e69cf00d4c26c6783a8a8e4","body":"sha256:c1c139970d1a8483237540a37d5730b7ad4b3531381b7beaf4b9d8ad0df9adff"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (keepaliveInitBody).
export function init() {
  keepalive_synth.keepaliveInitBody();
}

// /sedum:setFunction
