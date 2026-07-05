# Phlox-GW Roadmap

## Now

- [x] Go single-binary scaffold with embedded dashboard.
- [x] SQLite schema and migrations.
- [x] Local auth, seeded admin, users, API keys.
- [x] Provider and model catalog.
- [x] Admin UI for users, providers, models, model pricing, budgets, API keys, and usage.
- [x] Admin lifecycle controls: disable/delete users, reset passwords, delete providers/models.
- [x] OpenAI-compatible and Anthropic-compatible gateway endpoints.
- [x] Usage ledger, pricing, and budget enforcement.
- [x] CSV usage export for chargeback.
- [x] Model health-test action for enabled OpenAI, Anthropic-compatible, and Bedrock models.
- [x] OpenAI-compatible streaming pass-through with usage capture when upstream emits usage chunks.
- [x] Admin API key inventory with per-key model allowlists, monthly budgets, RPM limits, and TPM limits.
- [x] Immutable audit log for local login, admin, and API key lifecycle events.
- [x] Persisted provider health state with automatic failure tracking and circuit-open blocking.
- [x] Admin operations dashboard with 30-day cost, token, request, error, and latency trends.
- [x] Bedrock Converse adapter with AWS SDK credential chain support for non-streaming text chat.
- [x] Entra ID/OIDC browser login with local provisioning, department claim mapping, and admin group mapping.
- [x] API key rotation workflow and self-service expiration controls.
- [x] User, department, provider, and model-level RPM/TPM rate limits.
- [x] Provider reliability policies: ordered fallback routes, retry attempts, per-attempt request timeouts, and health-aware routing.
- [x] Weighted routing and traffic-splitting policies.
- [x] Budget burn-down, provider drilldowns, and model drilldowns.
- [x] Bedrock ConverseStream support with OpenAI-compatible SSE translation, data URL image input mapping, and function tool-call mapping.
- [x] Anthropic-compatible streaming pass-through with streamed usage capture from compatible endpoints.
- [x] Request/response metadata search and CSV export without storing prompt content by default.
- [x] Built-in guardrail policy layer with PII/API-key redaction and blocking controls.
- [x] Custom regex guardrail patterns with admin preview testing.
- [x] Prometheus metrics and OpenTelemetry traces.
- [x] Signed admin configuration export.
- [x] Anthropic-compatible streaming translation to Bedrock ConverseStream.
- [x] Optional Postgres database backend with SQLite still the default.
- [x] Cluster deployment hardening with Postgres: explicit deployment modes, migration locking, node heartbeats, readiness checks, admin cluster UI, and single-host demo runbook.
- [x] Azure provider types: Azure OpenAI deployments (api-key header and api-version handling) and Claude models in Azure AI Foundry via the Anthropic Messages API.
- [x] Reasoning-model parameter handling: health tests, the playground, and Anthropic-to-OpenAI translation retry with `max_completion_tokens` when an upstream rejects legacy sampling parameters.
- [x] Google Gemini provider type using the Gemini API (AI Studio keys) via Google's OpenAI-compatible surface, with streamed usage capture.
- [x] Internal refactor: split `internal/httpapi/server.go` and `internal/store/store.go` into focused files (gateway entry points, protocol bridges, admin handlers, policy gates, routing, per-entity persistence), with test files split to match.

## Next

- External secrets management (Vault and AWS Secrets Manager backends) with at-rest
  encryption for provider credentials stored in the database.
- `/v1/embeddings` gateway endpoint with per-model pricing, budgets, and rate limits.
- Official container image, Dockerfile, and Kubernetes/Helm deployment guidance.
- Signed configuration import/restore workflow to complete environment promotion.

## Later

- Semantic response cache.
- Strict distributed RPM/TPM counters for hard global cluster limits under high concurrency.
- Teams/organizations with scoped admin roles and service accounts beyond the current
  admin/user split.
- SCIM or Microsoft Graph sync for departments and groups after Entra ID SSO.
- External guardrail/policy plugins (webhook policy engine) and richer policy composition.
- Google Vertex AI provider adapter (project/region endpoints with OAuth or
  service-account credentials; the API-key Gemini API is already supported).
- OpenAI Responses API surface and additional modalities (images, audio) as demand warrants.
- In-memory hot-path rate-limit counters to remove per-request usage-ledger aggregate
  queries.

## Principles

- Every feature ships fully open source under Apache-2.0. Capabilities that comparable
  products gate behind enterprise licenses (SSO, audit logs, guardrails, metrics,
  clustering) stay free here.
- Single-binary simplicity is a product feature. New capabilities must not introduce
  mandatory external dependencies beyond the optional Postgres backend.
