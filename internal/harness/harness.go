// Package harness drives the bench arms (SPEC §10) against one case and
// records each sample with internal/bench. It measures; it never judges:
// comparisons are internal/bench's three permitted sentences.
//
// Arms:
//   - graft:    the full pipeline (draft → accept → converge per record)
//   - baseline: the same spec, the same model, one call, no pipeline
//   - intent:   one sentence plus the runtime contract, one call
//
// Every sample is built in a fresh directory outside the repository and scored
// by the oracle command, which is never shown to a model.
package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chad/graft/internal/bench"
	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
)

// Case is what every arm is asked to build.
type Case struct {
	Name        string
	Spec        string // full spec text (graft and baseline arms)
	Intent      string // one sentence (intent arm)
	Contract    string // the runtime contract every arm is told (how it is run)
	Conventions string // graft arm only: architecture conventions
	Module      string
	Fixture     string // digest of spec + intent + contract + oracle source: the question
	// Evolve cases: build Spec first, then change it to SpecV2 in the same tree,
	// and score with OracleArgs appended (e.g. -v2).
	SpecV2     string
	OracleArgs []string
	// graft-render arm: the skeletons for Spec and SpecV2.
	Skeleton, SkeletonV2 *recording.Skeleton
}

// Config is one harness run.
type Config struct {
	Case       Case
	Model      model.Model
	ModelName  string
	Gates      []converge.Gate
	Policy     evidence.Policy
	Tier       string
	Attempts   int
	OracleArgv []string // invoked as OracleArgv + "-bin" + <server>; prints bench JSON
	Commit     string
	Dirty      bool
	Progress   io.Writer
	// Transcripts, if set, keeps every prompt and answer under
	// <Transcripts>/<case>/<arm>/<n>/ so any sample can be audited.
	Transcripts string
	// graft-render arm: the sedum binary and its generators directory.
	Sedum      render.Sedum
	Generators string
}

// recorder counts calls and optionally writes each prompt and answer to dir.
type recorder struct {
	m     model.Model
	dir   string
	calls int
}

func (r *recorder) Complete(ctx context.Context, prompt string) (string, error) {
	r.calls++
	resp, err := r.m.Complete(ctx, prompt)
	if r.dir != "" {
		os.MkdirAll(r.dir, 0o755)
		base := filepath.Join(r.dir, fmt.Sprintf("%02d", r.calls))
		os.WriteFile(base+".prompt.md", []byte(prompt), 0o644)
		out := resp
		if err != nil {
			out = "ERROR: " + err.Error()
		}
		os.WriteFile(base+".answer.md", []byte(out), 0o644)
	}
	return resp, err
}

// classify turns a pipeline error into a sample reason. Infrastructure (a
// model command failure, a cancelled run) is unreachable, unless the code had
// already failed its checks in that run: a failed retry call does not erase a
// real code failure, so that sample is broke.
//
// "Already failed" is tracked for the whole sample, from the evidence log: if
// any gate recorded a non-pass verdict at any point in this sample (a region
// that later passed included), the sample is broke. Only a sample in which no
// gate ever failed can be unreachable.
func classify(err error, log *evidence.Log, since int, ctx context.Context) string {
	if errors.Is(err, pipeline.ErrModel) || ctx.Err() != nil {
		if f := firstFailure(log, since); f != "" {
			return fmt.Sprintf("pipeline: a gate had failed in this sample (%s) before the infrastructure failure: %v", f, err)
		}
		return infraPrefix + err.Error()
	}
	return "pipeline: " + err.Error()
}

// firstFailure returns the first non-pass gate verdict in the log from entry
// since on: the step being classified, not earlier steps of the same sample.
func firstFailure(log *evidence.Log, since int) string {
	if log == nil || log.Verify() != nil {
		return ""
	}
	all := log.All()
	if since > len(all) {
		since = len(all)
	}
	for _, e := range all[since:] {
		if e.Verdict != evidence.Pass {
			return firstLine(e.Detail)
		}
	}
	return ""
}

// infraPrefix marks a failure of the harness or the model command (not of the
// code under test); such a sample is unreachable, never broke.
const infraPrefix = "infra: "

// Digest hashes the parts that define the question.
func Digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s\x00", len(p), p)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Sample runs one arm once.
func Sample(ctx context.Context, cfg Config, arm bench.Arm) bench.Sample {
	dir, err := os.MkdirTemp("", "graft-bench-*")
	if err != nil {
		return bench.Sample{Outcome: bench.Unreachable, Reason: err.Error()}
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "app")
	var reason string
	switch arm {
	case bench.ArmGraft:
		reason = cfg.graftArm(ctx, dir, root)
	case bench.ArmGraftRender:
		reason = cfg.graftStep(ctx, filepath.Join(dir, "repo"), root, cfg.Case.Spec, cfg.Case.Skeleton)
	case bench.ArmBaseline:
		reason = cfg.oneCall(ctx, root, cfg.baselinePrompt())
	case bench.ArmIntent:
		reason = cfg.oneCall(ctx, root, cfg.intentPrompt())
	default:
		return bench.Sample{Outcome: bench.Unreachable, Reason: "unknown arm"}
	}
	if reason != "" {
		if strings.HasPrefix(reason, infraPrefix) {
			// The harness or the model command failed: nothing about the code was measured.
			return bench.Sample{Outcome: bench.Unreachable, Reason: strings.TrimPrefix(reason, infraPrefix)}
		}
		return bench.Sample{Outcome: bench.Broke, Reason: reason}
	}
	bin := filepath.Join(dir, "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return bench.Sample{Outcome: bench.Broke, Reason: "does not build: " + tail(string(out), 400)}
	}
	return cfg.score(ctx, bin)
}

func (cfg Config) graftArm(ctx context.Context, dir, root string) string {
	return cfg.graftStep(ctx, filepath.Join(dir, "repo"), root, cfg.Case.Spec, nil)
}

func (cfg Config) baselinePrompt() string {
	return "Write a complete Go program implementing this spec.\n\n## Spec\n\n" + strings.TrimSpace(cfg.Case.Spec) +
		"\n\n## How it is run\n\n" + strings.TrimSpace(cfg.Case.Contract) +
		"\n\nAnswer with the complete `main.go` (package main, standard library only) in a single ```go block.\n"
}

func (cfg Config) intentPrompt() string {
	return strings.TrimSpace(cfg.Case.Intent) + "\n\n## How it is run\n\n" + strings.TrimSpace(cfg.Case.Contract) +
		"\n\nAnswer with the complete `main.go` (package main, standard library only) in a single ```go block.\n"
}

// oneCall is the baseline and intent arms: one model call, one file, no gates.
func (cfg Config) oneCall(ctx context.Context, root, prompt string) string {
	resp, err := cfg.Model.Complete(ctx, prompt)
	if err != nil {
		return infraPrefix + "model: " + err.Error()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err.Error()
	}
	mod := cfg.Case.Module
	if mod == "" {
		mod = "app"
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+mod+"\n\ngo 1.22\n"), 0o644)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(model.ExtractCode(resp, "go")), 0o644); err != nil {
		return err.Error()
	}
	return ""
}

type oracleResult struct {
	Checks int      `json:"checks"`
	Passed int      `json:"passed"`
	Failed []string `json:"failed"`
}

// score runs the oracle: exit 0/1 with JSON is a measurement; exit 2 means
// the server never listened (broke); anything else is the harness's failure.
func (cfg Config) score(ctx context.Context, bin string) bench.Sample {
	argv := append(append(append([]string{}, cfg.OracleArgv...), cfg.Case.OracleArgs...), "-bin", bin)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		return bench.Sample{Outcome: bench.Unreachable, Reason: "oracle: " + err.Error()}
	}
	if code == 2 {
		return bench.Sample{Outcome: bench.Broke, Reason: "never listened: " + tail(errb.String(), 300)}
	}
	var r oracleResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Checks == 0 {
		return bench.Sample{Outcome: bench.Unreachable, Reason: fmt.Sprintf("oracle exit %d, unreadable output: %s", code, tail(out.String()+errb.String(), 300))}
	}
	s := bench.Sample{Checks: r.Checks, Passed: r.Passed, Outcome: bench.Works}
	if r.Passed < r.Checks {
		s.Outcome = bench.Disagreed
		s.Reason = "failed: " + strings.Join(r.Failed, ", ")
	}
	return s
}

// Run draws n samples of each arm and appends one record per arm.
func Run(ctx context.Context, cfg Config, store bench.Store, arms []bench.Arm, n int, res bench.Resolution) ([]bench.Record, error) {
	var out []bench.Record
	for _, arm := range arms {
		r := bench.Record{Case: cfg.Case.Name, Arm: arm, Commit: cfg.Commit, Dirty: cfg.Dirty, Fixture: cfg.Case.Fixture,
			Model: cfg.ModelName, Resolution: res}
		for i := 0; i < n; i++ {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			rec := &recorder{m: cfg.Model}
			if cfg.Transcripts != "" {
				rec.dir = filepath.Join(cfg.Transcripts, cfg.Case.Name, string(arm), fmt.Sprintf("%d-%d", time.Now().UnixNano(), i))
			}
			c := cfg
			c.Model = rec
			s := Sample(ctx, c, arm)
			calls := rec.calls
			s.Calls = &calls
			if rec.dir != "" {
				s.Transcript = rec.dir
			}
			if cfg.Progress != nil {
				fmt.Fprintf(cfg.Progress, "%s %d/%d: %s %d/%d %s\n", arm, i+1, n, s.Outcome, s.Passed, s.Checks, firstLine(s.Reason))
			}
			r.Samples = append(r.Samples, s)
		}
		stored, err := store.Append(r)
		if err != nil {
			return out, err
		}
		out = append(out, stored)
	}
	return out, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// counting wraps a Model and counts calls.
type counting struct {
	m     model.Model
	calls int
}

func (c *counting) Complete(ctx context.Context, p string) (string, error) {
	c.calls++
	return c.m.Complete(ctx, p)
}

// EvolveSample builds Spec, changes it to SpecV2 in the same tree, and scores
// the result with the evolved oracle. It records the model calls the change
// cost, and for graft how many pre-change files the change left byte-identical.
func EvolveSample(ctx context.Context, cfg Config, arm bench.Arm) bench.Sample {
	dir, err := os.MkdirTemp("", "graft-evolve-*")
	if err != nil {
		return bench.Sample{Outcome: bench.Unreachable, Reason: err.Error()}
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "app")
	cm := &counting{m: cfg.Model}
	c := cfg
	c.Model = cm
	var reason string
	var preserved, changed, synthChanged *int
	switch arm {
	case bench.ArmGraft, bench.ArmGraftRender:
		repo := filepath.Join(dir, "repo")
		skel1, skel2 := (*recording.Skeleton)(nil), (*recording.Skeleton)(nil)
		if arm == bench.ArmGraftRender {
			skel1, skel2 = c.Case.Skeleton, c.Case.SkeletonV2
		}
		if reason = c.graftStep(ctx, repo, root, c.Case.Spec, skel1); reason != "" {
			break
		}
		before, err := snapshot(root)
		if err != nil {
			reason = err.Error()
			break
		}
		cm.calls = 0
		if reason = c.graftStep(ctx, repo, root, c.Case.SpecV2, skel2); reason != "" {
			break
		}
		after, _ := snapshot(root)
		n := 0
		for f, src := range before {
			if after[f] == src {
				n++
			}
		}
		d := diffLines(before, after)
		preserved, changed = &n, &d
		if arm == bench.ArmGraftRender {
			sd := diffLines(filterSynth(before), filterSynth(after))
			synthChanged = &sd
		}
	case bench.ArmBaseline:
		if reason = c.oneCall(ctx, root, c.baselinePrompt()); reason != "" {
			break
		}
		before, _ := snapshot(root)
		cm.calls = 0
		if reason = c.oneCall(ctx, root, c.evolvePrompt(before["main.go"])); reason == "" {
			after, _ := snapshot(root)
			d := diffLines(before, after)
			changed = &d
		}
	default:
		return bench.Sample{Outcome: bench.Unreachable, Reason: "evolve cases run the graft, graft-render and baseline arms"}
	}
	calls := cm.calls
	if reason != "" {
		if strings.HasPrefix(reason, infraPrefix) {
			return bench.Sample{Outcome: bench.Unreachable, Reason: strings.TrimPrefix(reason, infraPrefix), ChangeCalls: &calls}
		}
		return bench.Sample{Outcome: bench.Broke, Reason: reason, ChangeCalls: &calls, Preserved: preserved, DiffLines: changed}
	}
	bin := filepath.Join(dir, "server")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return bench.Sample{Outcome: bench.Broke, Reason: "does not build: " + tail(string(out), 400), ChangeCalls: &calls, Preserved: preserved, DiffLines: changed}
	}
	s := cfg.score(ctx, bin)
	s.ChangeCalls, s.Preserved, s.DiffLines, s.SynthDiffLines = &calls, preserved, changed, synthChanged
	return s
}

// graftStep runs the pipeline once. A non-nil skeleton is the graft-render
// arm: sedum renders the skeleton and the model converges only Body helpers.
func (cfg Config) graftStep(ctx context.Context, repo, root, spec string, skel *recording.Skeleton) string {
	if skel != nil && (cfg.Sedum == nil || cfg.Generators == "") {
		return infraPrefix + "graft-render needs sedum and its generators"
	}
	acc, err := pipeline.Accept(repo, drafter.Drafts(spec), "graft bench", time.Now())
	if err != nil {
		return "accept: " + err.Error()
	}
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		return err.Error()
	}
	// Verdicts are scoped to this step: in an evolve sample, a gate retry while
	// building v1 says nothing about whether the change was measured.
	since := len(log.All())
	outs, err := pipeline.Run(ctx, pipeline.Config{
		Repo: repo, Root: root, Module: cfg.Case.Module, Spec: spec, Conventions: cfg.Case.Conventions,
		Tier: cfg.Tier, Attempts: cfg.Attempts, Model: cfg.Model, Gates: cfg.Gates, Policy: cfg.Policy, Log: log,
		Skeleton: skel, Sedum: cfg.Sedum, Generators: cfg.Generators,
	}, acc)
	_ = outs
	if err != nil {
		return classify(err, log, since, ctx)
	}
	return ""
}

func (cfg Config) evolvePrompt(current string) string {
	return "Here is a Go program and its revised spec. Update the program so it satisfies the revised spec.\n\n## Revised spec\n\n" +
		strings.TrimSpace(cfg.Case.SpecV2) + "\n\n## How it is run\n\n" + strings.TrimSpace(cfg.Case.Contract) +
		"\n\n## Current main.go\n\n```go\n" + current + "```\n\nAnswer with the complete updated `main.go` in a single ```go block.\n"
}

// filterSynth keeps only the model-written *_synth.go files of a snapshot.
func filterSynth(m map[string]string) map[string]string {
	out := map[string]string{}
	for f, src := range m {
		if recording.IsSynthPath(f) {
			out[f] = src
		}
	}
	return out
}

// snapshot reads every .go file in root.
func snapshot(root string) (map[string]string, error) {
	out := map[string]string{}
	names, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return nil, err
		}
		out[filepath.Base(n)] = string(b)
	}
	return out, nil
}

// diffLines is the size of the change between two trees, measured the same
// way for every arm: lines added plus lines removed, per file, by a
// longest-common-subsequence line diff (a new file counts all its lines, a
// deleted file all of its).
func diffLines(before, after map[string]string) int {
	total := 0
	seen := map[string]bool{}
	for f, a := range before {
		seen[f] = true
		total += lineDiff(a, after[f])
	}
	for f, b := range after {
		if !seen[f] {
			total += lineDiff("", b)
		}
	}
	return total
}

func splitLinesKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// lineDiff returns added+removed lines between a and b.
func lineDiff(a, b string) int {
	x, y := splitLinesKeep(a), splitLinesKeep(b)
	if a == b {
		return 0
	}
	// LCS length by dynamic programming over two rows.
	prev := make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			switch {
			case x[i-1] == y[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	lcs := prev[len(y)]
	return (len(x) - lcs) + (len(y) - lcs)
}

// RunEvolve draws n evolve samples of each arm and appends one record per arm.
func RunEvolve(ctx context.Context, cfg Config, store bench.Store, arms []bench.Arm, n int, res bench.Resolution) ([]bench.Record, error) {
	var out []bench.Record
	for _, arm := range arms {
		r := bench.Record{Case: cfg.Case.Name, Arm: arm, Commit: cfg.Commit, Dirty: cfg.Dirty, Fixture: cfg.Case.Fixture,
			Model: cfg.ModelName, Resolution: res}
		for i := 0; i < n; i++ {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			s := EvolveSample(ctx, cfg, arm)
			if cfg.Progress != nil {
				extra := ""
				if s.ChangeCalls != nil {
					extra += fmt.Sprintf(" change_calls=%d", *s.ChangeCalls)
				}
				if s.Preserved != nil {
					extra += fmt.Sprintf(" preserved=%d", *s.Preserved)
				}
				if s.DiffLines != nil {
					extra += fmt.Sprintf(" diff_lines=%d", *s.DiffLines)
				}
				fmt.Fprintf(cfg.Progress, "%s %d/%d: %s %d/%d%s %s\n", arm, i+1, n, s.Outcome, s.Passed, s.Checks, extra, firstLine(s.Reason))
			}
			r.Samples = append(r.Samples, s)
		}
		stored, err := store.Append(r)
		if err != nil {
			return out, err
		}
		out = append(out, stored)
	}
	return out, nil
}
