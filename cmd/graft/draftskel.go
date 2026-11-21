package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
	"github.com/chad/graft/internal/skeldraft"
)

const draftSkeletonUsage = `usage: graft draft --skeleton --config graft.json --spec SPEC.md --out proposal.json [flags]

Asks a model to propose the skeleton (the shape of each file: fixed decls and
stubs) for the spec's sections. It proposes and never applies: it writes only
--out (skeleton JSON, the same format --render imports) and --out.coverage.json,
and touches no record and no tree. To accept, read the proposal, then
  graft run --render --skeleton proposal.json --accept ...
which imports it into the records through the usual path.

Before a proposal is written it must pass: a strict parse, the language
profile's decl_check (drafted decls are types, consts and signatures only; no
logic), and a sedum render into a scratch tree that the profile's build gate
builds with placeholder bodies. A failure goes back to the model with the
checker's and the build's own output, for --retries retries (default 2, max 5).

The coverage report maps each spec statement to the stubs and decls the model
CLAIMS own it; it is a claim, not evidence. A statement nothing owns is flagged
UNOWNED, the hard signal.

The model is --model, else the config's "skeleton_model", else opus.

exit codes:
  0  proposal written, every statement claimed
  1  no valid proposal within the retries (nothing written)
  2  usage or config error, or --out exists (use --force)
  3  the model command, sedum or I/O failed
  4  proposal written, but some statements are UNOWNED (review them; --strict
     makes this a retry instead)
`

// defaultSkeletonModel is the model drafting uses when none is configured: a
// stronger one than the body model, since the output is small and rarely changes.
var defaultSkeletonModel = []string{"claude", "-p", "--tools", "", "--strict-mcp-config", "--model", "opus"}

func wantsSkeleton(args []string) bool {
	for _, a := range args {
		if a == "--skeleton" || a == "-skeleton" {
			return true
		}
	}
	return false
}

func draftSkeletonCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("draft --skeleton", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, draftSkeletonUsage); fs.PrintDefaults() }
	fs.Bool("skeleton", true, "draft a skeleton (this mode)")
	cfgPath := fs.String("config", "graft.json", "config: language, conventions, skeleton_model")
	specPath := fs.String("spec", "", "markdown spec")
	out := fs.String("out", "", "where to write the proposal (skeleton JSON)")
	modelArg := fs.String("model", "", "model command, split on spaces (default: config skeleton_model, else opus)")
	retries := fs.Int("retries", skeldraft.DefaultRetries, "times an invalid proposal goes back to the model (max 5)")
	strict := fs.Bool("strict", false, "an UNOWNED statement is a retry, not just a flag")
	force := fs.Bool("force", false, "overwrite --out and its coverage file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" || *out == "" || fs.NArg() != 0 {
		fmt.Fprint(stderr, draftSkeletonUsage)
		return 2
	}
	if *retries < 0 || *retries > skeldraft.MaxRetries {
		fmt.Fprintf(stderr, "graft: --retries must be 0..%d\n", skeldraft.MaxRetries)
		return 2
	}
	covPath := *out + ".coverage.json"
	if !*force {
		for _, p := range []string{*out, covPath} {
			if _, err := os.Lstat(p); err == nil {
				fmt.Fprintf(stderr, "graft: %s exists; refusing to overwrite it (use --force)\n", p)
				return 2
			}
		}
	}
	cfg, _, err := loadConvergeConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	if cfg.Conventions == "" {
		fmt.Fprintln(stderr, "graft: graft draft --skeleton needs conventions in the config")
		return 2
	}
	conv, err := os.ReadFile(filepath.Join(filepath.Dir(*cfgPath), cfg.Conventions))
	if err != nil {
		fmt.Fprintf(stderr, "graft: conventions: %v\n", err)
		return 2
	}
	spec, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	profile, err := lang.Resolve(cfg.Language)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	if len(profile.DeclCheck) == 0 {
		fmt.Fprintf(stderr, "graft: the %s language profile has no decl_check, so it cannot draft a skeleton\n", profile.Name)
		return 2
	}
	argv, timeout := defaultSkeletonModel, 10*time.Minute
	switch {
	case *modelArg != "":
		argv = strings.Fields(*modelArg)
	case cfg.SkeletonModel != nil && len(cfg.SkeletonModel.Argv) > 0:
		argv = cfg.SkeletonModel.Argv
		if cfg.SkeletonModel.TimeoutSec > 0 {
			timeout = time.Duration(cfg.SkeletonModel.TimeoutSec) * time.Second
		}
	}
	bin, err := exec.LookPath("sedum")
	if err != nil {
		fmt.Fprintln(stderr, "graft: graft draft --skeleton needs sedum on PATH")
		return 2
	}
	gens := os.Getenv("SEDUM_GENERATORS")
	if gens == "" {
		home, _ := os.UserHomeDir()
		gens = filepath.Join(home, "sedum", "generators")
	}
	if _, err := os.Stat(gens); err != nil {
		fmt.Fprintf(stderr, "graft: sedum generators: %v (set SEDUM_GENERATORS)\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pcfg := pipeline.Config{Module: cfg.Module, Lang: profile, Tier: cfg.Tier, Sedum: render.Exec(bin), Generators: gens}
	fmt.Fprintf(stdout, "model: %s\n", strings.Join(argv, " "))
	res, err := skeldraft.Draft(ctx, skeldraft.Config{
		Model:   model.Command{Argv: argv, Timeout: timeout},
		Profile: profile, Spec: string(spec), Conventions: string(conv),
		Retries: *retries, Strict: *strict,
		FileFor: func(s string) string { return pipeline.SectionFile(s, profile.Ext) },
		Check:   skeldraft.ExecCheck(profile),
		Scratch: func(ctx context.Context, drafts []drafter.Draft, sk recording.Skeleton) (string, func(), error) {
			return pipeline.RenderScratch(ctx, pcfg, drafts, sk)
		},
		Build: skeldraft.ExecBuild(profile),
	})
	fmt.Fprintf(stdout, "%d model call(s)\n", res.Calls)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		if errors.Is(err, skeldraft.ErrInvalid) {
			fmt.Fprintln(stderr, "graft: nothing written")
			return 1
		}
		return 3
	}
	pj, err := json.MarshalIndent(res.Skeleton, "", "  ")
	if err == nil {
		var cj []byte
		if cj, err = res.ReportJSON(); err == nil {
			if err = os.WriteFile(*out, append(pj, '\n'), 0o644); err == nil {
				err = os.WriteFile(covPath, append(cj, '\n'), 0o644)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 3
	}
	fmt.Fprint(stdout, res.Report())
	fmt.Fprintf(stdout, "proposal: %s (validated: decls logic-free, sedum render, seeded tree builds)\nnothing applied; to accept: graft run --render --skeleton %s --accept ...\n", *out, *out)
	if u := res.Unowned(); len(u) > 0 {
		return 4
	}
	return 0
}
