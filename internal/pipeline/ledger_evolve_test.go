package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/evidence"
)

// What does graft regenerate when ledger-lite's spec changes to spec-v2?
//
// The change edits two sections (Freezing, Journal) whose statements do not
// name the code they affect: the fix lives in the core (Post, the Entry type)
// and in the Deposits and Transfers code. These tests drive the real pipeline
// with fake models over the real case files and pin down which regions each
// arm asks a model to write in the change step. They are the executable form
// of the claim in examples/ledger-lite/README.md; they say nothing about what
// a real model does with those regions.

const llDir = "../../examples/ledger-lite/"

var declaredFunc = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)

// declModel is echoModel that also writes every helper the prompt declares and
// the file lacks (a model asked for a new stub's body writes it): a trivial body
// that compiles.
type declModel struct{ *echoModel }

func (m declModel) Complete(ctx context.Context, prompt string) (string, error) {
	out, err := m.echoModel.Complete(ctx, prompt)
	if err != nil {
		return "", err
	}
	if i := strings.Index(prompt, "## Declares:"); i >= 0 {
		block := prompt[i:]
		block = block[strings.Index(block, "```go\n")+6:]
		block = block[:strings.Index(block, "```")]
		have := map[string]bool{}
		for _, g := range declaredFunc.FindAllStringSubmatch(out, -1) {
			have[g[1]] = true
		}
		var add strings.Builder
		for _, line := range strings.Split(block, "\n") {
			if g := declaredFunc.FindStringSubmatch(line); g != nil && !have[g[1]] {
				add.WriteString("\n" + line + " { panic(\"echo body\") }\n")
			}
		}
		out = strings.Replace(out, "\n// converged\n```", add.String()+"\n// converged\n```", 1)
	}
	return out, nil
}

var wholeFile = regexp.MustCompile("Write the complete file `([^`]+)`")

// fileModel answers a graft-arm (no skeleton) prompt with an empty file that
// compiles, and records which file each call was for.
type fileModel struct{ files []string }

func (m *fileModel) Complete(_ context.Context, prompt string) (string, error) {
	g := wholeFile.FindStringSubmatch(prompt)
	if g == nil {
		return "", os.ErrInvalid
	}
	m.files = append(m.files, g[1])
	if g[1] == "running.go" {
		return "```go\npackage main\n\nfunc main() {}\n```\n", nil
	}
	return "```go\npackage main\n```\n", nil
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// Without a skeleton: only the records whose statements changed are written
// again, and the files that hold the core (running.go) and the code of
// Deposits and Transfers are not among them.
func TestLedgerEvolveGraftArmRegeneratesOnlyChangedSections(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs go")
	}
	spec1, _ := os.ReadFile(llDir + "spec/ledger-lite.md")
	spec2, _ := os.ReadFile(llDir + "spec-v2/ledger-lite.md")
	conv, _ := os.ReadFile(llDir + "conventions.md")
	repo, root := t.TempDir(), filepath.Join(t.TempDir(), "app")
	os.MkdirAll(filepath.Join(repo, ".graft"), 0o755)
	log, err := evidence.Open(filepath.Join(repo, ".graft", "evidence.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := &fileModel{}
	step := func(spec string) {
		t.Helper()
		acc, err := AcceptWith(repo, drafter.Drafts(spec), "test", time.Now(), AcceptOptions{Ext: ".go", Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), Config{
			Repo: repo, Root: root, Module: "ledgerlite", Spec: spec, Conventions: string(conv), Tier: "low",
			Model: m, Log: log, Policy: evidence.Policy{"low": {evidence.StaticAnalysis}},
			Gates: []converge.Gate{converge.CommandGate{Label: "go build", Argv: []string{"go", "build", "./..."}, As: evidence.StaticAnalysis, Timeout: 2 * time.Minute}},
		}, acc); err != nil {
			t.Fatal(err)
		}
	}
	step(string(spec1))
	if len(m.files) != 7 {
		t.Fatalf("step 1 wrote %v, want the 7 sections' files", m.files)
	}
	m.files = nil
	step(string(spec2))
	want := []string{"freezing.go", "journal.go"}
	if got := sorted(m.files); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the change step wrote %v, want %v", got, want)
	}
}

// With a skeleton: the shape of Running changed (Entry.Balance, Post's doc, and
// with the full skeleton the new Creditable stub), so Running is converged
// again as well, which is the one place the journal's balance can be added.
// Deposits and Transfers are not regenerated in either variant: their records
// are unchanged, and their regions hold only the routes' init. So even with the
// Creditable stub in the skeleton, nothing asks a model to call it from the
// deposit and transfer code.
func TestLedgerEvolveRenderArmRegenerates(t *testing.T) {
	for _, c := range []struct {
		skeleton string
		want     []string
	}{
		{"skeleton-v2.json", []string{"freezing_synth.go", "journal_synth.go", "running_synth.go"}},
		{"skeleton-v2-nocredit.json", []string{"freezing_synth.go", "journal_synth.go", "running_synth.go"}},
	} {
		t.Run(c.skeleton, func(t *testing.T) {
			cfg, m, spec1 := renderSetupIn(t, llDir, "ledgerlite", "skeleton.json", "spec/ledger-lite.md")
			if _, err := Run(context.Background(), cfg, accept(t, cfg, spec1)); err != nil {
				t.Fatal(err)
			}
			if len(m.paths) != 7 {
				t.Fatalf("step 1 wrote %v, want 7 regions", m.paths)
			}
			before := snapshot(t, cfg.Root)
			cfg2, _, spec2 := renderSetupIn(t, llDir, "ledgerlite", c.skeleton, "spec-v2/ledger-lite.md")
			cfg2.Repo, cfg2.Root, cfg2.Log, cfg2.Spec = cfg.Repo, cfg.Root, cfg.Log, spec2
			m.prompts, m.paths = nil, nil
			cfg2.Model = declModel{m}
			if _, err := Run(context.Background(), cfg2, accept(t, cfg2, spec2)); err != nil {
				t.Fatal(err)
			}
			if got := sorted(m.paths); strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("the change step wrote %v, want %v", got, c.want)
			}
			after := snapshot(t, cfg.Root)
			var kept, changed []string
			for k, v := range before {
				if after[k] == v {
					kept = append(kept, k)
				} else {
					changed = append(changed, k)
				}
			}
			t.Logf("changed files: %v", sorted(changed))
			t.Logf("byte-identical: %v", sorted(kept))
		})
	}
}
