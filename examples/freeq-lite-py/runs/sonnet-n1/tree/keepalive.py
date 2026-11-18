# graft:synth:begin {"record":"prov-2026-9bca227f","version":1,"gen":"sha256:7f0bb6f3a2db659cfe9fad8836779e64829e21830b710be6b3b15a498b3693e7","tier":"low","body":"a060db7fb98f9931a2a3949fccb607195bafab9a913ec3167f232c08810dc914"}
from running import HANDLERS


def keepalive_ping(server, client, msg):
    token = msg.params[0] if msg.params else ""
    client.send(":freeq-lite PONG freeq-lite :%s" % token)


HANDLERS["PING"] = keepalive_ping
# graft:synth:end {"record":"prov-2026-9bca227f"}
