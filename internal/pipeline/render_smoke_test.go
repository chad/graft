package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chad/graft/internal/converge"
	"github.com/chad/graft/internal/evidence"
)

// recordedModel answers each region with the body a real --render run wrote
// for it (testdata/freeq-render-answers, from a sonnet run that scored 47/47
// on the held-out oracle), so the pipeline can be driven end to end without a
// model.
type recordedModel struct{ calls int }

func (m *recordedModel) Complete(_ context.Context, prompt string) (string, error) {
	m.calls++
	g := askedPath.FindStringSubmatch(prompt)
	if g == nil {
		return "", os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join("testdata", "freeq-render-answers", filepath.Base(g[1])+".txt"))
	if err != nil {
		return "", err
	}
	return "```go\n" + string(b) + "```\n", nil
}

// TestRunRenderWithSmokeGate drives freeq-lite --render end to end with the
// whole-server smoke suite as an eval gate, the configuration that shipped
// broken in #21 (every seeded section's init panicked before the first region
// could pass smoke; fixed in #27). With correct answers every region must
// converge on its first attempt.
func TestRunRenderWithSmokeGate(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	cfg, _, spec := renderSetup(t, "skeleton.json", "spec/freeq-lite.md")
	smoke, err := filepath.Abs(flDir + "smoke/smoke.go")
	if err != nil {
		t.Fatal(err)
	}
	m := &recordedModel{}
	cfg.Model = m
	cfg.Policy = evidence.Policy{"low": {evidence.StaticAnalysis, evidence.Eval}}
	cfg.Gates = append(cfg.Gates, converge.CommandGate{Label: "smoke (visible)", Argv: []string{"go", "run", smoke}, As: evidence.Eval, Timeout: 5 * time.Minute})
	out, err := Run(context.Background(), cfg, accept(t, cfg, spec))
	if err != nil {
		t.Fatalf("%v\n%+v", err, out)
	}
	for _, o := range out {
		if !o.Applied || o.Attempts > 1 {
			t.Errorf("%s: applied=%v attempts=%d last failure: %s", o.Section, o.Applied, o.Attempts, firstLine(o.LastFailure))
		}
	}
	if m.calls != 7 {
		t.Errorf("%d model calls, want 7 (one per region)", m.calls)
	}
	// Every eval verdict must come from a smoke run that actually ran checks.
	for _, e := range cfg.Log.All() {
		if e.Kind == evidence.Eval && (e.Verdict != evidence.Pass || !strings.Contains(e.Detail, "checks passed")) {
			t.Errorf("eval evidence: %s %s", e.Verdict, firstLine(e.Detail))
		}
	}
}
