package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintStartupBanner(t *testing.T) {
	var output bytes.Buffer
	printStartupBanner(&output, startupBanner{
		Version:        "v1.2.3",
		Commit:         "abc123",
		BuildDate:      "2026-07-10T20:00:00Z",
		GoVersion:      "go1.26.5",
		Platform:       "darwin/arm64",
		PID:            1234,
		Address:        "127.0.0.1:8080",
		DeploymentMode: "single-sqlite",
		InstanceID:     "node-1",
		DatabaseDriver: "sqlite",
		DatabaseTarget: "/var/lib/phlox-gw/phlox-gw.db",
	}, false)

	text := output.String()
	for _, want := range []string{
		"PHLOX-GW", "v1.2.3", "Enterprise LLM gateway", "commit abc123",
		"built 2026-07-10T20:00:00Z", "go1.26.5", "darwin/arm64", "pid 1234",
		"http://127.0.0.1:8080/", "single-sqlite", "node-1", "sqlite",
		"/var/lib/phlox-gw/phlox-gw.db", "(o)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("startup banner does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("plain startup banner contains ANSI escapes: %q", text)
	}
	if strings.Contains(text, "FIRST-RUN ADMINISTRATOR") {
		t.Fatalf("ordinary startup banner contains bootstrap section:\n%s", text)
	}
}

func TestPrintStartupBannerIncludesBootstrapCredential(t *testing.T) {
	var output bytes.Buffer
	printStartupBanner(&output, startupBanner{
		Version:           "v0.1.0",
		GoVersion:         "go1.26.5",
		Platform:          "linux/amd64",
		PID:               42,
		Address:           ":8080",
		DeploymentMode:    "single-sqlite",
		InstanceID:        "node-bootstrap",
		DatabaseDriver:    "sqlite",
		DatabaseTarget:    "phlox-gw.db",
		TemporaryPassword: "temporary-password-1234567890",
	}, false)

	text := output.String()
	for _, want := range []string{
		"FIRST-RUN ADMINISTRATOR", "Username", "admin", "Temporary password",
		"temporary-password-1234567890", "shown once", "choose a new password",
		"http://127.0.0.1:8080/",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("bootstrap banner does not contain %q:\n%s", want, text)
		}
	}
}

func TestPrintStartupBannerUsesBrandColorsWhenEnabled(t *testing.T) {
	var output bytes.Buffer
	printStartupBanner(&output, startupBanner{Version: "v0.1.0"}, true)
	text := output.String()
	for _, want := range []string{"\x1b[38;2;223;0;255m", "\x1b[38;2;54;215;255m", "\x1b[0m"} {
		if !strings.Contains(text, want) {
			t.Fatalf("colored banner does not contain %q: %q", want, text)
		}
	}
}

func TestDashboardURL(t *testing.T) {
	for _, test := range []struct {
		address string
		want    string
	}{
		{address: "127.0.0.1:8080", want: "http://127.0.0.1:8080/"},
		{address: ":8080", want: "http://127.0.0.1:8080/"},
		{address: "[::1]:8080", want: "http://[::1]:8080/"},
	} {
		if got := dashboardURL(test.address); got != test.want {
			t.Errorf("dashboardURL(%q) = %q, want %q", test.address, got, test.want)
		}
	}
}

func TestBuildSummaryForDevelopmentBuild(t *testing.T) {
	if got := buildSummary(startupBanner{Commit: "unknown", BuildDate: "unknown"}); got != "development build" {
		t.Fatalf("buildSummary = %q, want development build", got)
	}
}

func TestSafeBannerTextRemovesTerminalControls(t *testing.T) {
	if got := safeBannerText("node\n\x1b[31m"); got != "node??[31m" {
		t.Fatalf("safeBannerText = %q, want terminal controls replaced", got)
	}
}
