package converge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/chad/graft/internal/evidence"
)

// Gate is one execution check run against a candidate tree.
type Gate interface {
	Name() string
	Kind() evidence.Kind
	// Run checks the tree at dir. An error means the gate could not run at all.
	Run(ctx context.Context, dir string) (evidence.Verdict, string, error)
}

// CommandGate runs a command in the candidate tree: exit 0 passes, any other
// exit fails, a timeout is inconclusive. argv is executed directly, no shell.
type CommandGate struct {
	Label   string
	Argv    []string
	As      evidence.Kind // e.g. evidence.TestRun or evidence.Eval
	Timeout time.Duration // 0 means 5 minutes
}

func (g CommandGate) Name() string        { return g.Label }
func (g CommandGate) Kind() evidence.Kind { return g.As }

func (g CommandGate) Run(ctx context.Context, dir string) (evidence.Verdict, string, error) {
	if len(g.Argv) == 0 {
		return "", "", errors.New("converge: command gate has no argv")
	}
	to := g.Timeout
	if to == 0 {
		to = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.Argv[0], g.Argv[1:]...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	detail := tail(out.String(), 2000)
	var exit *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return evidence.Inconclusive, fmt.Sprintf("timed out after %s\n%s", to, detail), nil
	case err == nil:
		return evidence.Pass, detail, nil
	case errors.As(err, &exit):
		return evidence.Fail, fmt.Sprintf("exit %d\n%s", exit.ExitCode(), detail), nil
	default:
		return "", "", fmt.Errorf("converge: gate %s: %w", g.Label, err)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
