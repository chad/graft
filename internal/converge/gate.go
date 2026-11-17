package converge

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
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
// On timeout the whole process group is killed, not just the direct child.
type CommandGate struct {
	Label   string
	Argv    []string
	As      evidence.Kind // e.g. evidence.TestRun or evidence.Eval
	Timeout time.Duration // 0 means 5 minutes
}

func (g CommandGate) Name() string        { return g.Label }
func (g CommandGate) Kind() evidence.Kind { return g.As }

// maxOutput caps what a gate's output can cost in memory; the tail is kept.
const maxOutput = 64 << 10

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
	out := &tailWriter{max: maxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	killGroupOnCancel(cmd)
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	detail := out.String()
	if len(detail) > 2000 {
		detail = "…" + detail[len(detail)-2000:]
	}
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

// tailWriter keeps the last max bytes written.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
