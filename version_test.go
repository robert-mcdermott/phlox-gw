package phloxgw

import (
	"regexp"
	"strings"
	"testing"
)

func TestVersionDefaultsToEmbeddedVersion(t *testing.T) {
	originalVersion := BuildVersion
	BuildVersion = ""
	t.Cleanup(func() { BuildVersion = originalVersion })

	want := strings.TrimSpace(embeddedVersion)
	if got := Version(); got != want {
		t.Fatalf("Version() = %q, want embedded version %q", got, want)
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$`).MatchString(want) {
		t.Fatalf("embedded version %q is not a supported semantic version", want)
	}
}

func TestVersionStringIncludesLinkerMetadata(t *testing.T) {
	originalVersion, originalCommit, originalDate := BuildVersion, BuildCommit, BuildDate
	BuildVersion = "v9.8.7"
	BuildCommit = "abc123"
	BuildDate = "2026-07-10T20:00:00Z"
	t.Cleanup(func() {
		BuildVersion, BuildCommit, BuildDate = originalVersion, originalCommit, originalDate
	})

	got := VersionString()
	for _, want := range []string{"phlox-gw v9.8.7", "commit abc123", "built 2026-07-10T20:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("VersionString() = %q, want it to contain %q", got, want)
		}
	}
}
