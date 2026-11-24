// Command reference is the v2 hand-written ledger-lite server (spec-v2: a frozen
// account also refuses incoming money, and journal entries carry the balance
// after the change). reference/ is v1. The -v2 oracle must score this 100%, so
// a failing check on it is a bug in the oracle, not here. Never shown to a model.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type account struct {
	ID      string `json:"id"`
	Balance int64  `json:"balance"`
	Held    int64  `json:"held"`
	Avail   int64  `json:"available"`
	Frozen  bool   `json:"frozen"`
}

type transfer struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"`
	key    string
}

type hold struct {
	ID      string `json:"id"`
	Account string `json:"account"`
	Amount  int64  `json:"amount"`
}

type entry struct {
	Seq     int    `json:"seq"`
	Account string `json:"account"`
	Delta   int64  `json:"delta"`
	Balance int64  `json:"balance"`
	Kind    string `json:"kind"`
}

type ledger struct {
	mu        sync.Mutex
	accounts  map[string]*account
	transfers map[string]*transfer
	byKey     map[string]*transfer
	holds     map[string]*hold
	nextT     int
	nextH     int
	journal   []entry
}

var idRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

type apiErr struct {
	status int
	msg    string
}

func (e apiErr) Error() string { return e.msg }

var (
	errNoAccount = apiErr{404, "no such account"}
	errFunds     = apiErr{409, "insufficient funds"}
	errFrozen    = apiErr{423, "account frozen"}
	errAmount    = apiErr{400, "invalid amount"}
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var e apiErr
	if !errors.As(err, &e) {
		e = apiErr{500, err.Error()}
	}
	writeJSON(w, e.status, map[string]string{"error": e.msg})
}

// body decodes the request body into a generic map; invalid JSON is an error.
func body(r *http.Request) (map[string]any, error) {
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m == nil {
		return nil, apiErr{400, "invalid json"}
	}
	return m, nil
}

// amount reads an integer amount in 1..100000000.
func amount(m map[string]any) (int64, error) {
	f, ok := m["amount"].(float64)
	if !ok || f != float64(int64(f)) || f < 1 || f > 100000000 {
		return 0, errAmount
	}
	return int64(f), nil
}

func (l *ledger) view(a *account) account {
	v := *a
	v.Avail = a.Balance - a.Held
	return v
}

func (l *ledger) get(id string) (*account, error) {
	a, ok := l.accounts[id]
	if !ok {
		return nil, errNoAccount
	}
	return a, nil
}

func (l *ledger) post(a *account, delta int64, kind string) {
	a.Balance += delta
	l.journal = append(l.journal, entry{Seq: len(l.journal) + 1, Account: a.ID, Delta: delta, Balance: a.Balance, Kind: kind})
}

func (l *ledger) debitable(a *account, n int64) error {
	if a.Frozen {
		return errFrozen
	}
	if a.Balance-a.Held < n {
		return errFunds
	}
	return nil
}

// creditable is the rule for putting money into an account (v2): a frozen
// account refuses it.
func (l *ledger) creditable(a *account) error {
	if a.Frozen {
		return errFrozen
	}
	return nil
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	l := &ledger{accounts: map[string]*account{}, transfers: map[string]*transfer{}, byKey: map[string]*transfer{}, holds: map[string]*hold{}}
	log.Fatal(http.ListenAndServe(*addr, l))
}

// ServeHTTP routes by hand, so an undefined method on a known path is 404
// (the spec), not net/http's 405.
func (l *ledger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	l.mu.Lock()
	defer l.mu.Unlock()
	var (
		status = 200
		out    any
		err    error
	)
	switch {
	case r.Method == "POST" && len(p) == 1 && p[0] == "accounts":
		status = 201
		out, err = l.create(r)
	case r.Method == "GET" && len(p) == 2 && p[0] == "accounts":
		out, err = l.show(p[1])
	case r.Method == "POST" && len(p) == 3 && p[0] == "accounts" && (p[2] == "deposit" || p[2] == "withdraw"):
		out, err = l.move(r, p[1], p[2])
	case r.Method == "POST" && len(p) == 1 && p[0] == "transfers":
		status, out, err = l.transfer(r)
	case r.Method == "GET" && len(p) == 2 && p[0] == "transfers":
		if t, ok := l.transfers[p[1]]; ok {
			out = t
		} else {
			err = apiErr{404, "no such transfer"}
		}
	case r.Method == "POST" && len(p) == 3 && p[0] == "accounts" && p[2] == "holds":
		status = 201
		out, err = l.hold(r, p[1])
	case r.Method == "POST" && len(p) == 3 && p[0] == "holds" && (p[2] == "capture" || p[2] == "release"):
		out, err = l.settle(p[1], p[2])
	case r.Method == "GET" && len(p) == 1 && p[0] == "journal":
		out = map[string]any{"entries": nonNil(l.journal)}
	case r.Method == "GET" && len(p) == 3 && p[0] == "accounts" && p[2] == "journal":
		if _, err = l.get(p[1]); err == nil {
			var es []entry
			for _, e := range l.journal {
				if e.Account == p[1] {
					es = append(es, e)
				}
			}
			out = map[string]any{"entries": nonNil(es)}
		}
	case r.Method == "POST" && len(p) == 3 && p[0] == "accounts" && (p[2] == "freeze" || p[2] == "unfreeze"):
		var a *account
		if a, err = l.get(p[1]); err == nil {
			a.Frozen = p[2] == "freeze"
			out = l.view(a)
		}
	default:
		err = apiErr{404, "not found"}
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, status, out)
}

func nonNil(es []entry) []entry {
	if es == nil {
		return []entry{}
	}
	return es
}

func (l *ledger) create(r *http.Request) (any, error) {
	m, err := body(r)
	if err != nil {
		return nil, err
	}
	id, _ := m["id"].(string)
	if !idRE.MatchString(id) {
		return nil, apiErr{400, "invalid id"}
	}
	if _, ok := l.accounts[id]; ok {
		return nil, apiErr{409, "account exists"}
	}
	a := &account{ID: id}
	l.accounts[id] = a
	return l.view(a), nil
}

func (l *ledger) show(id string) (any, error) {
	a, err := l.get(id)
	if err != nil {
		return nil, err
	}
	return l.view(a), nil
}

func (l *ledger) move(r *http.Request, id, op string) (any, error) {
	a, err := l.get(id)
	if err != nil {
		return nil, err
	}
	m, err := body(r)
	if err != nil {
		return nil, err
	}
	n, err := amount(m)
	if err != nil {
		return nil, err
	}
	if op == "deposit" {
		if err := l.creditable(a); err != nil {
			return nil, err
		}
		l.post(a, n, "deposit")
		return l.view(a), nil
	}
	if err := l.debitable(a, n); err != nil {
		return nil, err
	}
	l.post(a, -n, "withdraw")
	return l.view(a), nil
}

func (l *ledger) transfer(r *http.Request) (int, any, error) {
	m, err := body(r)
	if err != nil {
		return 0, nil, err
	}
	from, _ := m["from"].(string)
	to, _ := m["to"].(string)
	key, _ := m["key"].(string)
	n, err := amount(m)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		if t, ok := l.byKey[key]; ok {
			if t.From == from && t.To == to && t.Amount == n {
				return 200, t, nil
			}
			return 0, nil, apiErr{409, "key reused"}
		}
	}
	if from == to {
		return 0, nil, apiErr{400, "same account"}
	}
	a, err := l.get(from)
	if err != nil {
		return 0, nil, err
	}
	b, err := l.get(to)
	if err != nil {
		return 0, nil, err
	}
	if err := l.debitable(a, n); err != nil {
		return 0, nil, err
	}
	if err := l.creditable(b); err != nil {
		return 0, nil, err
	}
	l.nextT++
	t := &transfer{ID: "t" + strconv.Itoa(l.nextT), From: from, To: to, Amount: n, key: key}
	l.transfers[t.ID] = t
	if key != "" {
		l.byKey[key] = t
	}
	l.post(a, -n, "transfer")
	l.post(b, n, "transfer")
	return 201, t, nil
}

func (l *ledger) hold(r *http.Request, id string) (any, error) {
	a, err := l.get(id)
	if err != nil {
		return nil, err
	}
	m, err := body(r)
	if err != nil {
		return nil, err
	}
	n, err := amount(m)
	if err != nil {
		return nil, err
	}
	if err := l.debitable(a, n); err != nil {
		return nil, err
	}
	l.nextH++
	h := &hold{ID: "h" + strconv.Itoa(l.nextH), Account: id, Amount: n}
	l.holds[h.ID] = h
	a.Held += n
	return h, nil
}

func (l *ledger) settle(id, op string) (any, error) {
	h, ok := l.holds[id]
	if !ok {
		return nil, apiErr{404, "no such hold"}
	}
	delete(l.holds, id)
	a := l.accounts[h.Account]
	a.Held -= h.Amount
	if op == "capture" {
		l.post(a, -h.Amount, "capture")
	}
	return l.view(a), nil
}
