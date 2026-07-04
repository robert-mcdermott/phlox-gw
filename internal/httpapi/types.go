package httpapi

import (
	"net/http"

	"github.com/robert-mcdermott/phlox-gw/internal/store"
)

type upstreamResult struct {
	Route     store.RoutedModel
	Protocol  string
	Status    int
	Headers   http.Header
	Body      []byte
	ErrorText string
	LatencyMS int64
}

type requestEventMeta struct {
	Method    string
	Endpoint  string
	Streaming bool
	ClientIP  string
	UserAgent string
}

type modelHealthResult struct {
	OK         bool   `json:"ok"`
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Protocol   string `json:"protocol"`
	StatusCode int    `json:"status_code"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
}
