package main

import (
	"bytes"
	"strings"
	"testing"

	phloxgw "github.com/robert-mcdermott/phlox-gw"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
)

func TestPrintVersion(t *testing.T) {
	var output bytes.Buffer
	if !printVersion([]string{"--version"}, &output) {
		t.Fatal("printVersion did not handle --version")
	}
	want := "phlox-gw " + phloxgw.Version()
	if got := strings.TrimSpace(output.String()); !strings.HasPrefix(got, want) {
		t.Fatalf("version output = %q, want prefix %q", got, want)
	}
}

func TestPrintVersionIgnoresOtherArguments(t *testing.T) {
	var output bytes.Buffer
	if printVersion(nil, &output) {
		t.Fatal("printVersion handled an empty argument list")
	}
	if printVersion([]string{"--help"}, &output) {
		t.Fatal("printVersion handled --help")
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected output: %q", output.String())
	}
}

func TestApplyBuildDefaultsSetsServiceVersion(t *testing.T) {
	cfg := config.Config{}
	applyBuildDefaults(&cfg)
	if got, want := cfg.Telemetry.ServiceVersion, phloxgw.Version(); got != want {
		t.Fatalf("service version = %q, want %q", got, want)
	}
}

func TestApplyBuildDefaultsPreservesServiceVersionOverride(t *testing.T) {
	cfg := config.Config{Telemetry: config.TelemetryConfig{ServiceVersion: "deployment-override"}}
	applyBuildDefaults(&cfg)
	if got := cfg.Telemetry.ServiceVersion; got != "deployment-override" {
		t.Fatalf("service version = %q, want deployment-override", got)
	}
}
