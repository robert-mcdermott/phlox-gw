# Phlox-GW

Phlox-GW is an enterprise LLM gateway focused on secure model access, cost control,
budget enforcement, and operational visibility.

This repository is intentionally separate from Phlox. Phlox-GW keeps the useful platform
ideas from Phlox, but narrows the product to a high-performance gateway:

- Local auth with optional Entra ID/OIDC SSO and department claim mapping.
- User-owned API keys for programmatic access.
- OpenAI-compatible gateway endpoints for OpenAI, Ollama, OpenRouter, LiteLLM, vLLM,
  LM Studio, and compatible local runtimes.
- Anthropic-compatible gateway endpoint support.
- AWS Bedrock access through the OpenAI-compatible chat endpoint using Bedrock Converse.
- Provider/model catalog with administrator-owned pricing.
- Usage ledger for per-user and per-department chargeback.
- Monthly user and department budgets with warning thresholds and hard limits.
- Per-key model allowlists, monthly budgets, RPM limits, and TPM limits.
- Enterprise RPM/TPM rate limits by user, department, provider, and model.
- Embedded dashboard served from the same Go binary.
- SQLite database stored in the application data directory.

## Current State

This is the first implementation scaffold. It includes:

- Go HTTP server using the standard library.
- SQLite persistence through a pure-Go SQLite driver.
- Seeded local admin account: `admin` / `admin`.
- Session token auth, admin/user roles, users, API keys, providers, models, budgets,
  and usage ledger schema.
- Optional OIDC browser login for Entra ID or other OIDC providers, with signed state
  cookies, local user provisioning, department claim mapping, and admin group mapping.
- Dashboard workflows to create users, mint user API keys, add/update providers, add/update
  models and token prices, and create/delete budgets.
- Admin lifecycle controls for enabling/disabling users, resetting passwords, deleting
  users/providers/models, testing enabled models, and exporting usage to CSV.
- Admin API key governance for owner inventory, model allowlists, monthly key budgets,
  request-per-minute limits, token-per-minute limits, and revocation.
- Self-service API key expiration updates and in-place key rotation with one-time secret
  display.
- Admin-managed RPM/TPM rate limits by user, department, provider, and model, enforced
  before provider dispatch.
- Immutable audit log for local login, admin configuration changes, model tests, and API
  key lifecycle events.
- Provider health state with automatic failure tracking and circuit-open blocking after
  repeated provider failures.
- Model-level reliability controls for fallback routes, retry attempts, request timeouts,
  and health-aware routing.
- Admin operations charts for 30-day cost, tokens, requests, errors, and average latency.
- `/v1/models`, `/v1/chat/completions`, and `/anthropic/v1/messages` gateway surfaces.
- Bedrock models can be exposed through `/v1/chat/completions` for non-streaming text chat,
  with usage captured from Bedrock token metadata.
- Streaming OpenAI-compatible calls are proxied through while recording usage when the
  upstream stream includes a final usage chunk.
- Embedded dashboard assets under `frontend/dist`.

Weighted load balancing, guardrails, semantic caching, and full Prometheus/OpenTelemetry
integrations are documented in the roadmap and will be added behind the existing provider,
policy, and usage seams.

## Quick Start

```bash
go mod tidy
go run ./cmd/phlox-gw
```

Open `http://127.0.0.1:8080` and sign in as `admin` / `admin`.

Important environment variables:

```bash
PHLOX_GW_ADDR=127.0.0.1:8080
PHLOX_GW_DATA_DIR=/path/to/data
PHLOX_GW_SESSION_SECRET='replace-this-with-a-long-random-secret'
```

Optional OIDC/Entra ID environment variables:

```bash
PHLOX_GW_OIDC_ENABLED=true
PHLOX_GW_OIDC_DISPLAY_NAME='Entra ID'
PHLOX_GW_OIDC_ISSUER_URL='https://login.microsoftonline.com/<tenant-id>/v2.0'
PHLOX_GW_OIDC_CLIENT_ID='<app-client-id>'
PHLOX_GW_OIDC_CLIENT_SECRET='<app-client-secret>'
PHLOX_GW_OIDC_REDIRECT_URL='https://gateway.example.com/api/auth/oidc/callback'
PHLOX_GW_OIDC_DEPARTMENT_CLAIM='department'
PHLOX_GW_OIDC_GROUPS_CLAIM='groups'
PHLOX_GW_OIDC_ADMIN_GROUPS='<admin-group-object-id-or-name>'
```

If `PHLOX_GW_OIDC_REDIRECT_URL` is omitted, Phlox-GW derives it from the incoming request
host and `/api/auth/oidc/callback`. Auto-provisioning is enabled by default and can be
disabled with `PHLOX_GW_OIDC_AUTO_PROVISION=false`.

Provider secrets can be stored directly for local development, but production deployments
should prefer environment variable references. The database stores the provider
`api_key_env` field and resolves it at request time.

Bedrock providers use the configured `aws_region` and the AWS SDK default credential
chain. That means standard sources such as `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, SSO-backed profiles, ECS task roles, and EC2
instance roles can be used without storing AWS secrets in Phlox-GW.

## Repository Map

```text
cmd/phlox-gw/        Binary entry point
internal/auth/       Password hashing and signed session tokens
internal/config/     Environment and data path loading
internal/httpapi/    REST, admin, API key, and gateway handlers
internal/store/      SQLite schema and persistence methods
frontend/dist/       Embedded first-pass dashboard
frontend/src/        React/Vite source scaffold for the dashboard
docs/                Plan, design, and roadmap
```

## Documentation

- [Plan](docs/PLAN.md)
- [Design](docs/DESIGN.md)
- [Roadmap](docs/ROADMAP.md)
