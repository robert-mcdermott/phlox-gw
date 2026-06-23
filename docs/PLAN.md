# Phlox-GW Plan

## Product Scope

Phlox-GW should be the gateway and governance product extracted from the broader Phlox
platform. It should not start as a chat assistant, agent harness, RAG system, or code
execution environment. Those Phlox features are useful reference points, but this product
is strongest when it is narrow, fast, auditable, and easy to deploy.

## What To Keep From Phlox

- Local accounts, roles, and the Entra ID/OIDC SSO seam.
- Admin/user split with strict user-owned API keys.
- Provider profiles and live model catalog management.
- OpenAI-compatible gateway surface.
- Per-model input/output token pricing.
- Durable usage ledger that snapshots username and department at request time.
- Departmental chargeback reports and CSV export.
- Monthly user and department budgets with warnings and hard blocking.
- Phlox visual language: dark operational UI, semantic theme tokens, compact admin
  panels, and finance-friendly cost tables.

## What To Leave Behind Initially

- Chat UI, conversation history, message editing, and regeneration.
- Agentic tool loop, MCP tool execution, RAG, memory, and workspace sandboxing.
- Code execution and artifacts.
- Qdrant and document ingestion.

Those can become adjacent products or later gateway-adjacent capabilities, but they should
not complicate the first gateway release.

## Enterprise Features To Add

- Rate limits by user, key, department, provider, and model.
- Per-key budgets and spend limits in addition to user/department budgets.
- Provider failover policies and weighted load balancing.
- Request policy engine: model allowlists, data classification labels, prompt/response
  guardrails, and optional PII redaction.
- Audit log for admin actions and key lifecycle events.
- Prometheus metrics and OpenTelemetry traces.
- Request log search with latency, token, status, and provider error dimensions.
- Provider health checks and circuit breakers.
- Secret management through environment references first, then external vaults.
- SCIM or Graph sync for departments/groups after Entra ID SSO.
- Multi-node mode with Postgres or another shared database after SQLite reaches its limit.
- Signed config export/import for regulated environments.

## Implementation Phases

### Phase 1: Gateway Foundation

- Build a single Go binary that serves an embedded dashboard and HTTP API.
- Store users, API keys, providers, models, budgets, and usage in SQLite.
- Implement local auth, admin bootstrap, user-owned API key minting, and model catalog.
- Implement OpenAI-compatible and Anthropic-compatible pass-through routes.
- Record usage and cost from provider response metadata when available.
- Enforce user and department monthly budgets before dispatch.

### Phase 2: Production Controls

- Add Entra ID/OIDC login.
- Add API key expiration, rotation, scoped model allowlists, and per-key budgets.
- Add provider health checks, retries, circuit breakers, and weighted routing.
- Add request and response audit logs without storing prompt content by default.
- Add CSV exports and richer dashboard charts.

### Phase 3: Enterprise Operations

- Add Prometheus metrics and OpenTelemetry tracing.
- Add guardrails and policy plugins.
- Add semantic cache as an optional cost/latency optimization.
- Add cluster-safe database option and migration tooling.
- Add secrets backends and signed configuration bundles.

