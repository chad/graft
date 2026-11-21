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
	"strconv"
	"strings"
	"unicode"

	"github.com/chad/graft/internal/records"
)

// Region is one sedum region found under a root.
type Region struct {
	Path     string      // repo-relative file
	Owner    records.Pin // record id + version + gen_hash on the marker; Version is 0 for real v2 markers, which carry none
	BodyHash string      // hash on the marker
	Actual   string      // hash of the bytes currently between the markers
	Seeded   bool        // seeded tier: a human finishes it, graft never rewrites it
	Start    int         // 1-based line of the opening marker
	End      int         // 1-based line of the closing marker
}

// Edited reports whether the region text no longer matches the marker's hash.
// A region sedum cannot verify (see Unverifiable) is never Edited.
func (r Region) Edited() bool { return !r.Unverifiable() && r.Actual != r.BodyHash }

// Unverifiable reports whether the marker carries no body hash to compare: a
// v1 marker, a v2 marker with no body, or one with an unknown v. sedum treats
// these as unverifiable, not as failures, so BodyHash is left empty for them
// and Render neither refuses on them nor counts them as verified.
func (r Region) Unverifiable() bool { return r.BodyHash == "" }

// ErrHandEdited is returned when a re-render would overwrite a hand edit.
var ErrHandEdited = errors.New("render: owned region was hand-edited; refusing to re-render")

type attrs struct {
	Tier    string `json:"tier"`
	Record  string `json:"record"`
	Gen     string `json:"gen"`
	Body    string `json:"body"`
	Version int    `json:"version"`
}

// afterPrefix reports whether line is "<comment prefix> <keyword>..." and
// returns what follows keyword. Like sedum, a marker only counts at the start
// of the line's trimmed text, after a comment prefix; prose that merely
// mentions "sedum:" (a README, a backticked example) is not a marker. sedum
// knows each package's declared prefix; graft does not, so a prefix is any
// non-empty run of punctuation (#, //, --, <!--, /*, ;;).
func afterPrefix(line, keyword string) (string, bool) {
	t := strings.TrimSpace(line)
	i := strings.Index(t, keyword)
	if i <= 0 {
		return "", false
	}
	prefix := strings.TrimSpace(t[:i])
	if prefix == "" {
		return "", false
	}
	for _, c := range prefix {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '`' || c == '\'' || c == '"' {
			return "", false
		}
	}
	return t[i+len(keyword):], true
}

// parseOpen recognizes an opening marker line. key is "action[:variant]".
// A marker sedum cannot verify (v1, no body hash, or a foreign v) yields a
// zero attrs.Body, so the Region it produces is Unverifiable.
func parseOpen(line string) (key string, a attrs, ok bool, err error) {
	if _, isClose := parseClose(line); isClose {
		// A stray close ("// /sedum:x") is never an opener; sedum ignores it.
		return "", a, false, nil
	}
	rest, found := afterPrefix(line, " sedum:")
	if !found {
		rest, found = afterPrefix(line, "sedum:")
	}
	if !found {
		return "", a, false, nil
	}
	label, encoded, _ := strings.Cut(rest, " ")
	label = strings.TrimSpace(label)
	action, _, _ := strings.Cut(label, ":")
	if action == "" || action == "anchor" || strings.HasPrefix(label, "/") {
		return "", a, false, nil
	}
	encoded = strings.TrimSpace(encoded)
	if !strings.HasPrefix(encoded, "{") {
		// No attributes at all: sedum reads every field as absent.
		return label, a, true, nil
	}
	// A package comment suffix (e.g. " -->") may follow the object.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&raw); err != nil {
		return "", a, false, fmt.Errorf("marker %q has unreadable attributes: %w", label, err)
	}
	var v any
	if r, present := raw["v"]; present {
		if err := json.Unmarshal(r, &v); err != nil {
			return "", a, false, fmt.Errorf("marker %q has unreadable attributes: %w", label, err)
		}
		if n, isNum := v.(float64); !isNum || n != 2 {
			return label, a, true, nil // foreign: names no record, unverifiable
		}
	} else {
		return label, a, true, nil // v1: no body hash, unverifiable
	}
	if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&a); err != nil {
		return "", a, false, fmt.Errorf("marker %q has unreadable attributes: %w", label, err)
	}
	return label, a, true, nil
}

// parseClose recognizes "<comment> /sedum:<action>[:<variant>]".
func parseClose(line string) (string, bool) {
	rest, ok := afterPrefix(line, "/sedum:")
	if !ok {
		return "", false
	}
	// "//sedum:x" is an opener with prefix "//", not a close: the "/" of
	// "/sedum:" must start the prefix's last token.
	t := strings.TrimSpace(line)
	if i := strings.Index(t, "/sedum:"); i > 0 && !unicode.IsSpace(rune(t[i-1])) {
		return "", false
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", false
	}
	return f[0], true
}

func hashBody(b string) string {
	b = strings.ReplaceAll(b, "\r\n", "\n")
	sum := sha256.Sum256([]byte(b))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fenceState tracks CommonMark fenced code blocks: a run of 3+ backticks or
// tildes opens, and only a run of the same character at least as long, with
// nothing after it, closes.
type fenceState struct {
	char byte
	n    int
}

// update consumes one line and reports whether it is a fence line or inside a fence.
func (f *fenceState) update(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return f.n > 0
	}
	n := 0
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 {
		return f.n > 0
	}
	if f.n > 0 {
		if t[0] == f.char && n >= f.n && strings.TrimSpace(t[n:]) == "" {
			*f = fenceState{}
		}
		return true
	}
	if t[0] == '`' && strings.Contains(t[n:], "`") {
		return false // inline code span, not a fence
	}
	*f = fenceState{char: t[0], n: n}
	return true
}

// parseFile finds the regions in one file. Like sedum's FindRegions it fails
// on a region that is never closed or whose closing marker names a different
// action, because its extent is unknown.
func parseFile(rel string, data []byte) ([]Region, error) {
	lines := strings.SplitAfter(string(data), "\n")
	markdown := strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".markdown")
	var fence fenceState
	var out []Region
	for i := 0; i < len(lines); i++ {
		if markdown {
			// Fenced code blocks in docs show example markers; they are not regions.
			if fence.update(lines[i]) {
				continue
			}
		}
		key, a, ok, err := parseOpen(strings.TrimRight(lines[i], "\r\n"))
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", rel, err)
		}
		if !ok {
			continue
		}
		start := i + 1
		var body strings.Builder
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if k, isClose := parseClose(strings.TrimRight(lines[j], "\r\n")); isClose {
				if k != key {
					return nil, fmt.Errorf("file %s: region opened by marker %q is closed by marker %q", rel, key, k)
				}
				closed = true
				i = j
				break
			}
			body.WriteString(lines[j])
		}
		if !closed {
			return nil, fmt.Errorf("file %s: region opened by marker %q is never closed", rel, key)
		}
		r := Region{
			Path:     rel,
			Owner:    records.Pin{RecordID: a.Record, Version: a.Version, GenHash: a.Gen},
			BodyHash: a.Body,
			Actual:   hashBody(body.String()),
			Seeded:   a.Tier == "seeded",
			Start:    start,
			End:      i + 1,
		}
		out = append(out, r)
	}
	return out, nil
}

// IsMarkerLine reports whether line is a sedum open or close marker by the
// same rules ParseFile uses (anchor markers included, since sedum reads them).
func IsMarkerLine(line string) bool {
	line = strings.TrimRight(line, "\r\n")
	if _, ok := parseClose(line); ok {
		return true
	}
	if _, ok := afterPrefix(line, "sedum:"); ok {
		return true
	}
	_, ok := afterPrefix(line, " sedum:")
	return ok
}

// ParseFile returns the sedum regions in one file's bytes using the same
// per-file marker rules as ReadMarkers (rel decides Markdown fence handling).
// Unlike ReadMarkers it does not skip anything by path or content: files under
// provenance/, and files ReadMarkers would treat as binary, are parsed too.
func ParseFile(rel string, data []byte) ([]Region, error) { return parseFile(rel, data) }

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
			if rel, rerr := filepath.Rel(root, p); rerr == nil && filepath.ToSlash(rel) == "provenance" {
				// Provenance records quote example markers in prose; they are
				// never render targets. Everything else, YAML included, can hold
				// owned regions.
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
		rs, err := parseFile(filepath.ToSlash(rel), data)
		if err != nil {
			return err
		}
		out = append(out, rs...)
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

// MinSedum is the oldest sedum graft drives. sedum 0.14.0 is the first whose
// marker gen is the record's full gen_hash, shape included; an older sedum
// writes the shape-less hash, so its markers could never match graft's.
const MinSedum = "0.14.0"

// ErrSedumTooOld is returned for a sedum older than MinSedum, or one whose
// version cannot be read.
var ErrSedumTooOld = errors.New("render: unsupported sedum version")

// CheckVersion refuses a sedum version older than MinSedum. v is what
// `sedum --version` prints: a bare semver such as "0.14.0" (a leading "v" is
// tolerated).
func CheckVersion(v string) error {
	got, ok := parseSemver(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	want, _ := parseSemver(MinSedum)
	if !ok {
		return fmt.Errorf("%w: cannot read a version from %q; graft needs sedum >= %s", ErrSedumTooOld, v, MinSedum)
	}
	if got[0] != want[0] {
		if got[0] > want[0] {
			return nil
		}
	} else if got[1] != want[1] {
		if got[1] > want[1] {
			return nil
		}
	} else if got[2] >= want[2] {
		return nil
	}
	return fmt.Errorf("%w: graft needs sedum >= %s, found %s (sedum before %s hashes a record without its shape, so its markers never match graft's gen_hash); upgrade sedum", ErrSedumTooOld, MinSedum, strings.TrimSpace(v), MinSedum)
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	core := v
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Require reads s's version and refuses one older than MinSedum.
func Require(ctx context.Context, s Sedum) error {
	v, err := s.Version(ctx)
	if err != nil {
		return fmt.Errorf("render: sedum --version: %w", err)
	}
	return CheckVersion(v)
}

type execSedum struct{ bin string }

// Exec returns a Sedum backed by the binary at bin (no shell). It does not
// check the version; commands use Open.
func Exec(bin string) Sedum { return execSedum{bin: bin} }

// Open is Exec for a sedum graft is about to drive: it refuses a binary older
// than MinSedum before anything runs.
func Open(ctx context.Context, bin string) (Sedum, error) {
	s := Exec(bin)
	if err := Require(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// Bin is the path of the sedum binary, so the convergence contract can be run
// with the same one.
func (e execSedum) Bin() string { return e.bin }

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
