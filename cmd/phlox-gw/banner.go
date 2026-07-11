package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"

	phloxgw "github.com/robert-mcdermott/phlox-gw"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
)

const bannerDivider = "------------------------------------------------------------------------"

var phloxTerminalLogo = []string{
	`       _._       `,
	`    .-' | '-.    `,
	`   / \  |  / \   `,
	`  |--- (o) ---|  `,
	`   \ / /|\ \ /   `,
	`    '-._|_.-'    `,
	`       /_\       `,
}

type startupBanner struct {
	Version           string
	Commit            string
	BuildDate         string
	GoVersion         string
	Platform          string
	PID               int
	Address           string
	DeploymentMode    string
	InstanceID        string
	DatabaseDriver    string
	DatabaseTarget    string
	TemporaryPassword string
}

type bannerPalette struct {
	magenta string
	cyan    string
	muted   string
	gold    string
	bold    string
	reset   string
}

func newStartupBanner(cfg config.Config, temporaryPassword string) startupBanner {
	return startupBanner{
		Version:           phloxgw.Version(),
		Commit:            valueOrUnknown(phloxgw.BuildCommit),
		BuildDate:         valueOrUnknown(phloxgw.BuildDate),
		GoVersion:         runtime.Version(),
		Platform:          runtime.GOOS + "/" + runtime.GOARCH,
		PID:               os.Getpid(),
		Address:           cfg.Addr,
		DeploymentMode:    cfg.Deployment.Mode,
		InstanceID:        cfg.Deployment.InstanceID,
		DatabaseDriver:    cfg.Database.Driver,
		DatabaseTarget:    databaseLogTarget(cfg),
		TemporaryPassword: temporaryPassword,
	}
}

func printStartupBanner(w io.Writer, banner startupBanner, color bool) {
	palette := terminalPalette(color)
	info := []string{
		style(palette, palette.bold+palette.magenta, "PHLOX-GW") + "  " + style(palette, palette.bold+palette.cyan, safeBannerText(banner.Version)),
		style(palette, palette.muted, "Enterprise LLM gateway"),
		bannerDetail(palette, "Build", buildSummary(banner)),
		bannerDetail(palette, "Runtime", strings.Join([]string{safeBannerText(banner.GoVersion), safeBannerText(banner.Platform), "pid " + strconv.Itoa(banner.PID)}, "  ·  ")),
		bannerDetail(palette, "Dashboard", safeBannerText(dashboardURL(banner.Address))),
		bannerDetail(palette, "Deployment", safeBannerText(strings.TrimSpace(banner.DeploymentMode))+"  ·  "+safeBannerText(strings.TrimSpace(banner.InstanceID))),
		bannerDetail(palette, "Database", safeBannerText(strings.TrimSpace(banner.DatabaseDriver))+"  ·  "+safeBannerText(strings.TrimSpace(banner.DatabaseTarget))),
	}

	_, _ = fmt.Fprintln(w)
	for i, logoLine := range phloxTerminalLogo {
		_, _ = fmt.Fprintf(w, "  %s    %s\n", style(palette, palette.magenta, logoLine), info[i])
	}

	if strings.TrimSpace(banner.TemporaryPassword) != "" {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "  "+style(palette, palette.cyan, bannerDivider))
		_, _ = fmt.Fprintln(w, "  "+style(palette, palette.bold+palette.gold, "FIRST-RUN ADMINISTRATOR"))
		_, _ = fmt.Fprintln(w, "  "+bannerDetail(palette, "Username", "admin"))
		_, _ = fmt.Fprintln(w, "  "+bannerDetail(palette, "Temporary password", style(palette, palette.bold+palette.gold, safeBannerText(banner.TemporaryPassword))))
		_, _ = fmt.Fprintln(w, "  "+bannerDetail(palette, "Next step", "Sign in and choose a new password before continuing."))
		_, _ = fmt.Fprintln(w, "  "+style(palette, palette.muted, "This password is shown once. Store first-run logs safely."))
		_, _ = fmt.Fprintln(w, "  "+style(palette, palette.cyan, bannerDivider))
	}
	_, _ = fmt.Fprintln(w)
}

func buildSummary(banner startupBanner) string {
	commit := safeBannerText(strings.TrimSpace(banner.Commit))
	buildDate := safeBannerText(strings.TrimSpace(banner.BuildDate))
	commitKnown := commit != "" && commit != "unknown"
	buildDateKnown := buildDate != "" && buildDate != "unknown"
	if !commitKnown && !buildDateKnown {
		return "development build"
	}
	parts := make([]string, 0, 2)
	if commitKnown {
		parts = append(parts, "commit "+commit)
	}
	if buildDateKnown {
		parts = append(parts, "built "+buildDate)
	}
	return strings.Join(parts, "  ·  ")
}

func dashboardURL(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return "unknown"
	}
	host, port, err := net.SplitHostPort(address)
	if err == nil {
		if host == "" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port) + "/"
	}
	return "http://" + strings.TrimRight(address, "/") + "/"
}

func bannerDetail(palette bannerPalette, label, value string) string {
	return style(palette, palette.muted, fmt.Sprintf("%-20s", label)) + value
}

func safeBannerText(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return '?'
		}
		return r
	}, value)
}

func terminalPalette(enabled bool) bannerPalette {
	if !enabled {
		return bannerPalette{}
	}
	return bannerPalette{
		magenta: "\x1b[38;2;223;0;255m",
		cyan:    "\x1b[38;2;54;215;255m",
		muted:   "\x1b[38;2;182;159;201m",
		gold:    "\x1b[38;2;255;209;102m",
		bold:    "\x1b[1m",
		reset:   "\x1b[0m",
	}
}

func style(palette bannerPalette, code, text string) string {
	if palette.reset == "" || code == "" {
		return text
	}
	return code + text + palette.reset
}

func terminalColorsEnabled(w io.Writer) bool {
	if forced, ok := os.LookupEnv("FORCE_COLOR"); ok {
		return forced != "" && forced != "0"
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("CLICOLOR") == "0" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
