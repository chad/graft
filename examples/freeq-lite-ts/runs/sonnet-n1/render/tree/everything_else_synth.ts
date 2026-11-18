// graft:synth:begin {"record":"prov-2026-0aaad2ff","version":1,"gen":"sha256:c45008cb6427a53a72c2df88232b1c35e7ffa1bb1a073ee8d7c0220fa9bd2d2f","tier":"low","body":"df314718772617e647d83806b600bd623d6e2f0865ba006b450ab61cb6ec60ec"}
// The 421 reply for an unknown command after registration is already sent by
// running.ts's read loop (runningDispatch), with the command exactly as the
// client sent it. This module therefore has no command of its own to register.
export function everything_elseInitBody() {
  // Intentionally registers nothing: unknown commands are handled in running.ts.
}
// graft:synth:end {"record":"prov-2026-0aaad2ff"}
