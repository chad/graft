package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/records"
)

// convergeConfig is graft.json: the tier policy and the gates every candidate runs.
type convergeConfig struct {
	// graft run only (ignored by graft converge):
	Model *struct {
		Argv       []string `json:"argv"`
		TimeoutSec int      `json:"timeout_sec"`
	} `json:"model"`
	Conventions string   `json:"conventions"` // path, relative to the config file
	Visible     []string `json:"visible"`     // files shown to the model as checks it must pass, relative to the config
	Tier        string   `json:"tier"`
	Attempts    int      `json:"attempts"`
	Module      string   `json:"module"`
	Skeleton    string   `json:"skeleton"` // path, relative to the config file (graft run --render)
	// Language is the target language profile; absent means Go.
	Language *lang.Profile `json:"language"`

	Policy map[string][]evidence.Kind `json:"policy"`
	Gates  []struct {
		Label      string        `json:"label"`
		Argv       []string      `json:"argv"`
		Kind       evidence.Kind `json:"kind"`
		TimeoutSec int           `json:"timeout_sec"`
	} `json:"gates"`
}

func loadConvergeConfig(path string) (convergeConfig, []converge.Gate, error) {
	var c convergeConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return c, nil, err
	}
	// encoding/json alone is not strict: it matches keys case-insensitively,
	// lets a duplicate key win silently, accepts null and ignores trailing data.
	if err := strictCheck(b, map[string][]string{
		"":         {"policy", "gates", "model", "conventions", "visible", "tier", "attempts", "module", "skeleton", "language"},
		"language": {"name", "ext", "fence", "comment", "synth_suffix", "preamble", "bootstrap", "build", "run"},
		"gates[]":  {"label", "argv", "kind", "timeout_sec"},
		"model":    {"argv", "timeout_sec"},
	}); err != nil {
		return c, nil, fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, nil, fmt.Errorf("%s: %w", path, err)
	}
	if dec.More() {
		return c, nil, fmt.Errorf("%s: trailing data after the config object", path)
	}
	if len(c.Policy) == 0 {
		return c, nil, fmt.Errorf("%s: policy is empty", path)
	}
	for tier, kinds := range c.Policy {
		for _, k := range kinds {
			if !knownKind(k) {
				return c, nil, fmt.Errorf("%s: tier %q requires unknown kind %q", path, tier, k)
			}
		}
	}
	// An argument starting with ./ or ../ that names an existing file next to
	// the config is resolved against the config's directory (gates run inside
	// a scratch copy of the tree, not next to it). Anything else, such as the
	// Go pattern "./...", is passed through untouched.
	base := filepath.Dir(path)
	for i := range c.Gates {
		for j, a := range c.Gates[i].Argv {
			if !strings.HasPrefix(a, "./") && !strings.HasPrefix(a, "../") {
				continue
			}
			p := filepath.Join(base, a)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				if abs, err := filepath.Abs(p); err == nil {
					c.Gates[i].Argv[j] = abs
				}
			}
		}
	}
	var gates []converge.Gate
	for i, g := range c.Gates {
		if g.Label == "" || len(g.Argv) == 0 || g.Argv[0] == "" || g.Kind == "" {
			return c, nil, fmt.Errorf("%s: gate %d needs label, argv and kind", path, i)
		}
		if !converge.Executable(g.Kind) {
			return c, nil, fmt.Errorf("%s: gate %q claims kind %q; a command gate can only produce an executable kind (test_run, eval, static_analysis, conformance)", path, g.Label, g.Kind)
		}
		if g.TimeoutSec < 0 || g.TimeoutSec > maxTimeoutSec {
			return c, nil, fmt.Errorf("%s: gate %q timeout_sec must be 0..%d", path, g.Label, maxTimeoutSec)
		}
		gates = append(gates, converge.CommandGate{Label: g.Label, Argv: g.Argv, As: g.Kind,
			Timeout: time.Duration(g.TimeoutSec) * time.Second})
	}
	return c, gates, nil
}

// maxTimeoutSec bounds a gate's timeout (24h) well below time.Duration overflow.
const maxTimeoutSec = 24 * 60 * 60

// convergeUsage documents the flags and, above all, the exit codes.
const convergeUsage = `usage: graft converge --record ID --path FILE --tier T [flags] BODY|-

Checks a synthesized body through the configured gates and the tier's policy,
and writes it into the record's synthesis region only if the tier is satisfied.
The record's version, gen_hash and spec hash come from --records; they cannot
be set here.

exit codes:
  0  applied (guarantee: checked)
  1  checked, NOT applied: a gate failed or the tier is unsatisfied
  2  refused before checking began: usage, config, unknown record or tier,
     stale pin, bad path, hand-edited region, sedum overlap, marker in body
  3  failed after checking began: a gate could not start, the run was
     cancelled, or the tree or record changed during the gates;
     any evidence recorded so far is kept
`

// loadVisible reads the config's "visible" files (relative to the config).
func loadVisible(cfgPath string, c convergeConfig) (map[string]string, error) {
	out := map[string]string{}
	for _, rel := range c.Visible {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), rel))
		if err != nil {
			return nil, fmt.Errorf("visible %s: %w", rel, err)
		}
		out[filepath.ToSlash(rel)] = string(b)
	}
	return out, nil
}

// knownKind: any evidence kind a policy may require.
func knownKind(k evidence.Kind) bool {
	switch k {
	case evidence.TestRun, evidence.Eval, evidence.HumanSignoff, evidence.StaticAnalysis, evidence.Conformance:
		return true
	}
	return false
}

// convergeCmd: graft converge --config graft.json --records DIR --log FILE
// --record ID --path FILE --tier T [--comment //] [--root .] BODY|-
//
// Exit 0: applied. Exit 1: checked and NOT applied (a gate failed or the tier
// is unsatisfied). Exit 2: usage or a refusal before checking (bad path,
// stale pin, hand-edited region, sedum overlap, ...). Exit 3: failed after
// checking began (a later gate could not start, the tree or record changed
// during the gates, log I/O); evidence recorded so far is kept.
//
// Gates run with graft's environment and permissions: the scratch copy keeps
// them off the working tree, it is not a sandbox. --tier is caller-supplied
// because records carry no risk tier (SPEC §12). The spec hash, like the
// version and gen_hash, comes from the records layer.
func convergeCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("converge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, convergeUsage); fs.PrintDefaults() }
	cfgPath := fs.String("config", "graft.json", "policy and gates")
	recDir := fs.String("records", ".", "provenance repo root (records layer)")
	logPath := fs.String("log", ".graft/evidence.jsonl", "evidence log (relative to the current directory, not --root)")
	root := fs.String("root", ".", "working tree")
	id := fs.String("record", "", "owning record id")
	path := fs.String("path", "", "target file, relative to --root")
	tier := fs.String("tier", "", "risk tier of the owning record")
	comment := fs.String("comment", "//", "line-comment prefix for markers")
	asJSON := fs.Bool("json", false, "print the decision as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" || *path == "" || *tier == "" || fs.NArg() != 1 {
		fmt.Fprint(stderr, convergeUsage)
		return 2
	}
	var body []byte
	var err error
	if fs.Arg(0) == "-" {
		body, err = io.ReadAll(stdin)
	} else {
		body, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cfg, gates, err := loadConvergeConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	if cfg.Skeleton != "" {
		fmt.Fprintln(stderr, "graft: config \"skeleton\" only applies to `graft run --render`; remove it for converge")
		return 2
	}
	store, err := records.OpenDir(*recDir)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cand, err := converge.NewCandidate(store, *id, *path, *tier, string(body))
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	if _, ok := cfg.Policy[*tier]; !ok {
		fmt.Fprintf(stderr, "graft: unknown tier %q\n", *tier)
		return 2
	}
	if err := os.MkdirAll(dirOf(*logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	log, err := evidence.Open(*logPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cand.Comment = *comment
	c := converge.Converger{Root: *root, Records: store, Log: log, Policy: evidence.Policy(cfg.Policy), Gates: gates}
	// Ctrl-C cancels the context: the running gate's process group is killed
	// and Converge's deferred cleanup removes the scratch tree.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d, err := c.Converge(ctx, cand)
	if err != nil {
		if d.Checked {
			// Checking had begun when it failed (a gate could not start, the
			// run was cancelled, the tree or record changed): not a refusal,
			// not a decision. Evidence written so far is kept.
			fmt.Fprintf(stderr, "graft: failed after checking began (%d evidence entries recorded): %v\n", len(d.Evidence), err)
			return 3
		}
		fmt.Fprintf(stderr, "graft: refused: %v\n", err)
		return 2
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(d)
	} else {
		printDecision(stdout, cand, d)
	}
	if !d.Applied {
		return 1
	}
	return 0
}

func printDecision(w io.Writer, c converge.Candidate, d converge.Decision) {
	state := "NOT applied"
	if d.Applied {
		state = "applied"
	}
	fmt.Fprintf(w, "%s %s (record %s v%d) — %s; guarantee: %s\n", c.Path, state, c.RecordID, c.Version, verdict(d), d.Guarantee)
	for _, e := range d.Evidence {
		fmt.Fprintf(w, "  %s %s  evidence %.12s\n", e.Kind, e.Verdict, e.ID)
	}
	for _, k := range d.Result.Missing {
		fmt.Fprintf(w, "  missing: %s\n", k)
	}
	for _, k := range d.Result.Failing {
		fmt.Fprintf(w, "  failing: %s\n", k)
	}
}

func verdict(d converge.Decision) string {
	switch {
	case d.Result.Satisfied:
		return "tier satisfied"
	case len(d.Result.Failing) > 0:
		return "a check failed"
	default:
		return "tier unsatisfied"
	}
}

func dirOf(p string) string { return filepath.Dir(p) }
