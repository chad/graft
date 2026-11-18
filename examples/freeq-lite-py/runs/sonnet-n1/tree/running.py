# graft:synth:begin {"record":"prov-2026-7d4a4f54","version":1,"gen":"sha256:ccf08d6336702ebee7fa6cf37919a81648d65ba8855fda5843cc7c296a34840d","tier":"low","body":"7aa77af560027d951dcb3c72829637f3b7ba50b678a0bb5fa02f253eaa1bd439"}
import argparse
import queue
import socket
import threading

SERVER_NAME = "freeq-lite"
MAX_LINE = 512

# command registry: upper-case command -> handler(server, client, msg)
HANDLERS = {}

# Commands allowed before registration.
PRE_REGISTRATION = ("NICK", "USER", "PING", "QUIT")


class Message:
    """One parsed IRC line: optional prefix, command (as sent), params."""

    def __init__(self, command, params, prefix=None):
        self.command = command
        self.params = params
        self.prefix = prefix

    @staticmethod
    def parse(line):
        """Parse a line (no line terminator); return None if it has no command."""
        prefix = None
        rest = line.lstrip(" ")
        if rest.startswith(":"):
            prefix, _, rest = rest[1:].partition(" ")
            rest = rest.lstrip(" ")
        if not rest:
            return None
        params = []
        command, _, rest = rest.partition(" ")
        while rest:
            rest = rest.lstrip(" ")
            if not rest:
                break
            if rest.startswith(":"):
                params.append(rest[1:])
                break
            word, _, rest = rest.partition(" ")
            params.append(word)
        return Message(command, params, prefix)


class Channel:
    """A channel: its display name and its member clients."""

    def __init__(self, name):
        self.name = name
        self.members = set()
        self.topic = None


class Client:
    """One connection. `channels` holds the lower-cased names of the
    channels the client is in (keys of Server.channels)."""

    def __init__(self, sock):
        self.sock = sock
        self.nick = None
        self.user = None
        self.registered = False
        self.channels = set()
        self.closed = False
        self.outq = queue.Queue()

    def send(self, line):
        """Queue one line (without CRLF) for the writer thread."""
        if not self.closed:
            self.outq.put(line)

    def prefix(self):
        return "%s!%s@%s" % (self.nick, self.user, SERVER_NAME)

    def writer(self):
        try:
            while True:
                line = self.outq.get()
                if line is None:
                    break
                self.sock.sendall((line + "\r\n").encode("utf-8", "replace"))
        except OSError:
            pass
        try:
            self.sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        try:
            self.sock.close()
        except OSError:
            pass

    def run(self, server):
        threading.Thread(target=self.writer, daemon=True).start()
        buf = bytearray()
        discarding = False
        try:
            while True:
                try:
                    data = self.sock.recv(4096)
                except OSError:
                    break
                if not data:
                    break
                buf.extend(data)
                while True:
                    idx = buf.find(b"\n")
                    if idx < 0:
                        if len(buf) > MAX_LINE + 2:
                            # Too long already: drop it until the next newline.
                            discarding = True
                            del buf[:]
                        break
                    raw = bytes(buf[:idx])
                    del buf[:idx + 1]
                    if discarding:
                        discarding = False
                        continue
                    if raw.endswith(b"\r"):
                        raw = raw[:-1]
                    if len(raw) > MAX_LINE:
                        continue
                    self.dispatch(server, raw.decode("utf-8", "replace"))
                    if self.closed:
                        return
        finally:
            with server.mu:
                server.disconnect(self, "Connection closed")

    def dispatch(self, server, line):
        msg = Message.parse(line)
        if msg is None:
            return
        cmd = msg.command.upper()
        with server.mu:
            if self.closed:
                return
            if not self.registered and cmd not in PRE_REGISTRATION:
                server.reply(self, "451", ":You have not registered")
                return
            handler = HANDLERS.get(cmd)
            if handler is None:
                server.reply(self, "421", "%s :Unknown command" % msg.command)
                return
            try:
                handler(server, self, msg)
            except Exception:
                pass


class Server:
    def __init__(self):
        self.mu = threading.Lock()
        self.clients = {}   # lower-cased nick -> Client
        self.channels = {}  # lower-cased name -> Channel

    def reply(self, client, code, text):
        client.send(":%s %s %s %s" % (SERVER_NAME, code, client.nick or "*", text))

    def disconnect(self, client, reason):
        """Leaving rules; call with mu held. Idempotent. Does not send ERROR
        (the QUIT handler does that before calling)."""
        if client.closed:
            return
        peers = set()
        for key in list(client.channels):
            chan = self.channels.get(key)
            if chan is None:
                continue
            chan.members.discard(client)
            peers.update(chan.members)
            if not chan.members:
                del self.channels[key]
        if client.registered and peers:
            line = ":%s QUIT :%s" % (client.prefix(), reason)
            for peer in peers:
                peer.send(line)
        client.channels.clear()
        if client.nick is not None:
            key = client.nick.lower()
            if self.clients.get(key) is client:
                del self.clients[key]
        client.closed = True
        client.outq.put(None)


def running_parse_addr(text):
    host, _, port = text.rpartition(":")
    return host, int(port)


def main(argv):
    parser = argparse.ArgumentParser(prog="main.py")
    parser.add_argument("-addr", default=":6667")
    args = parser.parse_args(argv)
    host, port = running_parse_addr(args.addr)
    server = Server()
    lsock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    lsock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    lsock.bind((host, port))
    lsock.listen(128)
    while True:
        try:
            conn, _ = lsock.accept()
        except OSError:
            continue
        client = Client(conn)
        threading.Thread(target=client.run, args=(server,), daemon=True).start()
# graft:synth:end {"record":"prov-2026-7d4a4f54"}
