package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Exec runs the sedum binary at Bin against the generators directory, for the
// generator package Package. It never imports sedum.
type Exec struct {
	Bin        string
	Generators string
	Package    string
	Ctx        context.Context
}

func (e Exec) run(stdin []byte, args ...string) ([]byte, error) {
	ctx := e.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	args = append([]string{"convergence"}, append(args, "--generators", e.Generators, "--package", e.Package)...)
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sedum %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()+out.String()))
	}
	return out.Bytes(), nil
}

// Info is `sedum convergence info`.
func (e Exec) Info() (Info, error) {
	b, err := e.run(nil, "info")
	if err != nil {
		return Info{}, err
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return Info{}, fmt.Errorf("sedum convergence info: %w", err)
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
		return Output{}, fmt.Errorf("sedum convergence render: %w", err)
	}
	return o, nil
}
