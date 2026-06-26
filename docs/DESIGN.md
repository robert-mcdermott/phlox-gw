# Phlox-GW Design

## Goals

Phlox-GW is a low-latency, high-concurrency gateway for enterprise LLM access. It should
centralize provider configuration, authentication, budget enforcement, usage accounting,
and reporting while remaining easy to run on Linux, macOS, and Windows.

The default deployment target is a single binary with a SQLite database in the application
data directory. Postgres is available for single-node operation and explicit
`cluster-postgres` deployments where multiple gateway processes share state behind a load
balancer.

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
SQLite or Postgres database
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
- Optional OIDC authorization-code login for Entra ID or other OIDC providers.
- Optional Postgres backend with advisory startup migration locking.
- Cluster node heartbeat and readiness reporting for `cluster-postgres` deployments.

## Data Model

Core tables:

- `users`: local or OIDC-provisioned users, role, department, auth provider, active flag.
- `api_keys`: user-owned keys, hash, prefix, expiry, active flag, last used timestamp,
  model allowlist, monthly budget, RPM limit, and TPM limit.
- `providers`: provider type, base URL, API key or environment variable reference,
  health status, consecutive failure count, last health check, last error, and circuit
  open-until timestamp.
- `models`: provider-owned models, route id, enabled flag, pricing, context metadata.
- `usage_ledger`: append-only request metadata for chargeback and reporting.
- `budgets`: active user or department monthly budget definitions.
- `rate_limits`: active RPM/TPM policy definitions for users, departments, providers,
  and models.
- `guardrail_policies`: singleton built-in policy for PII/API-key detection, redaction,
  and blocking.
- `audit_log`: append-only local login, admin, and API key lifecycle events with
  sanitized details, client IP, and user agent.
- `cluster_nodes`: node identity, deployment mode, database backend, status, and heartbeat
  timestamps for Admin -> Cluster and readiness checks.

The usage ledger snapshots username and department at write time so chargeback rows remain
billable if users are later deleted or moved.

## Browser Authentication

Local login verifies a bcrypt password and returns a Phlox-GW HMAC-signed session token
for the embedded dashboard. Optional OIDC login uses an authorization-code flow with a
signed, HTTP-only, SameSite=Lax state cookie and nonce validation. After the OIDC ID token
is verified, configured claims are mapped into the local `users` table:

- `PHLOX_GW_OIDC_USERNAME_CLAIM` chooses the local username, with fallbacks to
  `preferred_username`, `upn`, `email`, then `sub`.
- `PHLOX_GW_OIDC_DEPARTMENT_CLAIM` populates the department used for chargeback and
  department budgets.
- `PHLOX_GW_OIDC_GROUPS_CLAIM` and `PHLOX_GW_OIDC_ADMIN_GROUPS` can grant admin role to
  matching SSO users.

Disabled local users remain blocked even if OIDC authentication succeeds. Existing local
roles are preserved unless an incoming OIDC admin group grants admin. Auto-provisioning is
enabled by default and can be disabled for environments that require pre-created users.

## Request Lifecycle

1. Client calls `/v1/chat/completions` or `/anthropic/v1/messages` with a Phlox-GW API key.
2. The gateway hashes the key, resolves the owner, checks expiry/active status, and updates
   `last_used_at`.
3. The requested model route is resolved against the enabled model catalog.
4. Model-level weighted routing chooses the initial backend route when a split policy is configured.
5. Model-level fallback routes are retained as failover candidates.
6. The provider health gate blocks dispatch when a provider circuit is still open.
7. The API key policy gate checks model allowlists, key monthly budget, RPM, and TPM.
8. The guardrail input policy can redact or block detected PII before dispatch.
9. The budget gate checks applicable user and department budgets for priced models.
10. The rate-limit gate checks user, department, provider, and model RPM/TPM policies.
11. The provider adapter rewrites only the routing fields needed by the upstream provider.
12. Non-streaming requests are dispatched with per-candidate retry and timeout policies.
13. Provider success/failure state is updated from each upstream response.
14. The guardrail output policy can redact responses or block non-streaming responses.
15. Latency, status, tokens, and cost are appended to the usage ledger.
16. The provider response is returned in the original API shape.

## Provider Strategy

Provider adapters are deliberately thin:

- `openai`: forwards to `{base_url}/chat/completions`. This covers OpenAI, Ollama,
  OpenRouter, LiteLLM, vLLM, LM Studio, and similar endpoints.
- `anthropic`: forwards to `{base_url}/v1/messages`, preserves Anthropic headers, and
  streams SSE responses through while capturing usage from compatible stream events.
- `bedrock`: uses the AWS SDK default credential chain and the provider `aws_region` to
  call Bedrock Converse and ConverseStream. Bedrock models are exposed through the
  OpenAI-compatible `/v1/chat/completions` surface and the Anthropic-compatible
  `/anthropic/v1/messages` surface for text, streaming text, and function tool-call round
  trips where the selected Bedrock model supports those features. OpenAI-compatible
  Bedrock routes also support data URL image inputs. Responses are normalized back to the
  client protocol shape for clients and usage accounting.

The model route format is `provider_id/model_id`. Bare model IDs are accepted only when
they resolve unambiguously.

Model rows can define ordered fallback routes and weighted routes. Weighted routes are
positive integer traffic-split entries such as `openai/gpt-4.1 80` and
`local-vllm/gpt-oss 20`; when present, they choose the first backend candidate for a
request. Fallback routes remain ordered failover candidates after the selected backend.
See [Model Routing](ROUTING.md) for field definitions and examples.

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

Enterprise rate limits use the same rolling one-minute ledger window and can apply to
users, departments, providers, or model routes. A request is blocked before dispatch when
any applicable active limit is already at or above its RPM or TPM threshold.

Because exact token cost is known only after provider response, the request that crosses a
budget can finish. The next priced request is blocked.

## Monitoring

The usage ledger is the source of truth for chargeback and operational reporting. The
admin API exposes a bounded daily time series derived from the ledger for requests,
errors, token volume, cost, and average latency. The embedded dashboard renders this as a
30-day operations panel, plus provider and model drilldowns, so administrators can spot
cost, traffic, latency, and provider error movement without needing an external metrics
stack for the first deployment mode.

Provider-reported token usage is preferred whenever it is available. Some
OpenAI-compatible local providers do not emit usage metadata, especially for streaming
responses; in that case Phlox-GW estimates text tokens in memory during proxying so cost
accounting does not fall to zero. The source prompt and response text are discarded after
the estimate and are not written to the database by default.

Budget burn-down reporting derives current-month spend from the same ledger and compares
it with active user and department budget limits. It exposes spend, remaining budget,
average daily run rate, days remaining, and projected month-end spend.

The request metadata log is a separate operational index for support and audit searches.
It records request id, actor, API key metadata, provider, model route, upstream model id,
protocol, endpoint, streaming flag, status, latency, token counts, cost, client IP, user
agent, and bounded error text. It deliberately does not store prompt text, response text,
image bytes, tool contents, API keys, or provider secrets by default.

## Guardrails

The first guardrail layer is a built-in PII plugin plus administrator-defined custom
regex patterns behind a small server-side interface. The built-in plugin detects email
addresses, US-style phone numbers, SSNs, Luhn-valid credit-card numbers, and common API
key/token patterns. Custom patterns are stored in the policy row as JSON and compiled with
Go's RE2 engine before save or preview.

Policies are admin-managed and stored in SQLite. Input policy actions are `off`, `redact`,
and `block`; output policy actions are also `off`, `redact`, and `block`. Input blocking
happens before provider dispatch. Output blocking is enforced for non-streaming responses
after provider return; streaming requests are rejected while hard output blocking is
enabled because partially streamed bytes cannot be recalled.

All inspection happens in memory. Redacted text can be sent upstream or returned to the
client, but original prompt and response content are not persisted by default. Preview
samples are also evaluated in memory and not stored.

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
- OIDC authorization-code login with signed state cookies and ID token nonce validation.
- SHA-256 API key storage.
- Admin-gated configuration and reporting APIs.
- API-key-only gateway routes.
- Admin API key inventory with model allowlists, monthly budgets, RPM limits, and TPM
  limits.
- Admin-managed RPM/TPM rate limits by user, department, provider, and model.
- Self-service key naming/expiration updates and in-place key rotation; newly minted or
  rotated plaintext secrets are returned only once.
- Immutable audit log for local login, admin configuration changes, model health tests,
  and API key lifecycle events.
- Persisted provider health state and circuit-open blocking after repeated provider
  failures.
- Request metadata search and CSV export without prompt or response content storage by
  default.
- Built-in PII/API-key guardrail policy with request redaction/blocking and response
  redaction/non-stream blocking.
- No prompt content stored in the ledger.

Planned:

- Provider secret encryption or external vault integration.
- External guardrail plugin loading and richer policy composition.
- TLS termination guidance and secure cookie mode.
