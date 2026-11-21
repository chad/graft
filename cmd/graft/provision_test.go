package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/chad/graft/internal/sedumtest"
)

// TestMain provisions sedum in CI (the render-mode signoff test drives
// graft run --render); locally those tests skip when sedum is missing.
func TestMain(m *testing.M) {
	if err := sedumtest.ProvisionInCI(); err != nil {
		fmt.Fprintln(os.Stderr, "graft tests: provisioning sedum:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
