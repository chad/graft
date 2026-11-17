package evidence

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// ErrBrokenChain is returned by Verify when the log has been altered.
var ErrBrokenChain = errors.New("evidence: hash chain broken")

// Log is an append-only JSONL evidence log.
type Log struct {
	mu      sync.Mutex
	path    string
	entries []Evidence
	now     func() time.Time
}

// Open opens (or creates on first Append) the log at path and loads it.
func Open(path string) (*Log, error) {
	l := &Log{path: path, now: time.Now}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Evidence
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("evidence: %s line %d: %w", path, n, err)
		}
		l.entries = append(l.entries, e)
	}
	return l, sc.Err()
}

// Append chains e onto the log, sets Prev, At (if zero) and ID, writes it,
// and returns the stored entry.
func (l *Log) Append(e Evidence) (Evidence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Kind == "" || e.Verdict == "" || e.Pin.RecordID == "" {
		return Evidence{}, errors.New("evidence: kind, verdict and pin.record_id are required")
	}
	if e.At.IsZero() {
		e.At = l.now()
	}
	e.At = e.At.UTC()
	e.Prev = ""
	if n := len(l.entries); n > 0 {
		e.Prev = l.entries[n-1].ID
	}
	id, err := computeID(e)
	if err != nil {
		return Evidence{}, err
	}
	e.ID = id
	b, err := json.Marshal(e)
	if err != nil {
		return Evidence{}, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Evidence{}, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return Evidence{}, err
	}
	if err := f.Close(); err != nil {
		return Evidence{}, err
	}
	l.entries = append(l.entries, e)
	return e, nil
}

// All returns a copy of every entry, oldest first.
func (l *Log) All() []Evidence {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Evidence(nil), l.entries...)
}

// Verify recomputes every ID and checks every Prev link.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := ""
	for i, e := range l.entries {
		if e.Prev != prev {
			return fmt.Errorf("%w: entry %d prev %q, want %q", ErrBrokenChain, i, e.Prev, prev)
		}
		id, err := computeID(e)
		if err != nil {
			return err
		}
		if id != e.ID {
			return fmt.Errorf("%w: entry %d content does not match its id", ErrBrokenChain, i)
		}
		prev = e.ID
	}
	return nil
}

// Fresh returns the entries that still speak for cur, oldest first.
func (l *Log) Fresh(cur Pin) []Evidence {
	var out []Evidence
	for _, e := range l.All() {
		if !e.Stale(cur) {
			out = append(out, e)
		}
	}
	return out
}
