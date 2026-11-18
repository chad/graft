// Command graft regenerates software from versioned intent.
//
// Three layers (see SPEC.md):
//   - records     — provenance records are the source of truth (github.com/livecodelife/provenance/record)
//   - rendering   — sedum, invoked as a binary, renders deterministically into owned regions
//   - convergence — open synthesis in declared regions, gated by evals and risk-tiered evidence
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chad/graft/internal/drafter"
	"github.com/chad/graft/internal/version"
)

func main() {
	os.Exit(run2(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the testable entry point without stdin.
func run(args []string, stdout, stderr io.Writer) int {
	return run2(args, strings.NewReader(""), stdout, stderr)
}

// run2 is the testable entry point: it returns the process exit code.
func run2(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "draft":
		return draft(args[1:], stdout, stderr)
	case "converge":
		return convergeCmd(args[1:], stdin, stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "graft: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: graft <command>

commands:
  draft F     propose draft records from a markdown spec (never applies them)
  converge    check a synthesized body through gates and tier policy; apply only if satisfied
  version   print graft's version
  help      show this help

Layers under construction: records (M1), rendering (M2), evidence (M3),
drafter (M4), convergence (M5), bench (M6). See SPEC.md.
`)
}

// draft prints the drafter's proposals for a spec. It writes nothing.
func draft(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: graft draft <spec.md>")
		return 2
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "graft: %v\n", err)
		return 1
	}
	for _, p := range drafter.Map(drafter.Drafts(string(b)), nil) {
		d := p.Draft
		if p.Kind == drafter.NoProposal {
			// Not shown unless it validates (SPEC §8): only the reason.
			fmt.Fprintf(stdout, "## %s — no confident proposal: %s\n\n", d.Section, p.Reason)
			continue
		}
		fmt.Fprintf(stdout, "## %s — %s (%s)\n", d.Section, p.Kind, p.Reason)
		fmt.Fprintf(stdout, "receipt: %v — %s\n", d.Receipt.Validated, d.Receipt.Reason)
		for _, st := range d.Statements {
			fmt.Fprintf(stdout, "- [%s %.2f] L%d %s\n", st.Type, st.Confidence, st.Line, st.Text)
		}
		for _, st := range d.Context {
			fmt.Fprintf(stdout, "- [context, not drafted] L%d %s\n", st.Line, st.Text)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}
