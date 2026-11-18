# graft:synth:begin {"record":"prov-2026-f25581c4","version":1,"gen":"sha256:5087793a7755623af61380d764d6cb3fc99b90408fc185521254f0be21f9a865","tier":"low","body":"2eed043e7a0168214312ca646256204d1947cb2d30af730dfc32164f440a743a"}
import re

from running import HANDLERS

REGISTRATION_NICK_RE = re.compile(r"[A-Za-z][A-Za-z0-9_-]{0,15}")


def registration_try_welcome(server, client):
    # Registered once both NICK and USER have been received.
    if client.registered or client.nick is None or client.user is None:
        return
    client.registered = True
    server.reply(client, "001", ":Welcome to freeq-lite %s" % client.nick)


def registration_nick(server, client, msg):
    if not msg.params:
        server.reply(client, "431", ":No nickname given")
        return
    new = msg.params[0]
    if not REGISTRATION_NICK_RE.fullmatch(new):
        client.send(":freeq-lite 432 * %s :Erroneous nickname" % new)
        return
    key = new.lower()
    owner = server.clients.get(key)
    if owner is not None and owner is not client:
        client.send(":freeq-lite 433 * %s :Nickname is already in use" % new)
        return
    old_prefix = client.prefix() if client.registered else None
    if client.nick is not None:
        old_key = client.nick.lower()
        if server.clients.get(old_key) is client:
            del server.clients[old_key]
    client.nick = new
    server.clients[key] = client
    if client.registered:
        line = ":%s NICK %s" % (old_prefix, new)
        targets = {client}
        for ckey in client.channels:
            chan = server.channels.get(ckey)
            if chan is not None:
                targets.update(chan.members)
        for target in targets:
            target.send(line)
    else:
        registration_try_welcome(server, client)


def registration_user(server, client, msg):
    if client.registered:
        server.reply(client, "462", ":You may not reregister")
        return
    if len(msg.params) < 4:
        server.reply(client, "461", "USER :Not enough parameters")
        return
    client.user = msg.params[0]
    registration_try_welcome(server, client)


# The 451 rule for unregistered clients is applied in running.py's dispatch.
HANDLERS["NICK"] = registration_nick
HANDLERS["USER"] = registration_user
# graft:synth:end {"record":"prov-2026-f25581c4"}
