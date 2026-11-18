// graft:synth:begin {"record":"prov-2026-7f6e24bb","version":1,"gen":"sha256:c45008cb6427a53a72c2df88232b1c35e7ffa1bb1a073ee8d7c0220fa9bd2d2f","tier":"low","body":"dc333fcae0c8c602d1ccd288a42c912e50a5899706e929a7104705e043d55642"}
// Unknown commands are answered by running.ts's read loop
// (":freeq-lite 421 <nick> <command> :Unknown command", the command exactly
// as the client sent it), so this module registers no handlers.
export function init(): void {
  // Nothing to register: the 421 rule is applied in running.ts.
}
// graft:synth:end {"record":"prov-2026-7f6e24bb"}
