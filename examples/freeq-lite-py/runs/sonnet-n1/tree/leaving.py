# graft:synth:begin {"record":"prov-2026-4532bb39","version":1,"gen":"sha256:d689c2248313666033d2d4c833c75fc7854491dddd89234b47b5e0fa615d0dfd","tier":"low","body":"43fa7fb881476aa2cf1139fec9087c70192bf0ecef1b8067105feb5b76a43498"}
from running import HANDLERS


def leaving_quit(server, client, msg):
    reason = msg.params[0] if msg.params else "Client quit"
    client.send("ERROR :Closing link")
    server.disconnect(client, reason)


# A dropped connection ("Connection closed") is handled in running.py's read loop.
HANDLERS["QUIT"] = leaving_quit
# graft:synth:end {"record":"prov-2026-4532bb39"}
