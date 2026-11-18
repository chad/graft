package converge

import (
	"context"
	"strings"
	"testing"

	"github.com/chad/graft/internal/evidence"
)

// A summary printed first must survive truncation of long failing output.
func TestCommandGateKeepsHeadOfLongOutput(t *testing.T) {
	g := CommandGate{Label: "long", Argv: []string{"sh", "-c",
		`echo "smoke: 3/12 checks passed"; i=0; while [ $i -lt 400 ]; do echo "FAIL check $i"; i=$((i+1)); done; exit 1`}}
	v, detail, err := g.Run(context.Background(), t.TempDir())
	if err != nil || v != evidence.Fail {
		t.Fatalf("%v %v", v, err)
	}
	if !strings.Contains(detail, "smoke: 3/12 checks passed") || !strings.Contains(detail, "FAIL check 399") {
		t.Fatalf("head or tail lost:\n%s", detail)
	}
}
