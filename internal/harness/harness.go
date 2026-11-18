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
func classify(err error, outs []pipeline.Outcome, ctx context.Context) string {
	if errors.Is(err, pipeline.ErrModel) || ctx.Err() != nil {
		for _, o := range outs {
			if !o.Applied && o.LastFailure != "" {
				return fmt.Sprintf("pipeline: %s failed its checks (%s), then the retry's model call failed: %v",
					o.Section, firstLine(o.LastFailure), err)
			}
		}
		return infraPrefix + err.Error()
	}
	return "pipeline: " + err.Error()
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
	repo := filepath.Join(dir, "repo")
	acc, err := pipeline.Accept(repo, drafter.Drafts(cfg.Case.Spec), "graft bench", time.Now())
	if err != nil {
		return "accept: " + err.Error()
	}
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		return err.Error()
	}
	outs, err := pipeline.Run(ctx, pipeline.Config{
		Repo: repo, Root: root, Module: cfg.Case.Module, Spec: cfg.Case.Spec, Conventions: cfg.Case.Conventions,
		Tier: cfg.Tier, Attempts: cfg.Attempts, Model: cfg.Model, Gates: cfg.Gates, Policy: cfg.Policy, Log: log,
	}, acc)
	if err != nil {
		return classify(err, outs, ctx)
	}
	return ""
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
	argv := append(append([]string{}, cfg.OracleArgv...), "-bin", bin)
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
