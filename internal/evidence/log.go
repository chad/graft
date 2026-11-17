package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

var (
	// ErrBrokenChain: the log or its head was altered (edited, reordered,
	// truncated) or is unreadable.
	ErrBrokenChain = errors.New("evidence: hash chain broken")
	// ErrTimeBackwards: an entry's At is earlier than the previous entry's.
	ErrTimeBackwards = errors.New("evidence: At is earlier than the previous entry")
)

// head is the sidecar that pins the log's tail, so deleting the newest
// entries is detected. It lives next to the log as <path>.head; commit it (or
// anchor it elsewhere) to protect it in turn.
type head struct {
	V     int    `json:"v"`
	Count int    `json:"count"`
	Last  string `json:"last"`
}

// Log is an append-only JSONL evidence log. Appends are serialized across
// processes with an exclusive file lock, and every Append re-reads the log
// from disk under that lock, so memory never drifts from disk.
type Log struct {
	mu      sync.Mutex
	path    string
	entries []Evidence
	now     func() time.Time
}

// Open loads the log at path and verifies it. A broken chain or a head that
// does not match is ErrBrokenChain. A missing log is an empty log.
func Open(path string) (*Log, error) {
	l, err := OpenUnverified(path)
	if err != nil {
		return nil, err
	}
	if err := l.Verify(); err != nil {
		return nil, err
	}
	return l, nil
}

// OpenUnverified loads the log without checking it, for inspection and repair.
// Nothing should evaluate policy on a log opened this way.
func OpenUnverified(path string) (*Log, error) {
	l := &Log{path: path, now: time.Now}
	es, err := readEntries(path)
	if err != nil {
		return nil, err
	}
	l.entries = es
	return l, nil
}

func readEntries(path string) ([]Evidence, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, fmt.Errorf("%w: %s ends in a partial line", ErrBrokenChain, path)
	}
	var es []Evidence
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Evidence
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%w: %s line %d: %v", ErrBrokenChain, path, n, err)
		}
		es = append(es, e)
	}
	return es, sc.Err()
}

func readHead(path string) (head, bool, error) {
	b, err := os.ReadFile(path + ".head")
	if errors.Is(err, os.ErrNotExist) {
		return head{}, false, nil
	}
	if err != nil {
		return head{}, false, err
	}
	var h head
	if err := json.Unmarshal(b, &h); err != nil {
		return head{}, false, fmt.Errorf("%w: head: %v", ErrBrokenChain, err)
	}
	return h, true, nil
}

// writeHead replaces the head atomically and durably.
func writeHead(path string, h head) error {
	b, _ := json.Marshal(h)
	tmp := path + ".head.tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path+".head")
}

// Append chains e onto the log and returns the stored entry. It locks the
// file, re-reads and verifies the log from disk, rejects an At earlier than
// the previous entry's, writes and fsyncs the entry, then advances the head.
func (l *Log) Append(e Evidence) (Evidence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Kind == "" || e.Verdict == "" || e.Pin.RecordID == "" {
		return Evidence{}, errors.New("evidence: kind, verdict and pin.record_id are required")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Evidence{}, err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return Evidence{}, err
	}
	defer unlockFile(f)

	current, err := readEntries(l.path)
	if err != nil {
		return Evidence{}, err
	}
	if err := verify(l.path, current); err != nil {
		return Evidence{}, err
	}
	l.entries = current

	if e.At.IsZero() {
		e.At = l.now()
	}
	e.At = e.At.UTC()
	e.V = FormatVersion
	e.Prev = ""
	if n := len(current); n > 0 {
		last := current[n-1]
		if e.At.Before(last.At) {
			return Evidence{}, fmt.Errorf("%w: %s < %s", ErrTimeBackwards, e.At, last.At)
		}
		e.Prev = last.ID
	}
	if e.ID, err = computeID(e); err != nil {
		return Evidence{}, err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return Evidence{}, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return Evidence{}, err
	}
	if err := f.Sync(); err != nil {
		return Evidence{}, err
	}
	if err := writeHead(l.path, head{V: FormatVersion, Count: len(current) + 1, Last: e.ID}); err != nil {
		return Evidence{}, err
	}
	l.entries = append(current, e)
	return e, nil
}

// All returns a copy of every loaded entry, oldest first.
func (l *Log) All() []Evidence {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Evidence(nil), l.entries...)
}

// Verify recomputes every ID, checks every Prev link and the time order, and
// checks the head: an edited, reordered or truncated log is ErrBrokenChain.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return verify(l.path, l.entries)
}

func verify(path string, es []Evidence) error {
	prev := ""
	var prevAt time.Time
	for i, e := range es {
		if e.V != FormatVersion {
			return fmt.Errorf("%w: entry %d has format v%d, want v%d", ErrBrokenChain, i, e.V, FormatVersion)
		}
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
		if e.At.Before(prevAt) {
			return fmt.Errorf("%w: entry %d: %v", ErrBrokenChain, i, ErrTimeBackwards)
		}
		prev, prevAt = e.ID, e.At
	}
	h, ok, err := readHead(path)
	if err != nil {
		return err
	}
	switch {
	case !ok && len(es) > 0:
		return fmt.Errorf("%w: log has %d entries but no head", ErrBrokenChain, len(es))
	case ok && (h.Count != len(es) || h.Last != prev):
		return fmt.Errorf("%w: head says %d entries ending %.12s, log has %d ending %.12s",
			ErrBrokenChain, h.Count, h.Last, len(es), prev)
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
