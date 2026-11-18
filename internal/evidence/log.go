package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrBrokenChain: the log or its head is damaged (an edited, reordered or
	// missing committed entry, or a head that does not match). See SPEC §7 for
	// what this does and does not detect.
	ErrBrokenChain = errors.New("evidence: hash chain broken")
	// ErrTimeBackwards: an entry's At is earlier than the previous entry's.
	ErrTimeBackwards = errors.New("evidence: At is earlier than the previous entry")
)

// head is the sidecar <path>.head pinning the committed prefix of the log.
// It is a LOWER BOUND: a writer fsyncs the entry, then advances the head, so
// a crash in between leaves extra entries that chain from head.Last. Those
// are accepted and the next writer rolls the head forward.
type head struct {
	V     int    `json:"v"`
	Count int    `json:"count"`
	Last  string `json:"last"`
}

// snapshot is a consistent read of log and head, taken under a lock.
type snapshot struct {
	entries  []Evidence
	head     head
	hasHead  bool
	partial  int64 // byte offset of a trailing partial line; -1 if none
	fileSize int64
}

// Log is an append-only JSONL evidence log. Writers hold an exclusive flock
// on the log file; readers hold a shared one and read log and head together.
type Log struct {
	mu      sync.Mutex
	path    string
	entries []Evidence
	now     func() time.Time
}

// Open loads and verifies the log at path. A missing log is an empty log.
func Open(path string) (*Log, error) {
	l := &Log{path: path, now: time.Now}
	if err := l.Verify(); err != nil {
		return nil, err
	}
	return l, nil
}

// OpenUnverified loads the log without checking it, for inspection and repair.
// Nothing should evaluate policy on a log opened this way.
func OpenUnverified(path string) (*Log, error) {
	l := &Log{path: path, now: time.Now}
	s, err := l.read(lockShared)
	if err != nil {
		return nil, err
	}
	l.entries = s.entries
	return l, nil
}

// Verify re-reads log and head under a shared lock and checks them: every ID,
// every Prev link, time order, and that the head is a committed prefix.
func (l *Log) Verify() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, err := l.read(lockShared)
	if err != nil {
		return err
	}
	if err := check(s); err != nil {
		return err
	}
	l.entries = s.entries
	return nil
}

// read takes the given lock on the log file and reads log and head together.
func (l *Log) read(how lockKind) (snapshot, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		// No log file, so nothing to lock. A head without a log could only
		// come from a writer that created the log after our open failed: in
		// that case the log exists now, so read it properly under the lock.
		h, ok, herr := readHead(l.path)
		if herr != nil || !ok || h.Count == 0 {
			return snapshot{head: h, hasHead: ok, partial: -1}, herr
		}
		if f, err = os.Open(l.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return snapshot{}, fmt.Errorf("%w: head commits %d entries but there is no log", ErrBrokenChain, h.Count)
			}
			return snapshot{}, err
		}
	} else if err != nil {
		return snapshot{}, err
	}
	defer f.Close()
	if err := lockFile(f, how); err != nil {
		return snapshot{}, err
	}
	defer unlockFile(f)
	return readLocked(l.path, f)
}

func readLocked(path string, f *os.File) (snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return snapshot{}, err
	}
	s := snapshot{partial: -1, fileSize: int64(len(b))}
	if i := bytes.LastIndexByte(b, '\n'); len(b) > 0 && i != len(b)-1 {
		s.partial = int64(i + 1) // an unfinished final write
		b = b[:i+1]
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Evidence
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return snapshot{}, fmt.Errorf("%w: %s line %d: %v", ErrBrokenChain, path, n, err)
		}
		s.entries = append(s.entries, e)
	}
	if err := sc.Err(); err != nil {
		return snapshot{}, err
	}
	s.head, s.hasHead, err = readHead(path)
	return s, err
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

// check verifies a snapshot. Entries past the head are accepted when they
// chain correctly (a crash after the entry's fsync, before the head moved).
// A trailing partial line is ignored by readers; the next writer truncates it.
func check(s snapshot) error {
	prev := ""
	var prevAt time.Time
	for i, e := range s.entries {
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
	// A missing head is a head at zero: a crash after the first entry's fsync
	// but before the first head write leaves exactly that, and the head is a
	// lower bound. (Deleting the head deliberately is out of scope: §7.)
	switch {
	case s.hasHead && s.head.Count > len(s.entries):
		return fmt.Errorf("%w: head commits %d entries, log has %d (truncated)", ErrBrokenChain, s.head.Count, len(s.entries))
	case s.hasHead && s.head.Count > 0 && s.entries[s.head.Count-1].ID != s.head.Last:
		return fmt.Errorf("%w: entry %d is not the committed head %.12s", ErrBrokenChain, s.head.Count-1, s.head.Last)
	case s.hasHead && s.head.Count == 0 && s.head.Last != "":
		return fmt.Errorf("%w: empty head names an entry", ErrBrokenChain)
	}
	return nil
}

// writeHead replaces the head atomically and durably (file, then directory).
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
	if err := os.Rename(tmp, path+".head"); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Append chains e onto the log and returns the stored entry. Under an
// exclusive lock it re-reads and verifies log and head, truncates a trailing
// partial line, rolls the head forward over any committed-but-unheaded
// entries, then writes, fsyncs, and advances the head.
func (l *Log) Append(e Evidence) (Evidence, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Kind == "" || e.Verdict == "" || e.Pin.RecordID == "" {
		return Evidence{}, errors.New("evidence: kind, verdict and pin.record_id are required")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return Evidence{}, err
	}
	defer f.Close()
	if err := lockFile(f, lockExclusive); err != nil {
		return Evidence{}, err
	}
	defer unlockFile(f)

	s, err := readLocked(l.path, f)
	if err != nil {
		return Evidence{}, err
	}
	if err := check(s); err != nil {
		return Evidence{}, err
	}
	if s.partial >= 0 {
		if err := f.Truncate(s.partial); err != nil {
			return Evidence{}, err
		}
	}
	current := s.entries

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
	if _, err := f.Seek(0, 2); err != nil {
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

// Path is the log's file path.
func (l *Log) Path() string { return l.path }

// All returns a copy of the entries as of the last Verify or Append.
func (l *Log) All() []Evidence {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Evidence(nil), l.entries...)
}

// Fresh returns the loaded entries that still speak for cur, oldest first.
func (l *Log) Fresh(cur Pin) []Evidence {
	var out []Evidence
	for _, e := range l.All() {
		if !e.Stale(cur) {
			out = append(out, e)
		}
	}
	return out
}
