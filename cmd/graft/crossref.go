package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chad/graft/internal/crossref"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/lang"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/pipeline"
	"github.com/chad/graft/internal/recording"
)

const crossrefUsage = `usage: graft crossref --config graft.json --spec SPEC.md --repo DIR --out crossref.json [flags]

The cross-reference step (SPEC §12). A statement in one section can constrain
code another section's record owns, without naming it. For every statement that
is new or changed since the last accept (found without a model), ONE model call
decides: "local", or "constrains" which other records. It proposes and never
applies: it writes only --out and touches no record and no tree. To accept, read
the proposal (delete or fix entries you disagree with), then
  graft run --accept --crossref crossref.json ...
which copies each accepted statement into the owning record's draft, so that
record is revised and its region regenerated.

Each answer carries a receipt: the statement quoted from the spec, the target
record, the reason, the code location ("file: Identifier") that must change and
a quote from the target's record. Receipts are validated for FORM, never for
truth: the target must exist and not be the statement's own record, the location
must be the target's file (and, with --root, name an identifier found in it; with
--skeleton, name a stub or decl of that file in the skeleton, else DANGLING with
"needs a skeleton change (graft draft --skeleton)"), the quote must occur in the target's record. A wrong but well-formed answer passes;
every "local" verdict is therefore printed for you to check.

After a spec change, graft run --accept refuses (exit 2) while a changed
statement has no answer, unless you pass --no-crossref.

The model is --model, else the config's "skeleton_model", else opus. With no
changed statement it makes no call and writes an empty proposal.

exit codes:
  0  proposal written, every changed statement answered and valid
  1  no parseable proposal within the retries (nothing written)
  2  usage or config error, drafts needing a human decision, or --out exists (use --force)
  3  the model command or I/O failed
  4  proposal written, but some statements are UNANSWERED or carry a DANGLING claim
     (only after the retries: an invalid answer is fed back to the model first;
     --strict makes that exit 1, nothing written)
Advisory warnings (a "local" statement that names something another record uses)
are printed but never change the exit code.
`

func crossrefCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crossref", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, crossrefUsage); fs.PrintDefaults() }
	cfgPath := fs.String("config", "graft.json", "config: conventions, skeleton_model")
	specPath := fs.String("spec", "", "markdown spec (the changed one)")
	repo := fs.String("repo", "", "provenance repo holding the accepted records")
	out := fs.String("out", "", "where to write the proposal (crossref.json)")
	modelArg := fs.String("model", "", "model command, split on spaces (default: config skeleton_model, else opus)")
	root := fs.String("root", "", "code tree: a location's identifier must occur in the target's file")
	skelPath := fs.String("skeleton", "", "render mode: the skeleton JSON (as for graft run --render); a location must name a stub or decl of the target's file in it")
	withCode := fs.Bool("with-code", false, "show each record's current file to the model (needs --root)")
	retries := fs.Int("retries", 2, "times an unparseable answer goes back to the model (max 5)")
	strict := fs.Bool("strict", false, "after the retries, an invalid proposal is exit 1 (nothing written), not exit 4")
	force := fs.Bool("force", false, "overwrite --out")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" || *repo == "" || *out == "" || fs.NArg() != 0 {
		fmt.Fprint(stderr, crossrefUsage)
		return 2
	}
	if *retries < 0 || *retries > 5 {
		fmt.Fprintln(stderr, "graft: --retries must be 0..5")
		return 2
	}
	if *withCode && *root == "" {
		fmt.Fprintln(stderr, "graft: --with-code needs --root")
		return 2
	}
	if !*force {
		if _, err := os.Lstat(*out); err == nil {
			fmt.Fprintf(stderr, "graft: %s exists; refusing to overwrite it (use --force)\n", *out)
			return 2
		}
	}
	cfg, _, err := loadConvergeConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	var conv []byte
	if cfg.Conventions != "" {
		if conv, err = os.ReadFile(filepath.Join(filepath.Dir(*cfgPath), cfg.Conventions)); err != nil {
			fmt.Fprintf(stderr, "graft: conventions: %v\n", err)
			return 2
		}
	}
	spec, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
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
	var skel *recording.Skeleton
	if *skelPath != "" {
		sb, err := os.ReadFile(*skelPath)
		if err != nil {
			fmt.Fprintf(stderr, "graft: skeleton: %v\n", err)
			return 2
		}
		sk, err := recording.LoadSkeleton(sb)
		if err != nil {
			fmt.Fprintf(stderr, "graft: skeleton %s: %v\n", *skelPath, err)
			return 2
		}
		skel = &sk
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	profile, err := lang.Resolve(cfg.Language)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	drafts := drafter.Drafts(string(spec))
	acceptOpts := pipeline.AcceptOptions{Ext: profile.Ext, Files: cfg.Files}
	if skel == nil {
		acceptOpts.Root = *root // as graft run: plain mode never takes over an existing file
	}
	fmt.Fprintf(stdout, "model: %s\n", strings.Join(argv, " "))
	prop, stmts, recs, calls, err := pipeline.Crossref(ctx, pipeline.CrossrefInput{
		Repo: *repo, Drafts: drafts, Model: model.Command{Argv: argv, Timeout: timeout},
		Conventions: string(conv), Root: *root, WithCode: *withCode, Retries: *retries, Strict: *strict, Skeleton: skel, Accept: acceptOpts,
	})
	fmt.Fprintf(stdout, "%d model call(s)\n", calls)
	switch {
	case errors.Is(err, pipeline.ErrUndecided):
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	case errors.Is(err, crossref.ErrNoProposal):
		fmt.Fprintf(stderr, "graft: %v\ngraft: nothing written\n", err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 3
	}
	if len(stmts) == 0 {
		fmt.Fprintln(stdout, "no statement is new or changed since the last accept (or this is the first accept): nothing to cross-reference")
	}
	pj, _ := json.MarshalIndent(prop, "", "  ")
	if err := os.WriteFile(*out, append(pj, '\n'), 0o644); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 3
	}
	bad := 0
	for _, a := range prop.Answers {
		fmt.Fprintf(stdout, "\n[%s] %s: %q\n", a.Status, a.Section, a.Text)
		switch {
		case a.Status != crossref.OK:
			bad++
			fmt.Fprintf(stdout, "  %s\n", a.Detail)
		case a.Verdict == crossref.Local:
			fmt.Fprintln(stdout, "  local: no other record's code must change (check this)")
		default:
			for _, t := range a.Targets {
				fmt.Fprintf(stdout, "  constrains %s at %s\n    reason: %s\n    quote:  %q\n", t.Record, t.Location, t.Reason, t.Quote)
			}
		}
	}
	for _, w := range crossref.Warn(stmts, prop.Answers, recs) {
		fmt.Fprintln(stdout, w)
	}
	fmt.Fprintf(stdout, "\nproposal: %s\nnothing applied; to accept: graft run --accept --crossref %s ...\n", *out, *out)
	if bad > 0 {
		return 4
	}
	return 0
}

// loadCrossref reads a proposal for `graft run --crossref`. Unknown keys are
// refused; statuses in the file are never trusted (accept re-validates).
func loadCrossref(path string) (*crossref.Proposal, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p crossref.Proposal
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%s: trailing data", path)
	}
	if p.Schema != crossref.SchemaVersion {
		return nil, fmt.Errorf("%s: schema %d, want %d", path, p.Schema, crossref.SchemaVersion)
	}
	return &p, nil
}
