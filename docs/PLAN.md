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

- Request policy engine: data classification labels, external policy plugins, and richer
  guardrail composition.
- Prometheus metrics and OpenTelemetry traces.
- Secret management through environment references first, then external vaults.
- SCIM or Graph sync for departments/groups after Entra ID SSO.
- Optional Postgres backend and cluster hardening after SQLite reaches its limit. `[implemented]`
- Signed config export for regulated environments. `[implemented]`
- Signed config import/restore workflow.

## Implementation Phases

### Phase 1: Gateway Foundation

- Build a single Go binary that serves an embedded dashboard and HTTP API.
- Store users, API keys, providers, models, budgets, and usage in SQLite.
- Implement local auth, admin bootstrap, user-owned API key minting, and model catalog.
- Implement Entra ID/OIDC browser login with department claim mapping.
- Implement OpenAI-compatible and Anthropic-compatible pass-through routes.
- Implement Anthropic-compatible streaming pass-through with streamed usage capture where
  compatible endpoints expose it.
- Implement Bedrock Converse routing through the OpenAI-compatible chat endpoint.
- Implement Bedrock ConverseStream routing, data URL image input mapping, and tool-call
  mapping through the OpenAI-compatible chat endpoint.
- Record usage and cost from provider response metadata when available.
- Enforce user and department monthly budgets before dispatch.
- Enforce API-key model allowlists, monthly key budgets, RPM limits, and TPM limits.
- Enforce RPM and TPM rate limits by user, department, provider, and model.
- Provide admin API key inventory and governance controls.
- Support API key rotation and self-service expiration controls.
- Record admin, login, and API key lifecycle events in an immutable audit log.
- Track provider health and block dispatch while a provider circuit is open.
- Apply model-level provider reliability policies for retries, fallback routes, request
  timeouts, and health-aware routing.
- Apply model-level weighted routing policies for traffic splitting across compatible
  backend routes.
- Show 30-day operational monitoring charts from the usage ledger.
- Show budget burn-down projections and provider/model drilldowns from the usage ledger.
- Add request and response metadata search/export without storing prompt content by
  default.
- Add built-in PII/API-key guardrail redaction and blocking controls.

### Phase 2: Production Controls

- Extend guardrails with external policy plugins and richer policy composition.
- Add Prometheus metrics and OpenTelemetry tracing. `[implemented]`

### Phase 3: Enterprise Operations

- Add semantic cache as an optional cost/latency optimization.
- Add optional Postgres database support. `[implemented]`
- Add cluster deployment hardening and migration tooling. `[implemented]`
- Add secrets backends and signed configuration import bundles.
