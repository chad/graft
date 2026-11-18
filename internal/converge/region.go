// Package converge is graft's convergence layer (SPEC.md §6): open synthesis
// in declared regions, accepted only when an execution gate passes and the
// region's risk tier is satisfied by fresh evidence.
//
// Everything here is MERELY CHECKED, never guaranteed (SPEC §2): every
// Decision says so. Synthesis regions use their own markers and never live
// inside a sedum-owned region.
package converge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	beginTag = "graft:synth:begin"
	endTag   = "graft:synth:end"
)

// Owner is what a synthesis region's marker records about the record that owns it.
type Owner struct {
	Record  string `json:"record"`
	Version int    `json:"version"`
	Gen     string `json:"gen"`
	Tier    string `json:"tier"`
	Body    string `json:"body"` // hash of the body as graft last wrote it
}

// Region is one synthesis region in a file.
type Region struct {
	Path   string // relative to the tree root
	Owner  Owner
	Actual string // hash of the bytes currently between the markers
	begin  int    // 0-based line index of the begin marker
	end    int    // 0-based line index of the end marker
}

// Edited reports a hand edit inside the region since graft last wrote it.
func (r Region) Edited() bool { return r.Actual != r.Owner.Body }

var (
	// ErrUnterminated: a begin marker with no matching end. Never dropped silently.
	ErrUnterminated = errors.New("converge: synthesis region has no matching end marker")
	// ErrBadMarker: a marker line that does not parse or is incomplete.
	ErrBadMarker = errors.New("converge: bad synthesis marker")
	// ErrDuplicateRegion: two regions in one file claim the same record.
	ErrDuplicateRegion = errors.New("converge: two synthesis regions for one record in a file")
)

// HashBody is the hash a marker stores for a region body.
func HashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.SplitAfter(string(b), "\n")
}

// markerArg returns what follows tag on line when line IS a marker line: only
// whitespace and comment punctuation may precede the tag, so a tag inside
// code (a string literal, an identifier) is not a marker. Trailing block-
// comment closers are stripped.
func markerArg(line, tag string) (string, bool) {
	at := strings.Index(line, tag)
	if at < 0 {
		return "", false
	}
	if strings.Trim(line[:at], " \t/#*;-<!") != "" {
		return "", false
	}
	rest := strings.TrimSpace(line[at+len(tag):])
	rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(rest, "*/"), "-->"))
	return rest, true
}

type endArg struct {
	Record string `json:"record"`
}

func parse(rel string, data []byte) ([]Region, error) {
	lines := splitLines(data)
	var out []Region
	seen := map[string]int{}
	for i := 0; i < len(lines); i++ {
		if arg, ok := markerArg(lines[i], endTag); ok {
			return nil, fmt.Errorf("%w: %s line %d: end marker with no begin (%s)", ErrBadMarker, rel, i+1, arg)
		}
		arg, ok := markerArg(lines[i], beginTag)
		if !ok {
			continue
		}
		var o Owner
		if err := json.Unmarshal([]byte(arg), &o); err != nil {
			return nil, fmt.Errorf("%w: %s line %d: %v", ErrBadMarker, rel, i+1, err)
		}
		if o.Record == "" || o.Body == "" || o.Tier == "" {
			return nil, fmt.Errorf("%w: %s line %d: record, tier and body are required", ErrBadMarker, rel, i+1)
		}
		if prev, dup := seen[o.Record]; dup {
			return nil, fmt.Errorf("%w: %s lines %d and %d both claim %s", ErrDuplicateRegion, rel, prev+1, i+1, o.Record)
		}
		seen[o.Record] = i
		j := i + 1
		for ; j < len(lines); j++ {
			if _, nested := markerArg(lines[j], beginTag); nested {
				return nil, fmt.Errorf("%w: %s line %d (a begin at line %d comes first)", ErrUnterminated, rel, i+1, j+1)
			}
			if earg, ok := markerArg(lines[j], endTag); ok {
				var e endArg
				if err := json.Unmarshal([]byte(earg), &e); err != nil || e.Record != o.Record {
					return nil, fmt.Errorf("%w: %s line %d: end marker does not name %s", ErrBadMarker, rel, j+1, o.Record)
				}
				break
			}
		}
		if j == len(lines) {
			return nil, fmt.Errorf("%w: %s line %d", ErrUnterminated, rel, i+1)
		}
		out = append(out, Region{Path: rel, Owner: o, Actual: HashBody(strings.Join(lines[i+1:j], "")), begin: i, end: j})
		i = j
	}
	return out, nil
}

// ReadRegions walks root and returns every synthesis region, sorted by path
// then position. .git is skipped; symlinks are not followed.
func ReadRegions(root string) ([]Region, error) {
	var out []Region
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || !bytes.Contains(b, []byte(beginTag)) && !bytes.Contains(b, []byte(endTag)) {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rs, err := parse(filepath.ToSlash(rel), b)
		out = append(out, rs...)
		return err
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func normalizeBody(body string) string {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body
}

// renderRegion returns data with body placed in the region owned by o.Record,
// creating the region at the end if there is none. The marker records the
// body hash. It never reads the file: callers pass the bytes they checked.
func renderRegion(rel string, data []byte, comment string, o Owner, body string) ([]byte, error) {
	body = normalizeBody(body)
	o.Body = HashBody(body)
	mk, _ := json.Marshal(o)
	em, _ := json.Marshal(endArg{Record: o.Record})
	begin := fmt.Sprintf("%s %s %s\n", comment, beginTag, mk)
	end := fmt.Sprintf("%s %s %s\n", comment, endTag, em)

	rs, err := parse(rel, data)
	if err != nil {
		return nil, err
	}
	lines := splitLines(data)
	for _, r := range rs {
		if r.Owner.Record != o.Record {
			continue
		}
		var next []string
		next = append(next, lines[:r.begin]...)
		next = append(next, begin, body, end)
		next = append(next, lines[r.end+1:]...)
		return []byte(strings.Join(next, "")), nil
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out + begin + body + end), nil
}
