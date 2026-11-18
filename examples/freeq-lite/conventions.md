freeq-lite is one Go program: `package main`, standard library only, Go 1.22. Every file is `package main` in the module root.

Architecture, so that each file can be written and compiled on its own:

- `running.go` owns the core and nothing about specific commands:
  - `func main()` parses `-addr` (default `:6667`) and serves TCP.
  - `type Server` holds all state behind one `sync.Mutex` named `mu`: registered clients by lower-cased nick, and channels by lower-cased name (each channel keeps its display name and its members).
  - `type Client` per connection: `Nick`, `User`, `Registered bool`, the set of channels it is in, and an outgoing line queue written by one writer goroutine (so a slow client never blocks the server).
  - `type Message struct { Command string; Params []string }` and a parser for IRC lines (optional `:prefix`, space-separated params, a final `:trailing` param).
  - A command registry: `var handlers = map[string]func(s *Server, c *Client, m Message){}`. The read loop upper-cases the command and calls the handler with `s.mu` held. Unknown commands, the not-registered rule and line-length limits are applied here, by the spec's rules.
  - Helpers every other file uses: `(c *Client) Send(line string)`, `(s *Server) Reply(c *Client, code, text string)` producing `:freeq-lite <code> <nick-or-*> <text>`, `(c *Client) Prefix() string`, and `(s *Server) Disconnect(c *Client, reason string)` which performs the leaving rules.
- Every other file adds its commands by assigning into `handlers` from an `init()` function, and may declare only unexported helpers whose names start with the file's own name (for example `channelsValidName`), so files never collide.
- Never block while holding `s.mu`: sends go through the client's queue.
