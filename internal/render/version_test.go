package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckVersion(t *testing.T) {
	for v, ok := range map[string]bool{
		"0.14.0": true, "0.14.1": true, "0.15.0": true, "1.0.0": true, "v0.14.0": true, "0.14.0\n": true, "0.14.0-rc1": true,
		"0.13.0": false, "0.12.2": false, "0.9.9": false, "0.13.9": false, "": false, "fake": false, "0.14": false,
	} {
		err := CheckVersion(v)
		if ok && err != nil {
			t.Errorf("%q refused: %v", v, err)
		}
		if !ok && !errors.Is(err, ErrSedumTooOld) {
			t.Errorf("%q: err %v, want ErrSedumTooOld", v, err)
		}
	}
}

// A sedum 0.13 on PATH is refused with a version error that names both
// versions, before anything is rendered.
func TestOpenRefusesAnOldSedum(t *testing.T) {
	for ver, wantOK := range map[string]bool{"0.13.0": false, "0.14.0": true} {
		bin := filepath.Join(t.TempDir(), "sedum")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho "+ver+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), bin)
		if wantOK != (err == nil) || (wantOK && s == nil) {
			t.Fatalf("sedum %s: Open = %v, %v", ver, s, err)
		}
		if !wantOK {
			if !errors.Is(err, ErrSedumTooOld) {
				t.Errorf("sedum %s: %v", ver, err)
			}
			for _, w := range []string{"0.14.0", "0.13.0"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error %q does not name %s", err, w)
				}
			}
		}
	}
}
