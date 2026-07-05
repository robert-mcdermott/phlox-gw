package httpapi

import (
	"net/http"
	"strings"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

// Anthropic clients (notably Claude Code) advertise beta features through the
// anthropic-beta request header. The first-party API ignores values it does
// not recognize, but third-party Anthropic endpoints such as Azure AI Foundry
// validate the header and reject the whole request with a 400 when any value
// is unknown to them. The gateway therefore filters the header per provider:
// values are kept only when they match an allowed prefix, so date-suffixed
// beta names (e.g. advisor-tool-2026-03-01) stay covered when Anthropic bumps
// the version date.
//
// Matching is on prefixes rather than full values so the list survives
// Anthropic's date-suffix bumps without a gateway update. A provider can
// override the built-in defaults with its own prefix list; the literal entry
// "*" disables filtering for that provider entirely.

// azureAnthropicBetaPrefixes lists the beta families Azure AI Foundry's
// Anthropic endpoint accepts. Betas that exist only on the first-party API
// (advisor-tool-, claude-code-, fast-mode-, task-budgets-, server-side
// fallbacks, managed-agents-, message-batches-) are intentionally absent and
// get dropped.
var azureAnthropicBetaPrefixes = []string{
	"interleaved-thinking-",
	"fine-grained-tool-streaming-",
	"computer-use-",
	"context-management-",
	"compact-",
	"context-1m-",
	"files-api-",
	"skills-",
	"code-execution-",
	"mcp-client-",
	"token-efficient-tools-",
	"token-counting-",
	"output-128k-",
	"pdfs-",
	"prompt-caching-",
	"extended-cache-ttl-",
}

// defaultBetaPrefixAllowlists maps provider types to their built-in beta
// header allowlist. A missing entry (e.g. type "anthropic") means the header
// passes through unfiltered unless the provider sets its own prefix list.
var defaultBetaPrefixAllowlists = map[string][]string{
	"azure-anthropic": azureAnthropicBetaPrefixes,
}

// betaPrefixesFor resolves the effective allowlist for a provider: the
// per-provider override when set, otherwise the built-in default for its
// type. A nil result means "do not filter".
func betaPrefixesFor(p store.Provider) []string {
	if override := splitBetaPrefixes(p.BetaHeaderPrefixes); len(override) > 0 {
		return override
	}
	return defaultBetaPrefixAllowlists[p.Type]
}

// splitBetaPrefixes parses a stored override list. Prefixes are separated by
// newlines or commas; blank entries are ignored.
func splitBetaPrefixes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	var prefixes []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			prefixes = append(prefixes, f)
		}
	}
	return prefixes
}

// normalizeBetaPrefixes canonicalizes an admin-supplied override list to one
// trimmed prefix per line for storage.
func normalizeBetaPrefixes(raw string) string {
	return strings.Join(splitBetaPrefixes(raw), "\n")
}

// filterAnthropicBetaHeader returns the anthropic-beta header value to send
// upstream for the given provider, keeping only values that match an allowed
// prefix. It returns the client's values untouched when no allowlist applies.
// The dropped slice reports removed values for logging.
func filterAnthropicBetaHeader(p store.Provider, inbound http.Header) (kept string, dropped []string) {
	raw := strings.Join(inbound.Values("anthropic-beta"), ",")
	if raw == "" {
		return "", nil
	}
	prefixes := betaPrefixesFor(p)
	if prefixes == nil {
		return raw, nil
	}
	var keptValues []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if betaValueAllowed(value, prefixes) {
			keptValues = append(keptValues, value)
		} else {
			dropped = append(dropped, value)
		}
	}
	return strings.Join(keptValues, ","), dropped
}

func betaValueAllowed(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix == "*" {
			return true
		}
		if len(value) >= len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// setUpstreamAnthropicBetaHeader applies the per-provider beta filter and
// sets the outgoing header, logging any values that were removed so
// operators can tell why a client-requested beta feature is inactive.
func (s *Server) setUpstreamAnthropicBetaHeader(req *http.Request, p store.Provider, inbound http.Header) {
	kept, dropped := filterAnthropicBetaHeader(p, inbound)
	if len(dropped) > 0 {
		s.logger.Debug("dropped unsupported anthropic-beta values",
			"provider", p.ID, "provider_type", p.Type,
			"dropped", strings.Join(dropped, ","), "kept", kept)
	}
	if kept != "" {
		req.Header.Set("anthropic-beta", kept)
	}
}
