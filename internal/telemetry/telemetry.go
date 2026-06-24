package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/robert-mcdermott/phlox-gw/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/robert-mcdermott/phlox-gw"

type Telemetry struct {
	cfg                 config.TelemetryConfig
	logger              *slog.Logger
	registry            *prometheus.Registry
	httpRequests        *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	upstreamRequests    *prometheus.CounterVec
	upstreamDuration    *prometheus.HistogramVec
	upstreamTokens      *prometheus.CounterVec
	upstreamCost        *prometheus.CounterVec
	tracer              trace.Tracer
	tracesEnabled       bool
	tracerProvider      *sdktrace.TracerProvider
	previousTracer      trace.TracerProvider
	previousPropagator  propagation.TextMapPropagator
}

type UpstreamObservation struct {
	ProviderID   string
	ProviderType string
	ModelRoute   string
	Protocol     string
	Status       int
	Latency      time.Duration
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	CostUSD      float64
}

func New(ctx context.Context, cfg config.TelemetryConfig, logger *slog.Logger) (*Telemetry, error) {
	return newWithSpanExporter(ctx, cfg, logger, nil)
}

func newWithSpanExporter(ctx context.Context, cfg config.TelemetryConfig, logger *slog.Logger, exporter sdktrace.SpanExporter) (*Telemetry, error) {
	if logger == nil {
		logger = slog.Default()
	}
	t := &Telemetry{
		cfg:                cfg,
		logger:             logger,
		tracer:             otel.Tracer(tracerName),
		previousTracer:     otel.GetTracerProvider(),
		previousPropagator: otel.GetTextMapPropagator(),
	}
	if cfg.MetricsEnabled {
		t.initMetrics()
	}
	if cfg.TracesEnabled {
		if err := t.initTracing(ctx, exporter); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *Telemetry) initMetrics() {
	t.registry = prometheus.NewRegistry()
	t.registry.MustRegister(prometheus.NewGoCollector())
	t.registry.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	t.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "phlox_gw_http_requests_total",
		Help: "Total inbound HTTP requests handled by Phlox-GW.",
	}, []string{"method", "route", "status"})
	t.httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "phlox_gw_http_request_duration_seconds",
		Help:    "Inbound HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route", "status"})
	t.upstreamRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "phlox_gw_upstream_requests_total",
		Help: "Total upstream model/provider requests dispatched by Phlox-GW.",
	}, []string{"provider_id", "provider_type", "model_route", "protocol", "status"})
	t.upstreamDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "phlox_gw_upstream_request_duration_seconds",
		Help:    "Upstream model/provider request duration in seconds.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"provider_id", "provider_type", "model_route", "protocol", "status"})
	t.upstreamTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "phlox_gw_upstream_tokens_total",
		Help: "Total tokens reported or estimated for upstream model requests.",
	}, []string{"provider_id", "provider_type", "model_route", "protocol", "direction"})
	t.upstreamCost = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "phlox_gw_upstream_cost_usd_total",
		Help: "Total estimated upstream model cost in USD.",
	}, []string{"provider_id", "provider_type", "model_route", "protocol"})
	t.registry.MustRegister(
		t.httpRequests,
		t.httpRequestDuration,
		t.upstreamRequests,
		t.upstreamDuration,
		t.upstreamTokens,
		t.upstreamCost,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "phlox_gw_build_info",
			Help:        "Phlox-GW build and service metadata.",
			ConstLabels: prometheus.Labels{"service": fallback(t.cfg.ServiceName, "phlox-gw"), "version": t.cfg.ServiceVersion},
		}, func() float64 { return 1 }),
	)
}

func (t *Telemetry) initTracing(ctx context.Context, exporter sdktrace.SpanExporter) error {
	injectedExporter := exporter != nil
	if exporter == nil {
		opts := []otlptracehttp.Option{}
		if t.cfg.OTLPEndpointURL != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(t.cfg.OTLPEndpointURL))
		}
		if t.cfg.OTLPInsecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		otlpExporter, err := otlptracehttp.New(ctx, opts...)
		if err != nil {
			return err
		}
		exporter = otlpExporter
	}
	ratio := t.cfg.SampleRatio
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes("",
			attribute.String("service.name", fallback(t.cfg.ServiceName, "phlox-gw")),
			attribute.String("service.version", t.cfg.ServiceVersion),
		),
	)
	if err != nil {
		return err
	}
	processor := sdktrace.WithBatcher(exporter)
	if injectedExporter {
		processor = sdktrace.WithSyncer(exporter)
	}
	t.tracerProvider = sdktrace.NewTracerProvider(
		processor,
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(t.tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.tracer = t.tracerProvider.Tracer(tracerName)
	t.tracesEnabled = true
	return nil
}

func (t *Telemetry) MetricsEnabled() bool {
	return t != nil && t.cfg.MetricsEnabled && t.registry != nil
}

func (t *Telemetry) MetricsPath() string {
	if t == nil || t.cfg.MetricsPath == "" {
		return "/metrics"
	}
	return t.cfg.MetricsPath
}

func (t *Telemetry) MetricsHandler() http.Handler {
	if !t.MetricsEnabled() {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(t.registry, promhttp.HandlerOpts{})
}

func (t *Telemetry) ObserveHTTPRequest(method, route string, status int, duration time.Duration) {
	if !t.MetricsEnabled() {
		return
	}
	labels := []string{method, fallback(route, "unknown"), strconv.Itoa(status)}
	t.httpRequests.WithLabelValues(labels...).Inc()
	t.httpRequestDuration.WithLabelValues(labels...).Observe(duration.Seconds())
}

func (t *Telemetry) ObserveUpstream(obs UpstreamObservation) {
	if !t.MetricsEnabled() {
		return
	}
	status := strconv.Itoa(obs.Status)
	providerID := fallback(obs.ProviderID, "unknown")
	providerType := fallback(obs.ProviderType, "unknown")
	modelRoute := fallback(obs.ModelRoute, "unknown")
	protocol := fallback(obs.Protocol, "unknown")
	t.upstreamRequests.WithLabelValues(providerID, providerType, modelRoute, protocol, status).Inc()
	if obs.Latency > 0 {
		t.upstreamDuration.WithLabelValues(providerID, providerType, modelRoute, protocol, status).Observe(obs.Latency.Seconds())
	}
	if obs.InputTokens > 0 {
		t.upstreamTokens.WithLabelValues(providerID, providerType, modelRoute, protocol, "input").Add(float64(obs.InputTokens))
	}
	if obs.OutputTokens > 0 {
		t.upstreamTokens.WithLabelValues(providerID, providerType, modelRoute, protocol, "output").Add(float64(obs.OutputTokens))
	}
	if obs.TotalTokens > 0 {
		t.upstreamTokens.WithLabelValues(providerID, providerType, modelRoute, protocol, "total").Add(float64(obs.TotalTokens))
	}
	if obs.CostUSD > 0 {
		t.upstreamCost.WithLabelValues(providerID, providerType, modelRoute, protocol).Add(obs.CostUSD)
	}
}

func (t *Telemetry) StartHTTPRequest(ctx context.Context, method, path string) (context.Context, trace.Span) {
	if t == nil || !t.tracesEnabled {
		return ctx, noopSpan()
	}
	return t.tracer.Start(ctx, fmt.Sprintf("HTTP %s", method), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("http.request.method", method), attribute.String("url.path", path)))
}

func (t *Telemetry) StartHTTPRequestFromHeaders(ctx context.Context, headers http.Header, method, path string) (context.Context, trace.Span) {
	if t == nil || !t.tracesEnabled {
		return ctx, noopSpan()
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(headers))
	return t.tracer.Start(ctx, fmt.Sprintf("HTTP %s", method), trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("http.request.method", method), attribute.String("url.path", path)))
}

func (t *Telemetry) FinishHTTPRequest(span trace.Span, method, route string, status int, duration time.Duration) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", fallback(route, "unknown")),
		attribute.Int("http.response.status_code", status),
		attribute.Float64("phlox_gw.duration_ms", float64(duration.Milliseconds())),
	)
	if status >= 500 {
		span.SetStatus(codes.Error, http.StatusText(status))
	}
	span.End()
}

func (t *Telemetry) StartUpstream(ctx context.Context, providerID, providerType, modelRoute, upstreamModel, protocol, operation string) (context.Context, trace.Span) {
	if t == nil || !t.tracesEnabled {
		return ctx, noopSpan()
	}
	return t.tracer.Start(ctx, "llm.upstream "+fallback(operation, "request"), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("phlox_gw.provider_id", providerID),
			attribute.String("phlox_gw.provider_type", providerType),
			attribute.String("phlox_gw.model_route", modelRoute),
			attribute.String("phlox_gw.upstream_model_id", upstreamModel),
			attribute.String("phlox_gw.protocol", protocol),
			attribute.String("phlox_gw.operation", operation),
		))
}

func (t *Telemetry) FinishUpstream(span trace.Span, status int, errText string, latency time.Duration) {
	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.Int("http.response.status_code", status),
		attribute.Float64("phlox_gw.latency_ms", float64(latency.Milliseconds())),
	)
	if status >= 500 || errText != "" && status >= 400 {
		span.SetStatus(codes.Error, errText)
	}
	span.End()
}

func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil || t.tracerProvider == nil {
		return nil
	}
	otel.SetTracerProvider(t.previousTracer)
	otel.SetTextMapPropagator(t.previousPropagator)
	return t.tracerProvider.Shutdown(ctx)
}

func fallback(value, fallbackValue string) string {
	if value == "" {
		return fallbackValue
	}
	return value
}

func noopSpan() trace.Span {
	return trace.SpanFromContext(context.Background())
}
