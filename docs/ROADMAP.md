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
- [x] Model health-test action for enabled OpenAI/Anthropic-compatible models.
- [x] OpenAI-compatible streaming pass-through with usage capture when upstream emits usage chunks.
- [x] Admin API key inventory with per-key model allowlists, monthly budgets, RPM limits, and TPM limits.
- [x] Immutable audit log for local login, admin, and API key lifecycle events.

## Next

- Add Bedrock adapter with AWS credential chain support.
- Add Entra ID/OIDC login and department claim mapping.
- Add API key rotation workflow and self-service expiration controls.
- Add user, department, provider, and model-level rate limits.
- Add provider health checks, retries, circuit breakers, and failover.
- Add richer dashboards for latency, error rate, token volume, and budget burn-down.
- Add Anthropic streaming support and streamed usage capture where compatible endpoints expose it.

## Later

- Prometheus metrics and OpenTelemetry traces.
- Guardrail plugin layer and PII redaction.
- Semantic response cache.
- Request metadata search and signed admin configuration export.
- Cluster mode with Postgres.
- External secrets management.
