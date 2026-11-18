# freeq-lite

A small chat server speaking a subset of IRC, the protocol freeq is built on.
No federation, no identity, no media: just people, nicks and channels.

## Running

- The server is a single Go program that listens for TCP connections on the address given by its `-addr` flag, which defaults to `:6667`
- Every message is one line ending in CRLF; a line ending in a bare LF is also accepted
- A line longer than 512 bytes is ignored
- Commands are case-insensitive; `nick`, `NICK` and `Nick` are the same command
- The server's name is `freeq-lite`, and every reply the server itself sends starts with `:freeq-lite`

## Registration

- A connection registers by sending `NICK <nick>` and `USER <user> 0 * :<real name>`, in either order
- Once both have been received, the server sends `:freeq-lite 001 <nick> :Welcome to freeq-lite <nick>`
- A nick must be 1 to 16 characters, start with a letter, and contain only letters, digits, `_` and `-`
- An invalid nick is answered with `:freeq-lite 432 * <nick> :Erroneous nickname`
- Nicks are unique ignoring case; a nick already in use is answered with `:freeq-lite 433 * <nick> :Nickname is already in use`
- Before registration, any command other than `NICK`, `USER`, `PING` and `QUIT` is answered with `:freeq-lite 451 * :You have not registered`
- After registration, `NICK <new>` changes the nick: the server sends `:<old prefix> NICK <new>` to the user and to everyone who shares a channel with them, each person once
- A user's prefix is defined as `<nick>!<user>@freeq-lite`

## Keepalive

- `PING <token>` is answered with `:freeq-lite PONG freeq-lite :<token>`, before or after registration

## Channels

- A channel name must start with `#`, be 2 to 32 characters long, and contain no spaces, commas or control characters
- `JOIN <channel>` adds the user to the channel, creating it if it does not exist
- When a user joins, `:<prefix> JOIN <channel>` is sent to every member of the channel, including the user who joined
- After joining, the joiner receives `:freeq-lite 353 <nick> = <channel> :<names>` followed by `:freeq-lite 366 <nick> <channel> :End of /NAMES list`, where names are the members' nicks sorted alphabetically and separated by single spaces
- Joining a channel the user is already in must send nothing and change nothing
- An invalid channel name is answered with `:freeq-lite 403 <nick> <channel> :No such channel`
- `PART <channel>` removes the user; `:<prefix> PART <channel>` is sent to every member including the user who left
- `PART <channel> :<reason>` sends `:<prefix> PART <channel> :<reason>` instead
- Parting a channel the user is not in is answered with `:freeq-lite 442 <nick> <channel> :You're not on that channel`
- The server must forget a channel as soon as it has no members
- `NAMES <channel>` is answered with the same 353 and 366 lines a joiner receives; for a channel that does not exist, only the 366 line is sent

## Messages

- `PRIVMSG <channel> :<text>` sends `:<prefix> PRIVMSG <channel> :<text>` to every member of the channel except the sender
- Only members may send to a channel; anyone else is answered with `:freeq-lite 404 <nick> <channel> :Cannot send to channel`
- `PRIVMSG <nick> :<text>` sends `:<prefix> PRIVMSG <nick> :<text>` to that user, where `<nick>` in the delivered line is the recipient's nick as they registered it; nick matching ignores case
- The sender of a `PRIVMSG` never receives their own message back
- A message to a nick or channel that does not exist is answered with `:freeq-lite 401 <nick> <target> :No such nick/channel`
- A `PRIVMSG` with no text is answered with `:freeq-lite 412 <nick> :No text to send`

## Leaving

- `QUIT` or `QUIT :<reason>` closes the connection: the server sends `ERROR :Closing link` and then closes it
- Everyone who shared a channel with the user receives `:<prefix> QUIT :<reason>` once, with `Client quit` as the reason when none was given
- A connection that drops without `QUIT` is treated the same way, with `Connection closed` as the reason
- After a user leaves, their nick is free again and they are no longer a member of any channel

## Everything else

- After registration, an unknown command is answered with `:freeq-lite 421 <nick> <command> :Unknown command`, with the command as the client sent it
