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
	beginTag = "graft:synth:begin "
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

// ErrUnterminated: a begin marker with no end marker. Never dropped silently.
var ErrUnterminated = errors.New("converge: synthesis region has no end marker")

// HashBody is the hash a marker stores for a region body.
func HashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func splitLines(b []byte) []string {
	s := string(b)
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")
}

func parse(rel string, data []byte) ([]Region, error) {
	lines := splitLines(data)
	var out []Region
	for i := 0; i < len(lines); i++ {
		at := strings.Index(lines[i], beginTag)
		if at < 0 {
			continue
		}
		var o Owner
		if err := json.Unmarshal([]byte(strings.TrimSpace(lines[i][at+len(beginTag):])), &o); err != nil {
			return nil, fmt.Errorf("converge: %s line %d: bad marker: %w", rel, i+1, err)
		}
		j := i + 1
		for ; j < len(lines) && !strings.Contains(lines[j], endTag); j++ {
			if strings.Contains(lines[j], beginTag) {
				return nil, fmt.Errorf("%w: %s line %d (nested begin at line %d)", ErrUnterminated, rel, i+1, j+1)
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
// then position. .git is skipped.
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
		b, err := os.ReadFile(p)
		if err != nil || !bytes.Contains(b, []byte(beginTag)) {
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

// write puts body into the region owned by o.Record in file path (relative to
// root), creating the region at the end of the file, or the file, if needed.
// The marker records the new body hash.
func write(root, rel, comment string, o Owner, body string) error {
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	o.Body = HashBody(body)
	mk, _ := json.Marshal(o)
	begin := fmt.Sprintf("%s %s%s\n", comment, beginTag, mk)
	end := fmt.Sprintf("%s %s\n", comment, endTag)

	p := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	rs, err := parse(rel, data)
	if err != nil {
		return err
	}
	lines := splitLines(data)
	var next []string
	replaced := false
	for _, r := range rs {
		if r.Owner.Record != o.Record {
			continue
		}
		next = append(next, lines[:r.begin]...)
		next = append(next, begin, body, end)
		next = append(next, lines[r.end+1:]...)
		replaced = true
		break
	}
	if !replaced {
		next = append(lines, begin, body, end)
		if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			next = append(lines, "\n", begin, body, end)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(strings.Join(next, "")), 0o644)
}
