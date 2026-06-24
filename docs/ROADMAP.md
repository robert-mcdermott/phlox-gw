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

## Next

- Add budget burn-down, provider drilldowns, and model drilldowns.
- Add Bedrock streaming with ConverseStream and richer multimodal/tool-call mapping.
- Add Anthropic streaming support and streamed usage capture where compatible endpoints expose it.

## Later

- Prometheus metrics and OpenTelemetry traces.
- Guardrail plugin layer and PII redaction.
- Semantic response cache.
- Request metadata search and signed admin configuration export.
- Cluster mode with Postgres.
- External secrets management.
