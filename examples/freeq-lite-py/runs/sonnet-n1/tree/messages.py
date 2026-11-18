# graft:synth:begin {"record":"prov-2026-6a0ab63a","version":1,"gen":"sha256:8016c4199acd0fb1d9d3c8e469bbf0eac436bdd3d43cb5ffc1d05ee8b4e79b56","tier":"low","body":"2ad985446e88ee1f7a4aff285c3b6de69babeaf9ce0097d4edea7ec580ed54fc"}
from running import HANDLERS


def messages_privmsg(server, client, msg):
    if not msg.params:
        server.reply(client, "411", ":No recipient given (PRIVMSG)")
        return
    target = msg.params[0]
    if len(msg.params) < 2 or msg.params[1] == "":
        server.reply(client, "412", ":No text to send")
        return
    text = msg.params[1]
    if target.startswith("#"):
        chan = server.channels.get(target.lower())
        if chan is None:
            server.reply(client, "401", "%s :No such nick/channel" % target)
            return
        if client not in chan.members:
            server.reply(client, "404", "%s :Cannot send to channel" % target)
            return
        line = ":%s PRIVMSG %s :%s" % (client.prefix(), chan.name, text)
        for member in chan.members:
            if member is not client:
                member.send(line)
        return
    dest = server.clients.get(target.lower())
    if dest is None or not dest.registered:
        server.reply(client, "401", "%s :No such nick/channel" % target)
        return
    if dest is client:
        return
    dest.send(":%s PRIVMSG %s :%s" % (client.prefix(), dest.nick, text))


HANDLERS["PRIVMSG"] = messages_privmsg
# graft:synth:end {"record":"prov-2026-6a0ab63a"}
