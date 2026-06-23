# Phlox-GW Design

## Goals

Phlox-GW is a low-latency, high-concurrency gateway for enterprise LLM access. It should
centralize provider configuration, authentication, budget enforcement, usage accounting,
and reporting while remaining easy to run on Linux, macOS, and Windows.

The first deployment target is a single binary with a SQLite database in the application
data directory. The design keeps clear seams for future cluster and database upgrades.

## Architecture

```text
Clients and SDKs
  |
  |  OpenAI-compatible /v1/*
  |  Anthropic-compatible /anthropic/v1/*
  v
Phlox-GW Go server
  |
  |-- Auth and API key resolver
  |-- Model router and provider catalog
  |-- Budget and policy gate
  |-- Provider adapters
  |-- Usage and latency recorder
  |-- Admin dashboard API
  |
  v
SQLite database
```

The frontend is an embedded static application. The Go binary serves both the API and the
dashboard so the production artifact can be distributed as one executable.

## Backend Stack

- Go standard `net/http` server and router.
- Pure-Go SQLite driver for cross-platform builds.
- HMAC-signed session tokens for browser auth.
- Bcrypt password hashes for local accounts.
- SHA-256 hashed API keys; plaintext keys are shown only once.
- Provider secrets resolved from environment variable references when configured.

## Data Model

Core tables:

- `users`: local users, role, department, auth provider, active flag.
- `api_keys`: user-owned keys, hash, prefix, expiry, active flag, last used timestamp,
  model allowlist, monthly budget, RPM limit, and TPM limit.
- `providers`: provider type, base URL, API key or environment variable reference,
  health status, consecutive failure count, last health check, last error, and circuit
  open-until timestamp.
- `models`: provider-owned models, route id, enabled flag, pricing, context metadata.
- `usage_ledger`: append-only request metadata for chargeback and reporting.
- `budgets`: active user or department monthly budget definitions.
- `audit_log`: append-only local login, admin, and API key lifecycle events with
  sanitized details, client IP, and user agent.

The usage ledger snapshots username and department at write time so chargeback rows remain
billable if users are later deleted or moved.

## Request Lifecycle

1. Client calls `/v1/chat/completions` or `/anthropic/v1/messages` with a Phlox-GW API key.
2. The gateway hashes the key, resolves the owner, checks expiry/active status, and updates
   `last_used_at`.
3. The requested model route is resolved against the enabled model catalog.
4. The API key policy gate checks model allowlists, key monthly budget, RPM, and TPM.
5. The budget gate checks applicable user and department budgets for priced models.
6. The provider health gate blocks dispatch when the provider circuit is still open.
7. The provider adapter rewrites only the routing fields needed by the upstream provider.
8. The request is dispatched with a bounded HTTP client.
9. Provider success/failure state is updated from the upstream response.
10. Latency, status, tokens, and cost are appended to the usage ledger.
11. The provider response is returned in the original API shape.

## Provider Strategy

Provider adapters are deliberately thin:

- `openai`: forwards to `{base_url}/chat/completions`. This covers OpenAI, Ollama,
  OpenRouter, LiteLLM, vLLM, LM Studio, and similar endpoints.
- `anthropic`: forwards to `{base_url}/v1/messages` and preserves Anthropic headers.
- `bedrock`: uses the same model catalog and budget/accounting gate; the AWS adapter is
  planned for the next implementation slice.

The model route format is `provider_id/model_id`. Bare model IDs are accepted only when
they resolve unambiguously.

Provider health state is persisted on the provider row. Successful upstream responses reset
the provider to `healthy`. Provider transport errors, 429, 401/403, and 5xx responses
increment consecutive failures; after three consecutive failures, the provider is marked
`down` and its circuit is opened for five minutes. Admin model health tests also update
the same health state.

## Budget Semantics

Budgets are monthly UTC windows. A request is blocked before dispatch when a priced model
is requested and either the user's own budget or the user's department budget is already
at or above its hard limit.

API key budgets use the same monthly UTC window and block priced requests when the key is
at or above its monthly spend limit. Key RPM/TPM limits use a rolling one-minute ledger
window. TPM enforcement is based on completed requests already recorded in the ledger, so
the request that crosses a token-per-minute boundary can finish before the next request is
blocked.

Because exact token cost is known only after provider response, the request that crosses a
budget can finish. The next priced request is blocked.

## Monitoring

The usage ledger is the source of truth for chargeback and operational reporting. The
admin API exposes a bounded daily time series derived from the ledger for requests,
errors, token volume, cost, and average latency. The embedded dashboard renders this as a
30-day operations panel so administrators can spot cost, traffic, latency, and provider
error movement without needing an external metrics stack for the first deployment mode.

## Frontend

The dashboard uses the Phlox operational visual language:

- Dark default theme with magenta and cyan accents.
- Compact cards and tables for administrators.
- Tokenized CSS variables so React/Vite can grow into full theme switching.
- First-screen product experience is the working admin console, not a marketing page.

The `frontend/src` tree is a Vite/React scaffold. The current binary embeds
`frontend/dist` so the server is usable before the richer frontend build pipeline lands.

## Security Posture

Current implementation:

- Bcrypt local passwords.
- HMAC-signed session tokens.
- SHA-256 API key storage.
- Admin-gated configuration and reporting APIs.
- API-key-only gateway routes.
- Admin API key inventory with model allowlists, monthly budgets, RPM limits, and TPM
  limits.
- Immutable audit log for local login, admin configuration changes, model health tests,
  and API key lifecycle events.
- Persisted provider health state and circuit-open blocking after repeated provider
  failures.
- No prompt content stored in the ledger.

Planned:

- Entra ID/OIDC and group/department claim mapping.
- Request metadata search across gateway calls.
- Provider secret encryption or external vault integration.
- Guardrails and redaction policies.
- TLS termination guidance and secure cookie mode.
