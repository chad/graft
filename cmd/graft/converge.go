package main

import (
	"bytes"
	"context"
	"encoding/json"

	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/records"
)

// convergeConfig is graft.json: the tier policy and the gates every candidate runs.
type convergeConfig struct {
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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Policy) == 0 {
		return c, nil, fmt.Errorf("%s: policy is empty", path)
	}
	var gates []converge.Gate
	for i, g := range c.Gates {
		if g.Label == "" || len(g.Argv) == 0 || g.Kind == "" {
			return c, nil, fmt.Errorf("%s: gate %d needs label, argv and kind", path, i)
		}
		gates = append(gates, converge.CommandGate{Label: g.Label, Argv: g.Argv, As: g.Kind,
			Timeout: time.Duration(g.TimeoutSec) * time.Second})
	}
	return c, gates, nil
}

// convergeCmd: graft converge --config graft.json --records DIR --log FILE
// --record ID --path FILE --tier T [--spec-hash H] [--comment //] [--root .] BODY|-
//
// Exit 0: applied. Exit 1: checked and NOT applied (a gate failed or the tier
// is unsatisfied). Exit 2: usage or a refusal before checking (bad path,
// stale pin, hand-edited region, sedum overlap, ...).
func convergeCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("converge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "graft.json", "policy and gates")
	recDir := fs.String("records", ".", "provenance repo root (records layer)")
	logPath := fs.String("log", ".graft/evidence.jsonl", "evidence log")
	root := fs.String("root", ".", "working tree")
	id := fs.String("record", "", "owning record id")
	path := fs.String("path", "", "target file, relative to --root")
	tier := fs.String("tier", "", "risk tier of the owning record")
	specHash := fs.String("spec-hash", "", "hash of the specs the gates run (opaque)")
	comment := fs.String("comment", "//", "line-comment prefix for markers")
	asJSON := fs.Bool("json", false, "print the decision as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" || *path == "" || *tier == "" || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: graft converge --record ID --path FILE --tier T [flags] BODY|-")
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
	store, err := records.OpenDir(*recDir)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
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
	cand, err := converge.NewCandidate(store, *id, *path, *tier, *specHash, string(body))
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cand.Comment = *comment
	c := converge.Converger{Root: *root, Records: store, Log: log, Policy: evidence.Policy(cfg.Policy), Gates: gates}
	d, err := c.Converge(context.Background(), cand)
	if err != nil {
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
