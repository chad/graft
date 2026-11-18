# graft:synth:begin {"record":"prov-2026-7b732a05","version":1,"gen":"sha256:f6523274d7e878f425faf93b1b918f4e252eec46658819c3412fd279de6c14d9","tier":"low","body":"20d34861b1717a471a4a142c70aa2805c28e223b361f3341b74ead49a81afdfd"}
from running import HANDLERS, Channel


def channels_valid_name(name):
    if not (2 <= len(name) <= 32) or name[0] != "#":
        return False
    for ch in name:
        if ch == " " or ch == "," or ord(ch) < 0x20 or ord(ch) == 0x7F:
            return False
    return True


def channels_send_names(server, client, name, chan):
    # chan is None for a channel that does not exist: only the 366 line.
    if chan is not None:
        nicks = sorted(m.nick for m in chan.members)
        server.reply(client, "353", "= %s :%s" % (chan.name, " ".join(nicks)))
        name = chan.name
    server.reply(client, "366", "%s :End of /NAMES list" % name)


def channels_join(server, client, msg):
    if not msg.params:
        server.reply(client, "461", "JOIN :Not enough parameters")
        return
    name = msg.params[0]
    if not channels_valid_name(name):
        server.reply(client, "403", "%s :No such channel" % name)
        return
    key = name.lower()
    chan = server.channels.get(key)
    if chan is not None and client in chan.members:
        return
    if chan is None:
        chan = Channel(name)
        server.channels[key] = chan
    chan.members.add(client)
    client.channels.add(key)
    line = ":%s JOIN %s" % (client.prefix(), chan.name)
    for member in chan.members:
        member.send(line)
    channels_send_names(server, client, chan.name, chan)


def channels_part(server, client, msg):
    if not msg.params:
        server.reply(client, "461", "PART :Not enough parameters")
        return
    name = msg.params[0]
    key = name.lower()
    chan = server.channels.get(key)
    if chan is None or client not in chan.members:
        server.reply(client, "442", "%s :You're not on that channel" % name)
        return
    if len(msg.params) > 1:
        line = ":%s PART %s :%s" % (client.prefix(), chan.name, msg.params[1])
    else:
        line = ":%s PART %s" % (client.prefix(), chan.name)
    for member in chan.members:
        member.send(line)
    chan.members.discard(client)
    client.channels.discard(key)
    if not chan.members:
        del server.channels[key]


def channels_names(server, client, msg):
    if not msg.params:
        return
    name = msg.params[0]
    channels_send_names(server, client, name, server.channels.get(name.lower()))


HANDLERS["JOIN"] = channels_join
HANDLERS["PART"] = channels_part
HANDLERS["NAMES"] = channels_names
# graft:synth:end {"record":"prov-2026-7b732a05"}
