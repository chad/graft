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

	"github.com/chad/graft/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point: it returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version.Version)
		return 0
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
  version   print graft's version
  help      show this help

Layers under construction: records (M1), rendering (M2), evidence (M3),
drafter (M4), convergence (M5), bench (M6). See SPEC.md.
`)
}
