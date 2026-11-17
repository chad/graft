package version

import (
	"regexp"
	"testing"
)

func TestVersionIsBareSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("Version %q is not a bare semver", Version)
	}
}
