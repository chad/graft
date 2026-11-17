// Package render is graft's rendering layer. It invokes the sedum binary (it
// never imports it) and reads sedum's ownership markers so a hand edit inside
// an owned region is caught before a re-render would overwrite it.
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chad/graft/internal/records"
)

// Region is one sedum region found under a root.
type Region struct {
	Path     string      // repo-relative file
	Owner    records.Pin // record id + version + gen_hash on the marker
	BodyHash string      // hash on the marker
	Actual   string      // hash of the bytes currently between the markers
	Seeded   bool        // seeded tier: a human finishes it, graft never rewrites it
}

// Edited reports whether the region text no longer matches the marker's hash.
func (r Region) Edited() bool { return r.Actual != r.BodyHash }

// ErrHandEdited is returned when a re-render would overwrite a hand edit.
var ErrHandEdited = errors.New("render: owned region was hand-edited; refusing to re-render")

type attrs struct {
	V       int    `json:"v"`
	Tier    string `json:"tier"`
	Record  string `json:"record"`
	Gen     string `json:"gen"`
	Body    string `json:"body"`
	Version int    `json:"version"`
}

// parseOpen recognizes an opening marker line: "<comment> sedum:<action>[:<variant>] {json}".
// It returns the action key (action[:variant]) and attributes.
func parseOpen(line string) (key string, a attrs, ok bool) {
	i := strings.Index(line, "sedum:")
	if i < 0 || (i > 0 && line[i-1] == '/') {
		return "", a, false
	}
	rest := line[i+len("sedum:"):]
	j := strings.Index(rest, " {")
	if j < 0 {
		return "", a, false
	}
	key = rest[:j]
	obj := strings.TrimSpace(rest[j+1:])
	// A package comment suffix (e.g. " -->") may follow the object.
	dec := json.NewDecoder(strings.NewReader(obj))
	if err := dec.Decode(&a); err != nil {
		return "", a, false
	}
	return key, a, key != ""
}

// parseClose recognizes "<comment> /sedum:<action>[:<variant>]".
func parseClose(line string) (key string, ok bool) {
	i := strings.Index(line, "/sedum:")
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[i+len("/sedum:"):])
	if f := strings.Fields(rest); len(f) > 0 {
		return f[0], true
	}
	return "", false
}

func hashBody(b string) string {
	b = strings.ReplaceAll(b, "\r\n", "\n")
	sum := sha256.Sum256([]byte(b))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func parseFile(rel string, data []byte) []Region {
	lines := strings.SplitAfter(string(data), "\n")
	var out []Region
	for i := 0; i < len(lines); i++ {
		key, a, ok := parseOpen(strings.TrimRight(lines[i], "\r\n"))
		if !ok {
			continue
		}
		var body strings.Builder
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if k, ok := parseClose(strings.TrimRight(lines[j], "\r\n")); ok && k == key {
				closed = true
				i = j
				break
			}
			body.WriteString(lines[j])
		}
		if !closed {
			continue
		}
		out = append(out, Region{
			Path:     rel,
			Owner:    records.Pin{RecordID: a.Record, Version: a.Version, GenHash: a.Gen},
			BodyHash: a.Body,
			Actual:   hashBody(body.String()),
			Seeded:   a.Tier == "seeded",
		})
	}
	return out
}

// ReadMarkers walks root and returns every sedum region, sorted by Path then position.
func ReadMarkers(root string) ([]Region, error) {
	var out []Region
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Contains(data, []byte("sedum:")) || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, parseFile(filepath.ToSlash(rel), data)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Sedum is the subset of the sedum binary graft drives.
type Sedum interface {
	Version(ctx context.Context) (string, error)
	GrowExecute(ctx context.Context, dir, recording string) error
	GrowCheck(ctx context.Context, dir string) error // non-nil = drift/refusal
}

type execSedum struct{ bin string }

// Exec returns a Sedum backed by the binary at bin (no shell).
func Exec(bin string) Sedum { return execSedum{bin: bin} }

func (e execSedum) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("render: sedum %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(buf.String()))
	}
	return buf.String(), nil
}

func (e execSedum) Version(ctx context.Context) (string, error) {
	out, err := e.run(ctx, "", "--version")
	return strings.TrimSpace(out), err
}

func (e execSedum) GrowExecute(ctx context.Context, dir, recording string) error {
	if !filepath.IsAbs(recording) {
		if abs, err := filepath.Abs(recording); err == nil {
			recording = abs
		}
	}
	_, err := e.run(ctx, dir, "grow", "--execute", recording, "--generators", "generators", "--output", ".")
	return err
}

func (e execSedum) GrowCheck(ctx context.Context, dir string) error {
	_, err := e.run(ctx, dir, "grow", "--check", "--generators", "generators", "--records", "provenance", "--output", ".")
	return err
}

// Render writes nothing itself. It refuses (with ErrHandEdited, naming the
// regions) if any non-seeded region under dir is Edited(); otherwise it calls
// GrowExecute.
func Render(ctx context.Context, s Sedum, dir, recording string) error {
	regions, err := ReadMarkers(dir)
	if err != nil {
		return err
	}
	var edited []string
	for _, r := range regions {
		if !r.Seeded && r.Edited() {
			edited = append(edited, r.Path+" ("+r.Owner.RecordID+")")
		}
	}
	if len(edited) > 0 {
		return fmt.Errorf("%w: %s", ErrHandEdited, strings.Join(edited, ", "))
	}
	return s.GrowExecute(ctx, dir, recording)
}
