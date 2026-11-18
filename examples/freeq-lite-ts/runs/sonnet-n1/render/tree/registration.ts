// sedum:anchor:imports
// sedum:addImport {"v":2,"tier":"owned","record":"prov-2026-32994e4a","gen":"sha256:3aa4bbe0448cf0ecf514bb17d3274761dc4354a163009acbd374a6be7b2fabdf","pkg":"sha256:6d22ff450530c1850916c12d78c1cb125d4025813882048545ea4a92f96569ea","id":{"file":"registration.ts","path":"./registration_synth.ts"},"kwargs":"sha256:ba50d42f005c45843b28c044338c8ba9081e56680241c59043da33138e264bdf","body":"sha256:e01caef4c38efee5b1bbdef1e89189b43d51ef91cad46d8e000727b08a05d1cd"}
import * as registration_synth from "./registration_synth.ts";

// /sedum:addImport

// sedum:anchor:decls
// sedum:setFunction {"v":2,"tier":"owned","record":"prov-2026-32994e4a","gen":"sha256:3aa4bbe0448cf0ecf514bb17d3274761dc4354a163009acbd374a6be7b2fabdf","pkg":"sha256:6589611adfb6618a7920e43f951a79901428192ea95c1084fde9f222aecd9537","id":{"file":"registration.ts","name":"init"},"kwargs":"sha256:49b1d5ddbea31c799041f8852075c79f18c40a8911886193dbc89966e893a8c5","body":"sha256:543d8b0d38d1405770f9c59f62bef9aa8524e36af91420ae211984dfb4c04f28"}
// init registers this module's commands: handlers.set("CMD", fn) for each, with handlers imported by that name from "./running.ts". It runs once at startup.
// init is rendered by graft; its body is a convergence region (registrationInitBody).
export function init() {
  registration_synth.registrationInitBody();
}

// /sedum:setFunction
