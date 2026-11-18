package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chad/graft/internal/bench"
	"github.com/chad/graft/internal/evidence"
	"github.com/chad/graft/internal/harness"
	"github.com/chad/graft/internal/model"
	"github.com/chad/graft/internal/recording"
	"github.com/chad/graft/internal/render"
)

// caseFile is examples/<case>/case.json: paths are relative to it.
type caseFile struct {
	Name        string `json:"name"`
	Spec        string `json:"spec"`
	Intent      string `json:"intent"`
	Contract    string `json:"contract"`
	Conventions string `json:"conventions"`
	Config      string `json:"config"`   // graft.json: model, gates, policy, tier
	Oracle      string `json:"oracle"`   // Go package dir of the oracle
	Skeleton    string `json:"skeleton"` // graft-render arm: sedum skeleton for spec
	Evolve      *struct {
		Spec       string   `json:"spec"`        // the changed spec
		OracleArgs []string `json:"oracle_args"` // e.g. ["-v2"]
		Skeleton   string   `json:"skeleton"`    // graft-render arm: skeleton for the changed spec
	} `json:"evolve"`
}

const benchUsage = `usage:
  graft bench run    --case CASE.json --arms graft,baseline,intent --resolution smoke|coarse|fine [--evolve] [--results DIR]
  graft bench report --case NAME [--results DIR]

run draws the resolution's sample count for each arm (smoke 2, coarse 5,
fine 30), scores every sample with the case's oracle, and appends one record
per arm. With --evolve, each sample builds the spec, changes it to the case's
evolve spec in the same tree, and is scored by the evolved oracle; results go
to <case>-evolve and record the model calls the change cost. report prints
intervals and the only permitted comparisons.
`

func benchCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, benchUsage)
		return 2
	}
	switch args[0] {
	case "run":
		return benchRun(args[1:], stdout, stderr)
	case "report":
		return benchReport(args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, benchUsage)
	return 2
}

func benchReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("case", "", "case name")
	dir := fs.String("results", "bench/results", "results directory")
	if err := fs.Parse(args); err != nil || *name == "" {
		fmt.Fprint(stderr, benchUsage)
		return 2
	}
	st := bench.Store{Dir: *dir}
	recs, bad, err := st.Load(*name)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	attrs, badA, err := st.LoadAttributions(*name)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	if err := bench.Report(stdout, *name, recs, attrs, append(bad, badA...)); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	return 0
}

func benchRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bench run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	casePath := fs.String("case", "", "case.json")
	armsFlag := fs.String("arms", "graft,baseline,intent", "arms to run")
	resFlag := fs.String("resolution", "", "smoke | coarse | fine")
	dir := fs.String("results", "bench/results", "results directory")
	transcripts := fs.String("transcripts", "", "keep every prompt and answer under this directory")
	evolve := fs.Bool("evolve", false, "run the case's change-then-regenerate variant")
	if err := fs.Parse(args); err != nil || *casePath == "" {
		fmt.Fprint(stderr, benchUsage)
		return 2
	}
	res := bench.Resolution(*resFlag)
	n, ok := bench.Samples[res]
	if !ok {
		fmt.Fprintln(stderr, "graft: --resolution must be smoke, coarse or fine (the question decides the size)")
		return 2
	}
	var arms []bench.Arm
	for _, a := range strings.Split(*armsFlag, ",") {
		switch arm := bench.Arm(strings.TrimSpace(a)); arm {
		case bench.ArmGraft, bench.ArmBaseline, bench.ArmIntent, bench.ArmGraftRender:
			arms = append(arms, arm)
		default:
			fmt.Fprintf(stderr, "graft: unknown arm %q\n", a)
			return 2
		}
	}
	base := filepath.Dir(*casePath)
	raw, err := os.ReadFile(*casePath)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	var cf caseFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		fmt.Fprintf(stderr, "graft: %s: %v\n", *casePath, err)
		return 2
	}
	read := func(rel string) (string, error) {
		b, err := os.ReadFile(filepath.Join(base, rel))
		return string(b), err
	}
	spec, err1 := read(cf.Spec)
	conv, err2 := read(cf.Conventions)
	if err := firstErr(err1, err2); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	cfg, gates, err := loadConvergeConfig(filepath.Join(base, cf.Config))
	if err != nil || cfg.Model == nil {
		fmt.Fprintf(stderr, "graft: config: %v (needs model)\n", err)
		return 2
	}
	visible, err := loadVisible(filepath.Join(base, cf.Config), cfg)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	oracleSrc, err := dirSource(filepath.Join(base, cf.Oracle))
	if err != nil {
		fmt.Fprintf(stderr, "graft: oracle: %v\n", err)
		return 2
	}
	tmp, err := os.MkdirTemp("", "graft-oracle-*")
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 2
	}
	defer os.RemoveAll(tmp)
	oracleBin := filepath.Join(tmp, "oracle")
	if out, err := exec.Command("go", "build", "-o", oracleBin, "./"+filepath.ToSlash(filepath.Join(base, cf.Oracle))).CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "graft: build oracle: %v\n%s", err, out)
		return 2
	}
	name, specV2, oracleArgs, fixtureExtra := cf.Name, "", []string(nil), ""
	if *evolve {
		if cf.Evolve == nil {
			fmt.Fprintln(stderr, "graft: the case has no evolve section")
			return 2
		}
		if specV2, err = read(cf.Evolve.Spec); err != nil {
			fmt.Fprintf(stderr, "graft: %v\n", err)
			return 2
		}
		for _, a := range arms {
			if a == bench.ArmIntent {
				fmt.Fprintln(stderr, "graft: evolve runs the graft, graft-render and baseline arms only")
				return 2
			}
		}
		name, oracleArgs = cf.Name+"-evolve", cf.Evolve.OracleArgs
		fixtureExtra = specV2 + "\x00" + strings.Join(oracleArgs, " ")
	}
	// graft-render: skeletons from the case, sedum from PATH, generators from
	// SEDUM_GENERATORS (or ~/sedum/generators); the sedum version is recorded.
	var skel1, skel2 *recording.Skeleton
	var sedum render.Sedum
	var gens, sedumVer string
	for _, a := range arms {
		if a != bench.ArmGraftRender {
			continue
		}
		load := func(rel string) (*recording.Skeleton, error) {
			if rel == "" {
				return nil, fmt.Errorf("case has no skeleton for the graft-render arm")
			}
			b, err := os.ReadFile(filepath.Join(base, rel))
			if err != nil {
				return nil, err
			}
			sk, err := recording.LoadSkeleton(b)
			return &sk, err
		}
		if skel1, err = load(cf.Skeleton); err != nil {
			fmt.Fprintf(stderr, "graft: %v\n", err)
			return 2
		}
		if *evolve {
			if skel2, err = load(cf.Evolve.Skeleton); err != nil {
				fmt.Fprintf(stderr, "graft: evolve: %v\n", err)
				return 2
			}
		}
		bin, err := exec.LookPath("sedum")
		if err != nil {
			fmt.Fprintln(stderr, "graft: the graft-render arm needs sedum on PATH")
			return 2
		}
		sedum = render.Exec(bin)
		if v, err := sedum.Version(context.Background()); err == nil {
			sedumVer = strings.TrimSpace(v)
		}
		gens = os.Getenv("SEDUM_GENERATORS")
		if gens == "" {
			home, _ := os.UserHomeDir()
			gens = filepath.Join(home, "sedum", "generators")
		}
		if _, err := os.Stat(gens); err != nil {
			fmt.Fprintf(stderr, "graft: sedum generators: %v (set SEDUM_GENERATORS)\n", err)
			return 2
		}
	}
	// The skeletons are part of the question the graft-render arm is asked, so
	// they are in the fixture digest whenever the case declares them.
	skeletonSrc := ""
	for _, rel := range []string{cf.Skeleton, evolveSkeleton(cf, *evolve)} {
		if rel == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, rel))
		if err != nil {
			fmt.Fprintf(stderr, "graft: %v\n", err)
			return 2
		}
		skeletonSrc += rel + "\x00" + string(b) + "\x00"
	}
	commit, dirty := gitState()
	modelName := strings.Join(cfg.Model.Argv, " ")
	// An alias like "sonnet" is not a model; record the CLI's version with it.
	if v, err := exec.Command(cfg.Model.Argv[0], "--version").Output(); err == nil {
		modelName += " [" + strings.TrimSpace(strings.SplitN(string(v), "\n", 2)[0]) + "]"
	}

	hc := harness.Config{
		Case: harness.Case{Name: name, Spec: spec, Intent: cf.Intent, Contract: cf.Contract, Conventions: conv,
			Module: cfg.Module, Fixture: harness.Digest(spec, cf.Intent, cf.Contract, oracleSrc, fixtureExtra, skeletonSrc),
			SpecV2: specV2, OracleArgs: oracleArgs, Skeleton: skel1, SkeletonV2: skel2},
		Model:     model.Command{Argv: cfg.Model.Argv, Timeout: time.Duration(cfg.Model.TimeoutSec) * time.Second},
		ModelName: modelName, Gates: gates, Policy: evidence.Policy(cfg.Policy), Tier: cfg.Tier, Attempts: cfg.Attempts,
		OracleArgv: []string{oracleBin}, Commit: commit, Dirty: dirty, Progress: stdout, Transcripts: *transcripts,
		Sedum: sedum, Generators: gens, SedumVersion: sedumVer,
		Visible: visible,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := harness.Run
	if *evolve {
		runner = harness.RunEvolve
	}
	recs, err := runner(ctx, hc, bench.Store{Dir: *dir}, arms, n, res)
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v (%d arm records written)\n", err, len(recs))
		return 3
	}
	st := bench.Store{Dir: *dir}
	all, bad, _ := st.Load(name)
	attrs, _, _ := st.LoadAttributions(name)
	fmt.Fprintln(stdout)
	if err := bench.Report(stdout, name, all, attrs, bad); err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 3
	}
	return 0
}

func evolveSkeleton(cf caseFile, evolve bool) string {
	if !evolve || cf.Evolve == nil {
		return ""
	}
	return cf.Evolve.Skeleton
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// dirSource concatenates a directory's .go files (sorted) for the fixture digest.
func dirSource(dir string) (string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(names) == 0 {
		return "", fmt.Errorf("no Go files in %s", dir)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		src, err := os.ReadFile(n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s\n%s", filepath.Base(n), src)
	}
	return b.String(), nil
}

func gitState() (commit string, dirty bool) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown", true
	}
	st, err := exec.Command("git", "status", "--porcelain").Output()
	return strings.TrimSpace(string(out)), err != nil || len(strings.TrimSpace(string(st))) > 0
}
