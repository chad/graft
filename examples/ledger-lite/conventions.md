ledger-lite is one Go program: `package main`, standard library only, Go 1.22. Every file is `package main` in the module root.

Architecture, so that each file can be written and compiled on its own:

- `running.go` owns the core and nothing about specific routes:
  - `func main()` parses `-addr` (default `:8080`) and serves HTTP.
  - `type Ledger` holds all state behind one `sync.Mutex` named `mu`:
    - accounts by id, as `*Account` (`ID`, `Balance`, `Held int64`, `Frozen bool`);
    - transfers by id, plus the idempotency keys;
    - holds by id;
    - the journal (`[]Entry`);
    - the next transfer and hold numbers.
  - Request routing is a registry, `var routes []Route` with `type Route struct { Method, Pattern string; Handle func(l *Ledger, w http.ResponseWriter, r *http.Request, params []string) }`. Patterns are paths in which `{}` matches one segment, for example `/accounts/{}/deposit`. The core matches method and path itself. It calls the handler with `l.mu` held and the matched segments as `params`. It answers every unmatched method or path with 404 `not found`.
  - Helpers every other file uses. They are **exported methods**, so they count as used before the sections that call them exist:
    - `(l *Ledger) WriteJSON(w, status, v)` and `(l *Ledger) WriteError(w, status, msg)`;
    - `(l *Ledger) Decode(w, r, v any) bool`, which writes 400 `invalid json` and returns false on bad JSON;
    - `(l *Ledger) Account(w, id) *Account`, which writes 404 `no such account` and returns nil when the account is missing;
    - `(l *Ledger) Amount(v any) (int64, bool)`, which validates an amount by the spec's rule;
    - `(a *Account) View() map[string]any`, the account view.
  - **The money rules live in the core**, because they span sections:
    - `(l *Ledger) Post(a *Account, delta int64, kind string)` is the **only** way any file may change a balance: it changes the balance and appends the journal entry;
    - `(l *Ledger) Debitable(a *Account, n int64) (status int, msg string)` applies every rule for taking or reserving money: frozen means 423, and more than available means 409.

    Every other file calls these and never writes `Balance` directly.
- Every other file adds its routes by appending to `routes` from an `init()` function. It may declare only unexported helpers whose names start with the file's own name (for example `transfersFind`), so files never collide.
- A refused request changes nothing: check everything first, then change state.
- Leave no function that nothing calls, because a gate rejects dead functions. If a statement in your section is already handled by the core (for example, the journal entries written by `Post`), do not re-implement it; say in a comment where it is handled.
