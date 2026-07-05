package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

func betaHeader(values ...string) http.Header {
	h := http.Header{}
	for _, v := range values {
		h.Add("anthropic-beta", v)
	}
	return h
}

func TestFilterAnthropicBetaHeaderPassthroughFirstParty(t *testing.T) {
	p := store.Provider{ID: "anthropic", Type: "anthropic"}
	raw := "advisor-tool-2026-03-01,claude-code-20250219,interleaved-thinking-2025-05-14"
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader(raw))
	if kept != raw {
		t.Fatalf("first-party provider should pass through unfiltered, got %q", kept)
	}
	if len(dropped) != 0 {
		t.Fatalf("first-party provider should drop nothing, dropped %v", dropped)
	}
}

func TestFilterAnthropicBetaHeaderAzureAnthropicDefaults(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic"}
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader(
		"advisor-tool-2026-03-01, interleaved-thinking-2025-05-14 ,claude-code-20250219,fine-grained-tool-streaming-2025-05-14"))
	if kept != "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14" {
		t.Fatalf("kept = %q", kept)
	}
	want := []string{"advisor-tool-2026-03-01", "claude-code-20250219"}
	if strings.Join(dropped, "|") != strings.Join(want, "|") {
		t.Fatalf("dropped = %v, want %v", dropped, want)
	}
}

func TestFilterAnthropicBetaHeaderFutureDateSuffixStillDropped(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic"}
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader("advisor-tool-2026-09-01"))
	if kept != "" || len(dropped) != 1 {
		t.Fatalf("date-bumped advisor beta should still be dropped, kept=%q dropped=%v", kept, dropped)
	}
}

func TestFilterAnthropicBetaHeaderProviderOverride(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic", BetaHeaderPrefixes: "advisor-tool-\ninterleaved-thinking-"}
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader(
		"advisor-tool-2026-03-01,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"))
	if kept != "advisor-tool-2026-03-01,interleaved-thinking-2025-05-14" {
		t.Fatalf("override should replace the defaults, kept = %q", kept)
	}
	if len(dropped) != 1 || dropped[0] != "fine-grained-tool-streaming-2025-05-14" {
		t.Fatalf("dropped = %v", dropped)
	}
}

func TestFilterAnthropicBetaHeaderWildcardOverride(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic", BetaHeaderPrefixes: "*"}
	raw := "advisor-tool-2026-03-01,claude-code-20250219"
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader(raw))
	if kept != raw || len(dropped) != 0 {
		t.Fatalf("wildcard should disable filtering, kept=%q dropped=%v", kept, dropped)
	}
}

func TestFilterAnthropicBetaHeaderOverrideAppliesToFirstParty(t *testing.T) {
	p := store.Provider{ID: "anthropic", Type: "anthropic", BetaHeaderPrefixes: "interleaved-thinking-"}
	kept, dropped := filterAnthropicBetaHeader(p, betaHeader("interleaved-thinking-2025-05-14,advisor-tool-2026-03-01"))
	if kept != "interleaved-thinking-2025-05-14" || len(dropped) != 1 {
		t.Fatalf("explicit override should filter even first-party providers, kept=%q dropped=%v", kept, dropped)
	}
}

func TestFilterAnthropicBetaHeaderMultipleHeaderLines(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic"}
	kept, _ := filterAnthropicBetaHeader(p, betaHeader("interleaved-thinking-2025-05-14", "advisor-tool-2026-03-01,files-api-2025-04-14"))
	if kept != "interleaved-thinking-2025-05-14,files-api-2025-04-14" {
		t.Fatalf("all header lines should be considered, kept = %q", kept)
	}
}

func TestFilterAnthropicBetaHeaderEmpty(t *testing.T) {
	p := store.Provider{ID: "foundry", Type: "azure-anthropic"}
	if kept, dropped := filterAnthropicBetaHeader(p, http.Header{}); kept != "" || dropped != nil {
		t.Fatalf("no inbound header should yield nothing, kept=%q dropped=%v", kept, dropped)
	}
}

func TestNormalizeBetaPrefixes(t *testing.T) {
	got := normalizeBetaPrefixes(" advisor-tool- ,\n\ninterleaved-thinking-\r\n")
	if got != "advisor-tool-\ninterleaved-thinking-" {
		t.Fatalf("normalizeBetaPrefixes = %q", got)
	}
	if normalizeBetaPrefixes("") != "" {
		t.Fatal("empty input should normalize to empty")
	}
}
