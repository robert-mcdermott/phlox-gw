package telemetry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceExport(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tel, err := newWithSpanExporter(context.Background(), config.TelemetryConfig{
		TracesEnabled: true,
		ServiceName:   "phlox-gw-test",
		SampleRatio:   1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), exporter)
	if err != nil {
		t.Fatalf("newWithSpanExporter: %v", err)
	}
	ctx, span := tel.StartHTTPRequestFromHeaders(context.Background(), http.Header{}, http.MethodGet, "/api/health")
	_, upstream := tel.StartUpstream(ctx, "provider", "openai", "route", "model", "openai", "chat.completions")
	tel.FinishUpstream(upstream, http.StatusOK, "", 2*time.Millisecond)
	tel.FinishHTTPRequest(span, http.MethodGet, "/api/health", http.StatusOK, time.Millisecond)
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("exported spans = %d, want 2", len(spans))
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tel.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
