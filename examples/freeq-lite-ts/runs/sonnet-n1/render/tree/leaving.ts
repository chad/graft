// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-99ef460f","gen":"sha256:28e1afe4042aca16b415dafddb8e0ba0975da4d466ff4db2ea69d0bc43cb26f5","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"leaving.ts","path":"./leaving_synth.ts"},"kwargs":"sha256:774849cd919a18018d472ee7b4810b1f867bb47f6e2eed9238c516db6c6d9b56","body":"sha256:72ceb4cc3ac393a2ba1e4b9cb9d76abebcef337a1dd809d00a655ef4e916d5ed"}
import * as leaving_synth from "./leaving_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-99ef460f","gen":"sha256:28e1afe4042aca16b415dafddb8e0ba0975da4d466ff4db2ea69d0bc43cb26f5","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"leaving.ts","name":"init"},"kwargs":"sha256:1fd0e9c643e8b71ca5fc9ac58c65287e2cde5ee6b931ebe4ac927a93c73008d1","body":"sha256:51a746cf48a429c149c465a91ec85e086099c41ec76ad89bd14aa0525499a6c6"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (leavingInitBody).
export function init() {
  leaving_synth.leavingInitBody();
}

// /sedum:setFunction
