// Package model is graft's only door to a language model. It runs a command
// you choose (claude -p, ollama run, a local wrapper), writes the prompt to
// its stdin and reads the answer from its stdout. graft holds no API keys and
// speaks no vendor API; the command does.
package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Model completes a prompt.
type Model interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// Command is a Model backed by an external program.
type Command struct {
	Argv    []string
	Timeout time.Duration // 0 means 10 minutes
	// Dir is the command's working directory. Empty means a fresh, empty
	// temporary directory, so an agentic model CLI cannot read the repository
	// (where held-out oracles and reference implementations live): the prompt
	// is all it sees.
	Dir string
}

// Complete runs the command with prompt on stdin and returns its stdout.
// A non-zero exit, a timeout or empty output is an error.
func (c Command) Complete(ctx context.Context, prompt string) (string, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return "", errors.New("model: no command")
	}
	to := c.Timeout
	if to == 0 {
		to = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	if cmd.Dir == "" {
		d, err := os.MkdirTemp("", "graft-model-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(d)
		cmd.Dir = d
	}
	cmd.Stdin = strings.NewReader(prompt)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("model: %s timed out after %s", c.Argv[0], to)
		}
		return "", fmt.Errorf("model: %s: %w: %s", c.Argv[0], err, tail(errb.String(), 500))
	}
	if strings.TrimSpace(out.String()) == "" {
		return "", fmt.Errorf("model: %s returned nothing", c.Argv[0])
	}
	return out.String(), nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// ExtractCode returns the last fenced code block tagged lang (or untagged) in
// resp, or the whole response trimmed when there is no fence.
func ExtractCode(resp, lang string) string {
	lines := strings.Split(resp, "\n")
	var blocks []string
	var cur []string
	in, skip := false, false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case skip:
			if t == "```" {
				skip = false
			}
		case !in && strings.HasPrefix(t, "```"):
			tag := strings.TrimSpace(strings.TrimPrefix(t, "```"))
			if tag == "" || strings.EqualFold(tag, lang) {
				in, cur = true, nil
			} else {
				skip = true // a block in another language: skip to its close
			}
		case in && t == "```":
			blocks = append(blocks, strings.Join(cur, "\n"))
			in = false
		case in:
			cur = append(cur, l)
		}
	}
	if len(blocks) > 0 {
		return strings.TrimSpace(blocks[len(blocks)-1]) + "\n"
	}
	return strings.TrimSpace(resp) + "\n"
}
