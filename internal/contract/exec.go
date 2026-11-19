package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds one sedum call when Exec.Timeout is zero. A render is
// pure template work, so a call that takes this long is hung.
const DefaultTimeout = 30 * time.Second

// Exec runs the sedum binary at Bin against the generators directory, for the
// generator package Package. It never imports sedum.
type Exec struct {
	Bin        string
	Generators string
	Package    string
	Ctx        context.Context
	Timeout    time.Duration // per call; DefaultTimeout when zero
}

func (e Exec) run(stdin []byte, args ...string) ([]byte, error) {
	ctx := e.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args = append([]string{"convergence"}, append(args, "--generators", e.Generators, "--package", e.Package)...)
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("sedum %s: no answer within %s", strings.Join(args, " "), timeout)
		}
		return nil, fmt.Errorf("sedum %s: %w: %s", strings.Join(args, " "), err, snippet(errb.Bytes(), out.Bytes()))
	}
	return out.Bytes(), nil
}

// snippet is the start of what a failed call printed, for an error message.
func snippet(parts ...[]byte) string {
	const max = 300
	t := strings.TrimSpace(string(bytes.Join(parts, nil)))
	if len(t) > max {
		t = t[:max] + "..."
	}
	return t
}

// Info is `sedum convergence info`.
func (e Exec) Info() (Info, error) {
	b, err := e.run(nil, "info")
	if err != nil {
		return Info{}, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return Info{}, fmt.Errorf("sedum convergence info: %w (output: %s)", err, snippet(b))
	}
	return i, nil
}

// Render is `sedum convergence render`.
func (e Exec) Render(in Input) (Output, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return Output{}, err
	}
	out, err := e.run(b, "render")
	if err != nil {
		return Output{}, err
	}
	var o Output
	if err := json.Unmarshal(out, &o); err != nil {
		return Output{}, fmt.Errorf("sedum convergence render: %w (output: %s)", err, snippet(out))
	}
	return o, nil
}
