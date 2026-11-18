// Package lang is graft's per-language profile: the few facts about a target
// language the pipeline needs (file extension, code fence, comment prefix,
// prompt wording, fresh-tree files, and how to build and start the program).
// Everything else in graft is language-agnostic. Go is the default, and a
// zero Profile behaves exactly like it.
package lang

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Profile describes one target language. Strings may use the placeholders
// {module} (the config's module), {out} (the built artifact's path) and
// {addr} (the address the server should listen on).
type Profile struct {
	Name        string            `json:"name"`
	Ext         string            `json:"ext"`          // e.g. ".go", ".py"
	Fence       string            `json:"fence"`        // code-fence language the model answers in
	Comment     string            `json:"comment"`      // line-comment prefix for region markers
	SynthSuffix string            `json:"synth_suffix"` // render mode: <stem><suffix><ext>
	Preamble    string            `json:"preamble"`     // how the answer must start, for the prompt
	Bootstrap   map[string]string `json:"bootstrap"`    // files written once into a fresh tree
	Build       []string          `json:"build"`        // empty: nothing to build
	Run         []string          `json:"run"`          // start the program; "-addr {addr}" appended unless {addr} appears
}

// Go is the default profile: what graft did before profiles existed.
var Go = Profile{
	Name:        "go",
	Ext:         ".go",
	Fence:       "go",
	Comment:     "//",
	SynthSuffix: "_synth",
	Preamble:    "starting with the package clause",
	Bootstrap:   map[string]string{"go.mod": "module {module}\n\ngo 1.22\n"},
	Build:       []string{"go", "build", "-o", "{out}", "."},
	Run:         []string{"{out}"},
}

// ErrProfile: a profile that cannot be used.
var ErrProfile = errors.New("lang: bad language profile")

// Resolve fills every unset field from Go when the profile is entirely unset,
// and otherwise checks the fields a non-Go profile must set itself (no silent
// mix of a Python extension with Go's build command).
func Resolve(p *Profile) (Profile, error) {
	if p == nil {
		return Go, nil
	}
	q := *p
	if q.Name == "" || q.Ext == "" || q.Fence == "" || q.Comment == "" || len(q.Run) == 0 {
		return q, fmt.Errorf("%w: name, ext, fence, comment and run are required", ErrProfile)
	}
	if !strings.HasPrefix(q.Ext, ".") || strings.ContainsAny(q.Ext, "/\\ ") {
		return q, fmt.Errorf("%w: ext %q must look like \".py\"", ErrProfile, q.Ext)
	}
	if q.SynthSuffix == "" {
		q.SynthSuffix = "_synth"
	}
	for name := range q.Bootstrap {
		if path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." {
			return q, fmt.Errorf("%w: bootstrap file %q must be a clean relative path", ErrProfile, name)
		}
	}
	return q, nil
}

// Expand substitutes the placeholders in s.
func Expand(s string, vars map[string]string) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

// ExpandArgv substitutes the placeholders in every argument.
func ExpandArgv(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = Expand(a, vars)
	}
	return out
}

// RunArgv is the command that starts the program listening on addr: Run with
// placeholders expanded, plus "-addr addr" unless Run already places {addr}.
func (p Profile) RunArgv(out, addr string) []string {
	vars := map[string]string{"out": out, "addr": addr}
	argv := ExpandArgv(p.Run, vars)
	for _, a := range p.Run {
		if strings.Contains(a, "{addr}") {
			return argv
		}
	}
	return append(argv, "-addr", addr)
}

// IsSource reports whether a path is a source file of this language.
func (p Profile) IsSource(name string) bool { return strings.HasSuffix(name, p.Ext) }
