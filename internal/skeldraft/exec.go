package skeldraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chad/graft/internal/lang"
)

const toolTimeout = 5 * time.Minute

// ErrTool: a tool graft depends on (the decl checker, the build gate) could not
// run or is itself broken. That is not a verdict on the proposal, so it is
// never fed back to the model and never retried.
var ErrTool = errors.New("skeldraft: tool failure")

// goEnv pins the environment graft builds and runs its own Go checker in, so
// the user's go settings can neither break it nor reach the network.
var goEnv = []string{"GOFLAGS=", "GOPROXY=off", "GOWORK=off", "GO111MODULE=off", "GOTOOLCHAIN=local"}

// ExecCheck runs the profile's decl_check: the decls as JSON on stdin; exit 0
// accepts, exit 1 rejects with the complaints on stderr (returned), anything
// else is an ErrTool (the checker itself is broken).
//
// The Go profile's checker is graft's own embedded source
// (lang.GoDeclCheckSource, standard library only). It is written to a
// temporary directory and BUILT there with `go build` (a source that does not
// compile is a tool error, not a rejection: `go run` would have reported both
// as exit 1), under a pinned environment with the module proxy off, then the
// binary is run. It never needs the network and never builds inside the user's
// module.
func ExecCheck(p lang.Profile) func(ctx context.Context, decls []DeclIn) (string, error) {
	return func(ctx context.Context, decls []DeclIn) (string, error) {
		if len(p.DeclCheck) == 0 {
			return "", fmt.Errorf("%w: the profile has no decl_check", ErrTool)
		}
		dir, err := os.MkdirTemp("", "graft-declcheck-*")
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrTool, err)
		}
		defer os.RemoveAll(dir)
		argv := append([]string(nil), p.DeclCheck...)
		env := os.Environ()
		if argv[0] == lang.GoDeclCheckVar {
			env = append(env, goEnv...)
			src, bin := filepath.Join(dir, "main.go"), filepath.Join(dir, "declcheck")
			if err := os.WriteFile(src, []byte(lang.GoDeclCheckSource), 0o644); err != nil {
				return "", fmt.Errorf("%w: %v", ErrTool, err)
			}
			bctx, cancel := context.WithTimeout(ctx, toolTimeout)
			defer cancel()
			b := exec.CommandContext(bctx, "go", "build", "-o", bin, src)
			b.Dir, b.Env = dir, env
			if out, err := b.CombinedOutput(); err != nil {
				return "", fmt.Errorf("%w: the Go decl checker did not build: %v: %s", ErrTool, err, strings.TrimSpace(string(out)))
			}
			argv = append([]string{bin}, argv[1:]...)
		}
		in, err := json.Marshal(decls)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrTool, err)
		}
		ctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = dir, env
		cmd.Stdin = bytes.NewReader(in)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err = cmd.Run()
		if err == nil {
			return "", nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && strings.TrimSpace(stderr.String()) != "" {
			return stderr.String(), nil
		}
		return "", fmt.Errorf("%w: %s: %v: %s", ErrTool, argv[0], err, strings.TrimSpace(stderr.String()))
	}
}

// ExecBuild runs the profile's build argv in the scratch tree. A failing build
// is not an error: its output is the answer, returned for the model to read.
// A build that cannot start at all is an ErrTool.
func ExecBuild(p lang.Profile) func(ctx context.Context, root string) (string, error) {
	return func(ctx context.Context, root string) (string, error) {
		if len(p.Build) == 0 {
			return "", nil // nothing to build for this language (an interpreted profile)
		}
		out := filepath.Join(os.TempDir(), fmt.Sprintf("graft-draft-bin-%d", os.Getpid()))
		defer os.Remove(out)
		argv := lang.ExpandArgv(p.Build, map[string]string{"out": out})
		ctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = root
		b, err := cmd.CombinedOutput()
		if err == nil {
			return "", nil
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s, nil
			}
			return ee.Error(), nil
		}
		return "", fmt.Errorf("%w: %s: %v", ErrTool, argv[0], err)
	}
}
