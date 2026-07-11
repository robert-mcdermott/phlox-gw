package phloxgw

import (
	_ "embed"
	"fmt"
	"strings"
)

// embeddedVersion is the source-tree version used by development builds.
// Release builds override BuildVersion through -ldflags using the same VERSION file.
//
//go:embed VERSION
var embeddedVersion string

// BuildVersion, BuildCommit, and BuildDate are populated by the release build scripts.
// They remain variables so the Go linker can set them with -X.
var (
	BuildVersion string
	BuildCommit  string
	BuildDate    string
)

// Version returns the semantic version for this build.
func Version() string {
	if version := strings.TrimSpace(BuildVersion); version != "" {
		return version
	}
	if version := strings.TrimSpace(embeddedVersion); version != "" {
		return version
	}
	return "dev"
}

// VersionString returns the human-readable version printed by phlox-gw --version.
func VersionString() string {
	version := "phlox-gw " + Version()
	details := make([]string, 0, 2)
	if commit := strings.TrimSpace(BuildCommit); commit != "" && commit != "unknown" {
		details = append(details, "commit "+commit)
	}
	if buildDate := strings.TrimSpace(BuildDate); buildDate != "" && buildDate != "unknown" {
		details = append(details, "built "+buildDate)
	}
	if len(details) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(details, ", "))
}
