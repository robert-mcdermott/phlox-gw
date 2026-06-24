# Phlox-GW

Phlox-GW is a self-hosted enterprise LLM gateway. It gives administrators one place to
publish model routes, control who can use them, attach prices, enforce budgets and rate
limits, and report usage for chargeback.

The product is intentionally narrower than the broader Phlox platform. Phlox-GW is not a
chat assistant, RAG system, or agent runtime. It is the gateway and governance layer that
sits between users, applications, local model runtimes, cloud LLM APIs, and AWS Bedrock.

## What It Does

Phlox-GW provides:

- Local browser authentication with admin and user roles.
- Optional Entra ID or other OIDC single sign-on.
- User-owned API keys for programmatic access.
- OpenAI-compatible gateway endpoints for OpenAI, Ollama, OpenRouter, LiteLLM, vLLM,
  LM Studio, and other compatible endpoints.
- Anthropic-compatible gateway endpoint support, including request/response translation
  to OpenAI-compatible and Bedrock routes.
- AWS Bedrock access through OpenAI-compatible and Anthropic-compatible endpoints using
  Bedrock Converse and ConverseStream.
- Provider and model catalog management from the admin UI.
- Public model routes that can be stable aliases instead of provider-specific names.
- Per-model input and output prices in USD per 1 million tokens.
- Per-user and per-department monthly chargeback from an append-only usage ledger.
- User, department, provider, model, and API-key level RPM and TPM limits.
- User, department, and API-key monthly budgets with hard-limit enforcement.
- Provider health tracking with circuit-open blocking after repeated failures.
- Model fallback routes, retries, request timeouts, health-aware routing, and weighted
  traffic splitting.
- Admin operations charts for cost, tokens, requests, errors, latency, providers, and
  models.
- Budget burn-down views and CSV exports.
- Request metadata search and CSV export without storing prompt text, response text, image
  bytes, tool contents, API keys, or provider secrets by default.
- Built-in guardrail policy controls for PII/API-key detection, request redaction or
  blocking, and response redaction or non-stream blocking.
- Signed, sanitized admin configuration export for review, backup, and environment
  promotion without exporting secrets or user credentials.
- A browser dashboard embedded into the same Go binary as the gateway API.
- A SQLite database that can live next to the application binary or in a configured data
  directory.

## Architecture

```text
Applications, SDKs, and users
  |
  |  OpenAI-compatible /v1/*
  |  Anthropic-compatible /anthropic/v1/*
  v
Phlox-GW single Go binary
  |
  |-- Browser auth and API key auth
  |-- Provider and model route catalog
  |-- Budget, API-key policy, rate-limit, and guardrail gates
  |-- Provider adapters for OpenAI-compatible, Anthropic-compatible, and Bedrock
  |-- Usage ledger, request metadata log, audit log, and admin APIs
  |-- Embedded dashboard assets from frontend/dist
  v
SQLite database
```

The first deployment mode is a single executable and a single SQLite database file. The
design keeps the backend simple for Linux, macOS, and Windows while leaving room for later
Postgres or multi-node operation.

## Requirements

- Go matching the version in [go.mod](go.mod). The current module declares Go 1.26.
- Node.js and npm only if you plan to rebuild the frontend from `frontend/src`.
- Network access from the gateway host to the configured upstream providers.
- AWS credentials on the gateway host if you use Bedrock.

The checked-in `frontend/dist` assets are already embedded by `go build`, so a normal
backend build does not require Node.

## Quick Start

From the repository root:

```bash
go test ./...
scripts/build-release.sh --skip-frontend
scripts/run-local.sh
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080) and sign in with the seeded local
administrator:

```text
Username: admin
Password: admin
```

Change that password before any shared use.

By default Phlox-GW stores `phlox-gw.db` in the current working directory and listens on
`127.0.0.1:8080`.

## Production-Oriented Run

Set a persistent data directory and a long random session secret:

```bash
export PHLOX_GW_ADDR="127.0.0.1:8080"
export PHLOX_GW_DATA_DIR="/var/lib/phlox-gw"
export PHLOX_GW_SESSION_SECRET="$(openssl rand -base64 48)"

./phlox-gw
```

For production, put Phlox-GW behind a TLS-terminating reverse proxy or load balancer. The
application currently serves HTTP directly and expects TLS to be handled at the edge.

See the [Operator Guide](docs/OPERATIONS.md) for systemd, macOS, Windows, OIDC, provider,
backup, and reverse-proxy guidance.

## Build Options

Build the frontend and all release binaries:

```bash
scripts/build-release.sh
```

This writes:

- `dist/phlox-gw-darwin-arm64`
- `dist/phlox-gw-linux-amd64`
- `dist/phlox-gw-linux-arm64`
- `dist/phlox-gw-windows-amd64.exe`
- `dist/phlox-gw-windows-arm64.exe`
- `dist/checksums.txt`

On Windows PowerShell:

```powershell
scripts\build-release.ps1
```

Build just the Go binaries when `frontend/dist` is already current:

```bash
scripts/build-release.sh --skip-frontend
```

The frontend source lives in `frontend/src/static`. `npm run build` copies those files into
`frontend/dist`, and the Go binary embeds `frontend/dist`.

For local runs, copy the environment template and start the gateway:

```bash
cp scripts/env.example .env
scripts/run-local.sh
```

On Windows:

```powershell
Copy-Item scripts\env.example .env
scripts\run-local.ps1
```

## First-Time Setup

1. Start the gateway and sign in as `admin`.
2. Go to `Admin -> Users` and create real users with departments.
3. Go to `Admin -> Providers` and enable or create provider profiles.
4. Go to `Admin -> Models` and create routes for upstream models.
5. Set input and output prices in USD per 1 million tokens.
6. Go to `Admin -> Budgets` and configure user or department monthly limits.
7. Go to `Admin -> Rate Limits` for user, department, provider, or model RPM/TPM limits.
8. Go to `Admin -> Guardrails` if you want PII redaction or blocking.
9. Go to `API Keys` as a user, or `Admin -> API Keys` as an admin, and mint a key.
10. Test the key with `/v1/models` or `/v1/chat/completions`.

## Providers

Provider rows describe where Phlox-GW sends requests after model routing.

| Provider type | Typical base URL | Notes |
| --- | --- | --- |
| `openai` | `https://api.openai.com/v1` | Also works for OpenRouter, LiteLLM, vLLM, Ollama, LM Studio, and other OpenAI-compatible APIs. |
| `openai` for Ollama | `http://localhost:11434/v1` | Local Ollama exposes an OpenAI-compatible API at `/v1`. |
| `anthropic` | `https://api.anthropic.com` | Phlox-GW appends `/v1/messages`. |
| `bedrock` | blank | Uses the AWS SDK credential chain and configured AWS region. |

Provider API keys can be stored directly for local testing, but production deployments
should prefer environment variable references. For example, set `api_key_env` to
`OPENAI_API_KEY` and run the gateway with that environment variable set.

Bedrock does not need a provider API key. It uses standard AWS credential sources such as
`AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, SSO
profiles, ECS task roles, or EC2 instance roles.

## Models And Routes

A model row maps a public route to an upstream provider model.

Important fields:

- `Provider`: the provider profile that owns the upstream endpoint or Bedrock region.
- `Upstream model id`: the model name Phlox-GW sends upstream.
- `Route id`: the public model name clients send to Phlox-GW. If blank, it defaults to
  `provider_id/model_id`.
- `Input cost` and `Output cost`: USD per 1 million tokens.
- `Streaming`: whether the route should be advertised for streaming.
- `Fallback routes`: ordered backup routes to try on provider failure.
- `Weighted routes`: traffic split entries such as `openai/gpt-4o-mini 80`.

Example route:

```text
Provider: local-ollama
Upstream model id: gemma4:31b-cloud
Route id: local-ollama/gemma4:31b-cloud
```

Clients then call:

```json
{
  "model": "local-ollama/gemma4:31b-cloud",
  "messages": [
    { "role": "user", "content": "Write a one sentence status update." }
  ]
}
```

For detailed route behavior, see [Model Routing](docs/ROUTING.md).

## Using The Gateway

List available models:

```bash
curl -sS http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer pgw-sk-your-key"
```

Call an OpenAI-compatible chat route:

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer pgw-sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "local-ollama/gemma4:31b-cloud",
    "messages": [
      { "role": "user", "content": "Explain Phlox-GW in one sentence." }
    ]
  }'
```

Stream a response:

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer pgw-sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "local-ollama/gemma4:31b-cloud",
    "stream": true,
    "messages": [
      { "role": "user", "content": "Give me three deployment checks." }
    ]
  }'
```

Call the Anthropic-compatible endpoint. The route may point to an Anthropic-compatible
provider or an OpenAI-compatible provider for both streaming and non-streaming requests.
Bedrock routes are also supported for both streaming and non-streaming requests through
Bedrock Converse and ConverseStream translation:

```bash
curl -sS http://127.0.0.1:8080/anthropic/v1/messages \
  -H "x-api-key: pgw-sk-your-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic/claude-3-5-sonnet-latest",
    "max_tokens": 256,
    "messages": [
      { "role": "user", "content": "Summarize this gateway." }
    ]
  }'
```

More examples are in [API Usage](docs/API_USAGE.md).

## Configuration

Core environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `PHLOX_GW_ADDR` | `127.0.0.1:8080` | HTTP listen address. |
| `PHLOX_GW_DATA_DIR` | current working directory | Directory containing `phlox-gw.db`. |
| `PHLOX_GW_SESSION_SECRET` | random development secret | HMAC secret for browser sessions. Set this explicitly for shared use. |

OIDC and Entra ID settings:

| Variable | Purpose |
| --- | --- |
| `PHLOX_GW_OIDC_ENABLED` | Enables browser SSO when `true`. |
| `PHLOX_GW_OIDC_DISPLAY_NAME` | Button label in the login UI. Defaults to `Entra ID`. |
| `PHLOX_GW_OIDC_ISSUER_URL` | OIDC issuer, for example `https://login.microsoftonline.com/<tenant-id>/v2.0`. |
| `PHLOX_GW_OIDC_CLIENT_ID` | Application client ID. |
| `PHLOX_GW_OIDC_CLIENT_SECRET` | Application client secret. |
| `PHLOX_GW_OIDC_REDIRECT_URL` | Optional fixed callback URL. |
| `PHLOX_GW_OIDC_SCOPES` | Optional space or comma separated scopes. `openid` is always included. |
| `PHLOX_GW_OIDC_USERNAME_CLAIM` | Claim used for the local username. Defaults to `preferred_username`. |
| `PHLOX_GW_OIDC_DEPARTMENT_CLAIM` | Claim used for chargeback department. Defaults to `department`. |
| `PHLOX_GW_OIDC_GROUPS_CLAIM` | Claim containing group memberships. Defaults to `groups`. |
| `PHLOX_GW_OIDC_ADMIN_GROUPS` | Space or comma separated groups that grant Phlox-GW admin role. |
| `PHLOX_GW_OIDC_AUTO_PROVISION` | Creates local users on first SSO login. Defaults to `true`. |

Telemetry settings:

| Variable | Purpose |
| --- | --- |
| `PHLOX_GW_METRICS_ENABLED` | Exposes Prometheus metrics when `true`. Defaults to `false`. |
| `PHLOX_GW_METRICS_PATH` | Metrics scrape path. Defaults to `/metrics`. |
| `PHLOX_GW_OTEL_TRACES_ENABLED` | Enables OpenTelemetry trace export when `true`. Defaults to `false`. |
| `PHLOX_GW_OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | OTLP/HTTP traces endpoint, for example `http://localhost:4318/v1/traces`. |
| `PHLOX_GW_OTEL_EXPORTER_OTLP_INSECURE` | Allows insecure OTLP transport when `true`. |
| `PHLOX_GW_OTEL_SAMPLE_RATIO` | Trace sampling ratio from `0.0` to `1.0`. Defaults to `1.0`. |

The full configuration reference is in the [Operator Guide](docs/OPERATIONS.md).

## Accounting And Privacy

The usage ledger records request metadata needed for chargeback: user, department, API key
metadata, provider, model route, token counts, cost, status, latency, and timestamp.

Provider-reported token usage is preferred. Some local OpenAI-compatible providers omit
usage metadata, especially for streaming responses. In that case Phlox-GW estimates text
tokens in memory so cost accounting does not fall to zero. Prompt and response content are
discarded after the estimate and are not written to SQLite by default.

The guardrail policy can detect email addresses, phone numbers, SSNs, credit-card numbers,
common API key/token patterns, and administrator-defined custom regex patterns. Input
policies can redact or block before provider dispatch. Output policies can redact responses
before the client receives them or block non-stream responses after provider return. Hard
output blocking rejects streaming requests because a stream cannot be unsent once bytes
have left the gateway. The Guardrails admin panel includes a local preview tool for testing
draft pattern changes before saving.

## Documentation

- [Operator Guide](docs/OPERATIONS.md): build, configuration, SSO, providers, deployment,
  backups, and troubleshooting.
- [API Usage](docs/API_USAGE.md): client endpoints, curl examples, streaming, errors, and
  integration notes.
- [Design](docs/DESIGN.md): architecture and implementation decisions.
- [Model Routing](docs/ROUTING.md): route IDs, fallback routes, weighted routes, and common
  patterns.
- [Plan](docs/PLAN.md): product scope and implementation phases.
- [Roadmap](docs/ROADMAP.md): completed and planned work.

## License

Phlox-GW is licensed under the [Apache License 2.0](LICENSE).

## Repository Map

```text
cmd/phlox-gw/        Binary entry point
embed.go            Go embed declaration for frontend/dist
internal/auth/       Password hashing, API key generation, and signed session tokens
internal/config/     Environment and data path loading
internal/httpapi/    Browser, admin, API key, provider, and gateway handlers
internal/store/      SQLite schema, migrations, and persistence methods
frontend/dist/       Embedded dashboard assets
frontend/src/static/ Source dashboard assets used by the frontend build
docs/                Design, operator, API, routing, plan, and roadmap docs
```

## Current Roadmap Focus

The gateway foundation, first guardrail policy layer, and observability hooks are in
place. The next major implementation area is semantic cache support, followed by external
secrets management, signed configuration export, and cluster database options.
