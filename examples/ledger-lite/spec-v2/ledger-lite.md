# ledger-lite

A small in-memory ledger served over HTTP: accounts, money movements, holds, a journal of every balance change, and frozen accounts.

## Running

- The server must listen on the address given by the `-addr` flag (default `:8080`)
- All state must be kept in memory; nothing is written to disk
- Request and response bodies are JSON, and amounts are integers in cents
- Every response must have the header `Content-Type: application/json`, and every error response must have the body `{"error": "<message>"}`
- A request body that is not valid JSON must get 400 with error `invalid json`
- A path or method the spec does not define must get 404 with error `not found`
- Requests may arrive concurrently; every operation must behave as if it ran alone (no lost updates, no partial effects)

## Accounts

- `POST /accounts` with `{"id": "..."}` must create an account and answer 201 with the account view
- An account id must be 1 to 32 characters from `a-z0-9_-`; otherwise the server must answer 400 with error `invalid id`
- Creating an id that already exists must answer 409 with error `account exists`
- `GET /accounts/{id}` must answer 200 with the account view: `{"id": "...", "balance": B, "held": H, "available": A, "frozen": F}`
- `available` is always `balance - held`, and a new account has balance 0, held 0 and is not frozen
- Any request naming an account that does not exist must get 404 with error `no such account`

## Deposits and withdrawals

- `POST /accounts/{id}/deposit` with `{"amount": N}` must add N to the balance and answer 200 with the account view
- `POST /accounts/{id}/withdraw` with `{"amount": N}` must subtract N from the balance and answer 200 with the account view
- An amount must be an integer from 1 to 100000000; otherwise the server must answer 400 with error `invalid amount`
- A withdrawal larger than the available amount must answer 409 with error `insufficient funds`

## Transfers

- `POST /transfers` with `{"from": "...", "to": "...", "amount": N}` must move N from one account to the other and answer 201 with `{"id": "...", "from": "...", "to": "...", "amount": N}`
- Transfer ids are `t1`, `t2`, … in the order transfers are made
- A transfer from an account to itself must answer 400 with error `same account`
- A transfer larger than the sender's available amount must answer 409 with error `insufficient funds`
- A transfer may carry `"key": "..."`; a second transfer with the same key and the same from, to and amount must answer 200 with the first transfer and move no money, and one with the same key but anything different must answer 409 with error `key reused`
- `GET /transfers/{id}` must answer 200 with the transfer, or 404 with error `no such transfer`

## Holds

- `POST /accounts/{id}/holds` with `{"amount": N}` must reserve N of the available amount and answer 201 with `{"id": "...", "account": "...", "amount": N}`; a hold raises `held` and leaves `balance` unchanged
- Hold ids are `h1`, `h2`, … in the order holds are placed
- A hold larger than the available amount must answer 409 with error `insufficient funds`
- `POST /holds/{id}/capture` must take the held money: the balance and `held` both drop by the hold's amount; it answers 200 with the account view
- `POST /holds/{id}/release` must give the reservation back: `held` drops by the hold's amount and the balance is unchanged; it answers 200 with the account view
- A hold can be captured or released once; any later capture or release, or an unknown hold id, must answer 404 with error `no such hold`

## Journal

- Every change to an account's balance, by any operation, must append one entry to the journal: `{"seq": S, "account": "...", "delta": D, "balance": B, "kind": K}`, where `seq` counts from 1 across the whole server, `balance` is that account's balance right after the change, and `kind` is `deposit`, `withdraw`, `transfer` or `capture`
- A transfer appends two entries, the sender's (negative delta) and then the receiver's (positive delta), with consecutive seq numbers
- Anything that does not change a balance (creating an account, placing or releasing a hold, a refused or repeated request) must not append an entry
- At all times every account's balance must equal the sum of its journal deltas
- `GET /journal` must answer 200 with `{"entries": [...]}` in seq order
- `GET /accounts/{id}/journal` must answer 200 with `{"entries": [...]}` holding only that account's entries, in seq order

## Freezing

- `POST /accounts/{id}/freeze` must freeze the account and `POST /accounts/{id}/unfreeze` must unfreeze it; both answer 200 with the account view
- A frozen account must refuse every operation that would take money out of it or reserve it: withdrawals, transfers from it and new holds must answer 423 with error `account frozen`
- A frozen account must also refuse money coming in: deposits to it and transfers to it must answer 423 with error `account frozen`. Holds placed before it was frozen may still be captured or released
- A refused operation of any kind must change nothing: no balance, hold, transfer id, hold id or journal entry
