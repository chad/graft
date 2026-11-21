package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/records"
)

const signoffUsage = `usage: graft signoff --record ID --path FILE --by NAME [flags]

Records a human sign-off: evidence of kind human_signoff, appended to the
evidence log, pinned to exactly what you are shown: the record's gen_hash (and
version), the region's body hash, the spec hash and, with --render, the
skeleton hash. Like any evidence it goes stale the moment any of those moves,
and a tier that requires human_signoff is satisfied only while it is fresh.

Everything pinned is read from --records and the tree; none of it can be set
here. graft shows the region and what is pinned, then asks for confirmation
(a prompt on a terminal; without one, --yes is required).

--by is whatever you type. It is recorded as given and is NOT authenticated:
graft does not verify who you are, and the log is not tamper-proof (SPEC §7).

The log defaults to <records>/.graft/evidence.jsonl, the log graft run reads
(not graft converge's default, which is relative to the current directory).

Render mode is detected, not chosen: when graft.json names a "skeleton" or the
records carry a shape (the tree was made by 'graft run --render'), the sign-off
pins the skeleton hash exactly as graft run does, and --spec (the spec graft
run was given) is required, because the hash covers the records that spec
resolves to. The hash is computed from those records' shapes; the skeleton
file (graft.json "skeleton", else skeleton.json beside it) is read only for its
generator name.

exit codes:
  0  recorded
  1  declined at the prompt; nothing written
  2  refused before writing: usage, config, unknown record, bad path, no
     region, hand-edited region, region not at the record's current gen_hash,
     no confirmation (stdin is not a terminal and no --yes)
  3  failed after you were asked: the record, skeleton or region changed while
     the prompt was open, or the log could not be written; nothing was written
     unless the log says otherwise
`

// stdinIsTTY reports whether r is an interactive terminal. A variable so tests
// can exercise the prompt.
var stdinIsTTY = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// signoffState is everything a sign-off pins, as read right now.
type signoffState struct {
	Pin    evidence.Pin
	Body   string
	Region converge.Region
}

// signoffInputs are the flags that decide what is read.
type signoffInputs struct {
	store   records.Store
	repo    string
	root    string
	file    string // slash path relative to root
	id      string
	render  bool
	genName string   // render: the skeleton generator's name
	ids     []string // render: the records the spec resolves to
}

// readSignoff reads the record, region and (render) skeleton hash and builds
// the pin. Every refusal is an error; nothing is written.
func readSignoff(in signoffInputs) (signoffState, error) {
	var s signoffState
	cur, err := in.store.Current(in.id)
	if err != nil {
		return s, err
	}
	regions, err := converge.ReadRegions(in.root)
	if err != nil {
		return s, err
	}
	found := false
	for _, r := range regions {
		if r.Path == in.file && r.Owner.Record == in.id {
			s.Region, found = r, true
			break
		}
	}
	if !found {
		return s, fmt.Errorf("no synthesis region for %s in %s", in.id, in.file)
	}
	if s.Region.Edited() {
		return s, fmt.Errorf("%w: %s (record %s); a sign-off must be for what graft wrote", converge.ErrHandEdited, in.file, in.id)
	}
	if s.Region.Owner.Gen != cur.GenHash {
		return s, fmt.Errorf("the region was written for gen_hash %s but %s is now at %s (v%d): re-run graft converge or graft run first, then sign off", s.Region.Owner.Gen, in.id, cur.GenHash, cur.Version)
	}
	if s.Body, err = converge.RegionBody(in.root, s.Region); err != nil {
		return s, err
	}
	s.Pin = evidence.Pin{RecordID: in.id, Version: cur.Version, GenHash: cur.GenHash,
		RegionHash: converge.RegionHash(s.Body), SpecHash: cur.SpecHash}
	if in.render {
		if s.Pin.SkeletonHash, err = pipeline.SkeletonHashOf(in.store, in.genName, in.ids); err != nil {
			return s, err
		}
	}
	return s, nil
}

// isRenderMode: the tree was made by graft run --render when graft.json names
// a skeleton or any accepted record carries a shape (the skeleton is record
// content since M11a). Plain runs write no shapes.
func isRenderMode(store records.Store, repo string, cfg convergeConfig) (bool, error) {
	if cfg.Skeleton != "" {
		return true, nil
	}
	idx, err := pipeline.LoadIndex(repo)
	if err != nil {
		return false, err
	}
	for id := range idx.Records {
		p, err := store.Current(id)
		if err != nil {
			return false, err
		}
		if len(p.Shape) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// cleanRelPath accepts only a slash-separated path inside the root.
func cleanRelPath(p string) (string, error) {
	if p == "" || strings.Contains(p, `\`) || path.IsAbs(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("--path %q must be a relative, slash-separated path inside the root", p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("--path %q escapes the root", p)
	}
	return c, nil
}

func signoffCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("signoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, signoffUsage); fs.PrintDefaults() }
	cfgPath := fs.String("config", "graft.json", "policy (shown for context) and language")
	recDir := fs.String("records", ".", "provenance repo root (records layer)")
	logFlag := fs.String("log", "", "evidence log (default <records>/.graft/evidence.jsonl, the log graft run reads)")
	root := fs.String("root", ".", "working tree")
	id := fs.String("record", "", "owning record id")
	file := fs.String("path", "", "file holding the region, relative to --root")
	by := fs.String("by", "", "who signs off; recorded as given, NOT authenticated")
	note := fs.String("note", "", "free text recorded with the sign-off")
	yes := fs.Bool("yes", false, "skip the confirmation prompt (required when stdin is not a terminal)")
	specPath := fs.String("spec", "", "the spec graft run was given; required in render mode (detected), where the skeleton hash covers the records it resolves to")
	asJSON := fs.Bool("json", false, "print the recorded evidence as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" || *file == "" || strings.TrimSpace(*by) == "" || fs.NArg() != 0 {
		fmt.Fprint(stderr, signoffUsage)
		return 2
	}
	if strings.ContainsAny(*by+*note, "\r\n\x00") || len(*by) > 200 {
		fmt.Fprintln(stderr, "graft: --by and --note must be single-line text (--by at most 200 bytes)")
		return 2
	}
	rel, err := cleanRelPath(*file)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cfg, _, err := loadConvergeConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	store, err := records.OpenDir(*recDir)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	in := signoffInputs{store: store, repo: *recDir, root: *root, file: rel, id: *id}
	if in.render, err = isRenderMode(store, *recDir, cfg); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	switch {
	case in.render && *specPath == "":
		fmt.Fprintln(stderr, "graft: refused: this is a render tree (graft.json names a skeleton, or the records carry a shape); pass --spec, the spec graft run was given, so the skeleton hash is the one graft run pins")
		return 2
	case !in.render && *specPath != "":
		fmt.Fprintln(stderr, "graft: refused: --spec only applies to a render tree, and this one is not (no skeleton in graft.json, no record has a shape)")
		return 2
	}
	if in.render {
		profile, err := lang.Resolve(cfg.Language)
		if err != nil {
			fmt.Fprintf(stderr, "graft: %v\n", err)
			return 2
		}
		sp := skeletonFile(cfg.Skeleton, *cfgPath, "")
		sb, err := os.ReadFile(sp)
		if err != nil {
			fmt.Fprintf(stderr, "graft: skeleton: %v (set \"skeleton\" in graft.json; its generator name is part of the hash)\n", err)
			return 2
		}
		sk, err := recording.LoadSkeleton(sb)
		if err != nil {
			fmt.Fprintf(stderr, "graft: skeleton %s: %v\n", sp, err)
			return 2
		}
		in.genName = pipeline.SkeletonGenerator(&sk, profile)
		spec, err := os.ReadFile(*specPath)
		if err != nil {
			fmt.Fprintf(stderr, "graft: %v\n", err)
			return 2
		}
		if in.ids, err = pipeline.SpecRecordIDs(*recDir, drafter.Drafts(string(spec))); err != nil {
			fmt.Fprintf(stderr, "graft: refused: %v\n", err)
			return 2
		}
		if !slices.Contains(in.ids, in.id) {
			fmt.Fprintf(stderr, "graft: refused: record %s is not one of the records the spec resolves to\n", in.id)
			return 2
		}
	}
	st, err := readSignoff(in)
	if err != nil {
		fmt.Fprintf(stderr, "graft: refused: %v\n", err)
		return 2
	}
	// The log is verified here; evidence.Open refuses a damaged one.
	logPath := evidenceLogPath(*recDir, *logFlag)
	if err := os.MkdirAll(dirOf(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	log, err := evidence.Open(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	// With --json, stdout carries only the JSON evidence; what the human is
	// shown and asked goes to stderr.
	ui := stdout
	if *asJSON {
		ui = stderr
	}
	showSignoff(ui, st, in, log, logPath, cfg.Policy, *by, *note)
	if !*yes {
		if !stdinIsTTY(stdin) {
			fmt.Fprintln(stderr, "graft: refused: stdin is not a terminal; pass --yes to record the sign-off without a prompt")
			return 2
		}
		fmt.Fprint(ui, "Sign off on this region? [y/N] ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(ui, "not signed off; nothing written")
			return 1
		}
	}
	// What was shown must still be what is pinned: read it all again.
	again, err := readSignoff(in)
	if err != nil {
		fmt.Fprintf(stderr, "graft: failed: the tree or records changed while you were being asked: %v\n", err)
		return 3
	}
	if again.Pin != st.Pin {
		fmt.Fprintf(stderr, "graft: failed: the record, region or skeleton changed while you were being asked (now %s); nothing written, review again\n", pinSummary(again.Pin))
		return 3
	}
	e, err := log.Append(evidence.Evidence{Kind: evidence.HumanSignoff, Verdict: evidence.Pass, Pin: st.Pin,
		By: strings.TrimSpace(*by), Detail: *note})
	if err != nil {
		fmt.Fprintf(stderr, "graft: failed: %v\n", err)
		return 3
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(e)
		return 0
	}
	fmt.Fprintf(stdout, "signed off %s (record %s v%d) by %q; evidence %.12s in %s (by is not authenticated)\n", in.file, in.id, st.Pin.Version, strings.TrimSpace(*by), e.ID, logPath)
	return 0
}

func pinSummary(p evidence.Pin) string {
	return fmt.Sprintf("v%d gen %s region %.19s", p.Version, p.GenHash, p.RegionHash)
}

// showSignoff prints what the human is signing off on.
func showSignoff(w io.Writer, st signoffState, in signoffInputs, log *evidence.Log, logPath string, policy map[string][]evidence.Kind, by, note string) {
	p := st.Pin
	fmt.Fprintf(w, "Sign-off for record %s v%d, %s\n", p.RecordID, p.Version, in.file)
	fmt.Fprintf(w, "--- region body ---\n%s--- end of region ---\n", visible(st.Body))
	fmt.Fprintf(w, "pinned: gen_hash %s\n        region_hash %s\n        spec_hash %s\n", p.GenHash, p.RegionHash, orNone(p.SpecHash))
	if in.render {
		fmt.Fprintf(w, "        skeleton_hash %s\n", p.SkeletonHash)
	} else {
		fmt.Fprintln(w, "        skeleton_hash none (plain mode; use --render for a graft run --render tree)")
	}
	// For information only: what else speaks for exactly this region. A
	// sign-off on code whose checks fail should be a visible choice.
	latest := map[evidence.Kind]evidence.Evidence{}
	for _, e := range log.Fresh(p) {
		latest[e.Kind] = e
	}
	kinds := make([]string, 0, len(latest))
	for k := range latest {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	var prior []evidence.Evidence
	for _, e := range log.Fresh(p) {
		if e.Kind == evidence.HumanSignoff {
			prior = append(prior, e)
		}
	}
	if len(prior) > 0 {
		fmt.Fprintln(w, "already signed off (fresh), signing again adds another entry:")
		for _, e := range prior {
			fmt.Fprintf(w, "  %s by %q at %s\n", e.Verdict, e.By, e.At.Format(time.RFC3339))
		}
	}
	fmt.Fprintln(w, "other fresh evidence for this region:")
	n := 0
	for _, k := range kinds {
		if evidence.Kind(k) == evidence.HumanSignoff {
			continue
		}
		e := latest[evidence.Kind(k)]
		fmt.Fprintf(w, "  %s: %s (by %s)\n", k, e.Verdict, e.By)
		n++
	}
	if n == 0 {
		fmt.Fprintln(w, "  none")
	}
	if req, ok := policy[st.Region.Owner.Tier]; ok {
		var missing []string
		for _, k := range req {
			if k == evidence.HumanSignoff {
				continue
			}
			if _, ok := latest[k]; !ok {
				missing = append(missing, string(k))
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(w, "  tier %q also requires, with no fresh evidence: %s\n", st.Region.Owner.Tier, strings.Join(missing, ", "))
		}
	}
	fmt.Fprintf(w, "log: %s\n", logPath)
	fmt.Fprintf(w, "by: %q   (recorded as typed; NOT authenticated)\n", strings.TrimSpace(by))
	if note != "" {
		fmt.Fprintf(w, "note: %s\n", note)
	}
}

func orNone(s string) string {
	if s == "" {
		return "none (the record has no specs)"
	}
	return s
}

// visible makes s safe to print to a terminal: control characters other than
// newline and tab (an escape sequence in a region body, say) are shown as
// \xNN or \uNNNN, never interpreted.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == utf8.RuneError || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			if r < 0x100 {
				fmt.Fprintf(&b, "\\x%02x", r)
			} else {
				fmt.Fprintf(&b, "\\u%04x", r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
