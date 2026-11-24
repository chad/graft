package main

import (
	"os/exec"
	"path/filepath"
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
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			addr, stop, err := start([]string{bin}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := runCheck(c, addr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The -v2 oracle must score the v2 reference fully, and must not: every v1
// check that still holds is in the set, so a v2 server that broke one fails.
func TestV2OracleScoresReferenceV2Fully(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference-v2")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference-v2").CombinedOutput(); err != nil {
		t.Fatalf("build reference-v2: %v\n%s", err, out)
	}
	for _, c := range checkSet(true) {
		t.Run(c.name, func(t *testing.T) {
			addr, stop, err := start([]string{bin}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if err := runCheck(c, addr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The v1 reference is a v1 server: under -v2 it must fail exactly the checks
// the revision added or changed (and nothing from the v1 set that still holds).
func TestV2OracleRejectsV1Reference(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	bin := filepath.Join(t.TempDir(), "reference")
	if out, err := exec.Command("go", "build", "-o", bin, "../reference").CombinedOutput(); err != nil {
		t.Fatalf("build reference: %v\n%s", err, out)
	}
	isNew := map[string]bool{}
	for _, c := range v2Checks {
		isNew[c.name] = true
	}
	for _, c := range checkSet(true) {
		addr, stop, err := start([]string{bin}, "")
		if err != nil {
			t.Fatal(err)
		}
		err = runCheck(c, addr)
		stop()
		switch {
		case isNew[c.name] && err == nil:
			t.Errorf("new v2 check %s passed on the v1 reference", c.name)
		case !isNew[c.name] && err != nil:
			t.Errorf("v1 check %s fails on the v1 reference under -v2: %v", c.name, err)
		}
	}
}

func TestV2CheckSetShape(t *testing.T) {
	names := map[string]bool{}
	for _, c := range checkSet(true) {
		if names[c.name] {
			t.Errorf("duplicate check %s", c.name)
		}
		names[c.name] = true
	}
	if names["frozen_still_receives"] || !names["frozen_refuses_incoming"] {
		t.Error("v2 set must swap frozen_still_receives for frozen_refuses_incoming")
	}
	if len(checkSet(true)) != len(checks)-len(v2Dropped)+len(v2Checks) {
		t.Error("v2 set is not v1 minus the dropped plus the new")
	}
}
