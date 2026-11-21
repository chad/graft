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

// ExecCheck runs the profile's decl_check: the decls as JSON on stdin; exit 0
// accepts, exit 1 rejects with the complaints on stderr (returned), anything
// else is an error (the checker itself is broken).
//
// The Go profile's checker is graft's own embedded source (lang.GoDeclCheckSource,
// standard library only). It is written to a temporary directory and run with
// `go run` there, with the module proxy off, so it never needs the network and
// never builds inside the user's module.
func ExecCheck(p lang.Profile) func(ctx context.Context, decls []DeclIn) (string, error) {
	return func(ctx context.Context, decls []DeclIn) (string, error) {
		if len(p.DeclCheck) == 0 {
			return "", errors.New("the profile has no decl_check")
		}
		dir, err := os.MkdirTemp("", "graft-declcheck-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		argv := make([]string, len(p.DeclCheck))
		for i, a := range p.DeclCheck {
			if a == lang.GoDeclCheckVar {
				src := filepath.Join(dir, "main.go")
				if err := os.WriteFile(src, []byte(lang.GoDeclCheckSource), 0o644); err != nil {
					return "", err
				}
				a = src
			}
			argv[i] = a
		}
		in, err := json.Marshal(decls)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(ctx, toolTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local", "GO111MODULE=off")
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
		return "", fmt.Errorf("%s: %v: %s", argv[0], err, strings.TrimSpace(stderr.String()))
	}
}

// ExecBuild runs the profile's build argv in the scratch tree. A failing build
// is not an error: its output is the answer, returned for the model to read.
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
		return "", err
	}
}
