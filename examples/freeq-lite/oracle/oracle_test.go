package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The oracle must score the hand-written reference 100%: if it does not,
// the oracle (not the reference) is what is wrong.
func TestOracleScoresReferenceFully(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("build reference: %v\n%s", err, out)
	}
	run := func(c check, extra ...string) {
		t.Run(c.name+fmt.Sprint(extra), func(t *testing.T) {
			addr, stop, err := start([]string{bin}, "", extra...)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := c.run(addr); err != nil {
				t.Fatal(err)
			}
		})
	}
	// v1 reference: every v1 check. v2 reference: every v1 and v2 check.
	for _, c := range checks {
		run(c)
		run(c, "-v2")
	}
	for _, c := range v2Checks {
		run(c, "-v2")
	}
}

func TestServerArgv(t *testing.T) {
	if got := strings.Join(serverArgv([]string{"python3", "main.py"}, "h:1"), " "); got != "python3 main.py -addr h:1" {
		t.Fatal(got)
	}
	if got := strings.Join(serverArgv([]string{"node", "s.js", "--listen={addr}"}, "h:1"), " "); got != "node s.js --listen=h:1" {
		t.Fatal(got)
	}
}
