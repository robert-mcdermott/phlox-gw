# Phlox-GW Operator Guide

This guide covers building, configuring, deploying, backing up, and troubleshooting
Phlox-GW.

## Build And Test

Run all tests:

```bash
go test ./...
```

Build the single binary:

```bash
go build -o phlox-gw ./cmd/phlox-gw
```

The compiled binary embeds `frontend/dist`, so users do not need a separate web server for
the dashboard.

Build the frontend and all release binaries:

```bash
scripts/build-release.sh
```

This writes binaries and checksums under `dist/`:

- `phlox-gw-darwin-arm64`
- `phlox-gw-linux-amd64`
- `phlox-gw-linux-arm64`
- `phlox-gw-windows-amd64.exe`
- `phlox-gw-windows-arm64.exe`
- `checksums.txt`

The script runs `npm run build` in `frontend/` first, so the embedded UI is regenerated
before Go compiles. Use `--skip-frontend` when `frontend/dist` is already current:

```bash
scripts/build-release.sh --skip-frontend
```

On Windows PowerShell, use the equivalent helper:

```powershell
scripts\build-release.ps1
```

The frontend source for the embedded dashboard lives in `frontend/src/static`. `npm run
build` copies that source into `frontend/dist`, which is what `embed.go` includes in the
single binary.

For local runs, copy the environment template and use the run helper:

```bash
cp scripts/env.example .env
scripts/run-local.sh
```

On Windows:

```powershell
Copy-Item scripts\env.example .env
scripts\run-local.ps1
```

## Runtime Files

Phlox-GW writes one SQLite database file:

```text
<PHLOX_GW_DATA_DIR>/phlox-gw.db
```

If `PHLOX_GW_DATA_DIR` is not set, the current working directory is used.

Keep the database on persistent local storage. Do not place the same SQLite database file
behind multiple running Phlox-GW nodes.

## Environment Variables

### Core Settings

| Variable | Default | Required | Description |
| --- | --- | --- | --- |
| `PHLOX_GW_ADDR` | `127.0.0.1:8080` | No | HTTP listen address. Use `0.0.0.0:8080` only when the host/network is trusted or TLS is terminated in front of the gateway. |
| `PHLOX_GW_DATA_DIR` | current working directory | No | Directory containing `phlox-gw.db`. Created on startup if missing. |
| `PHLOX_GW_SESSION_SECRET` | generated development secret | Production | HMAC secret for browser sessions. Set to a stable high-entropy value before shared use. |

The development session secret changes each process start. Users may be logged out after a
restart unless `PHLOX_GW_SESSION_SECRET` is set.

Generate a secret on Linux or macOS:

```bash
openssl rand -base64 48
```

Generate one with PowerShell:

```powershell
$bytes = New-Object byte[] 48
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToBase64String($bytes)
```

### OIDC And Entra ID

| Variable | Default | Description |
| --- | --- | --- |
| `PHLOX_GW_OIDC_ENABLED` | `false` | Enables browser SSO. |
| `PHLOX_GW_OIDC_DISPLAY_NAME` | `Entra ID` | Login button label. |
| `PHLOX_GW_OIDC_ISSUER_URL` | empty | OIDC issuer URL. Required when OIDC is enabled. |
| `PHLOX_GW_OIDC_CLIENT_ID` | empty | OIDC client ID. Required when OIDC is enabled. |
| `PHLOX_GW_OIDC_CLIENT_SECRET` | empty | OIDC client secret. Required when OIDC is enabled. |
| `PHLOX_GW_OIDC_REDIRECT_URL` | derived from request host | Callback URL. Set explicitly behind some proxies. |
| `PHLOX_GW_OIDC_SCOPES` | `openid profile email` | Space, comma, or newline separated scopes. |
| `PHLOX_GW_OIDC_USERNAME_CLAIM` | `preferred_username` | Claim used for local username. |
| `PHLOX_GW_OIDC_DEPARTMENT_CLAIM` | `department` | Claim used for chargeback and department budgets. |
| `PHLOX_GW_OIDC_GROUPS_CLAIM` | `groups` | Claim used for admin group mapping. |
| `PHLOX_GW_OIDC_ADMIN_GROUPS` | empty | Space, comma, or newline separated group IDs or names that grant admin. |
| `PHLOX_GW_OIDC_AUTO_PROVISION` | `true` | Creates local users on first successful SSO login. |

Example Entra ID configuration:

```bash
export PHLOX_GW_OIDC_ENABLED=true
export PHLOX_GW_OIDC_DISPLAY_NAME="Entra ID"
export PHLOX_GW_OIDC_ISSUER_URL="https://login.microsoftonline.com/<tenant-id>/v2.0"
export PHLOX_GW_OIDC_CLIENT_ID="<application-client-id>"
export PHLOX_GW_OIDC_CLIENT_SECRET="<client-secret>"
export PHLOX_GW_OIDC_REDIRECT_URL="https://gateway.example.com/api/auth/oidc/callback"
export PHLOX_GW_OIDC_DEPARTMENT_CLAIM="department"
export PHLOX_GW_OIDC_GROUPS_CLAIM="groups"
export PHLOX_GW_OIDC_ADMIN_GROUPS="<admin-group-object-id>"
```

In Entra ID, configure the application redirect URI to match
`/api/auth/oidc/callback`. If the organization emits group overage claims instead of full
group membership in the ID token, admin group mapping may require additional identity
work in a future release.

## Observability

Phlox-GW can expose Prometheus metrics and export OpenTelemetry traces. Both are disabled
by default so a new local install does not expose an unauthenticated scrape endpoint or
attempt outbound telemetry export.

### Prometheus Metrics

| Variable | Default | Description |
| --- | --- | --- |
| `PHLOX_GW_METRICS_ENABLED` | `false` | Exposes a Prometheus scrape endpoint when `true`. |
| `PHLOX_GW_METRICS_PATH` | `/metrics` | Metrics scrape path. |

Example:

```bash
export PHLOX_GW_METRICS_ENABLED=true
export PHLOX_GW_METRICS_PATH=/metrics
```

Scrape target:

```yaml
scrape_configs:
  - job_name: phlox-gw
    static_configs:
      - targets: ["127.0.0.1:8080"]
```

Gateway metrics include:

- `phlox_gw_http_requests_total`
- `phlox_gw_http_request_duration_seconds`
- `phlox_gw_upstream_requests_total`
- `phlox_gw_upstream_request_duration_seconds`
- `phlox_gw_upstream_tokens_total`
- `phlox_gw_upstream_cost_usd_total`
- `phlox_gw_build_info`

Labels intentionally avoid user IDs, usernames, API-key IDs, prompts, responses, tool
contents, and provider secrets. Use the SQLite usage ledger and admin reports for
per-user and per-department chargeback.

### OpenTelemetry Traces

| Variable | Default | Description |
| --- | --- | --- |
| `PHLOX_GW_OTEL_TRACES_ENABLED` | `false` | Enables OTLP/HTTP trace export. |
| `PHLOX_GW_OTEL_SERVICE_NAME` | `phlox-gw` | Service name attached to exported traces. |
| `PHLOX_GW_OTEL_SERVICE_VERSION` | empty | Optional service version label. |
| `PHLOX_GW_OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` or `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/HTTP traces endpoint. |
| `PHLOX_GW_OTEL_EXPORTER_OTLP_INSECURE` | `OTEL_EXPORTER_OTLP_INSECURE` or `false` | Allows insecure OTLP transport. |
| `PHLOX_GW_OTEL_SAMPLE_RATIO` | `1.0` | Trace sampling ratio from `0.0` to `1.0`. |

Example with a local OpenTelemetry Collector:

```bash
export PHLOX_GW_OTEL_TRACES_ENABLED=true
export PHLOX_GW_OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:4318/v1/traces
export PHLOX_GW_OTEL_EXPORTER_OTLP_INSECURE=true
export PHLOX_GW_OTEL_SAMPLE_RATIO=1.0
```

Trace spans include inbound HTTP requests and child spans around upstream provider calls.
Attributes include method, route, provider ID, provider type, model route, upstream model
ID, protocol, status, and latency. Spans do not include prompt text, response text, tool
contents, API keys, or provider secrets.

## Provider Configuration

Create providers in `Admin -> Providers`.

### OpenAI

Provider:

```text
ID: openai
Type: openai
Base URL: https://api.openai.com/v1
API key env: OPENAI_API_KEY
Enabled: true
```

Runtime environment:

```bash
export OPENAI_API_KEY="sk-..."
```

### Ollama

Provider:

```text
ID: local-ollama
Type: openai
Base URL: http://localhost:11434/v1
Enabled: true
```

Ollama must be reachable from the machine running Phlox-GW. If Ollama runs on another host
or inside a container, use that reachable host name instead of `localhost`.

### vLLM, LM Studio, LiteLLM, OpenRouter

Use provider type `openai` and the service's OpenAI-compatible base URL. The gateway
appends `/chat/completions`, so the base URL should normally end at `/v1`.

Examples:

```text
http://localhost:8000/v1
http://localhost:1234/v1
https://openrouter.ai/api/v1
```

### Anthropic

Provider:

```text
ID: anthropic
Type: anthropic
Base URL: https://api.anthropic.com
API key env: ANTHROPIC_API_KEY
Enabled: true
```

Runtime environment:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

For provider pass-through, Phlox-GW appends `/v1/messages` for Anthropic-compatible
providers. The client-facing `/anthropic/v1/messages` endpoint can also translate
Anthropic Messages requests to OpenAI-compatible routes, including streaming text and
tool-use events. Bedrock routes are supported through this endpoint for non-streaming
requests.

### AWS Bedrock

Provider:

```text
ID: bedrock
Type: bedrock
AWS region: us-east-1
Enabled: true
```

No provider API key is needed. The AWS SDK default credential chain is used. Common
options include:

```bash
export AWS_PROFILE="my-sso-profile"
export AWS_REGION="us-east-1"
```

or:

```bash
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_SESSION_TOKEN="..."
```

Instance roles, task roles, and SSO-backed profiles are also supported by the AWS SDK.

## Model Configuration

Create models in `Admin -> Models`.

Required fields:

- Provider.
- Upstream model ID.
- Route ID, or blank to default to `provider_id/model_id`.
- Input and output price in USD per 1 million tokens.

Recommended fields:

- Display name.
- Context window.
- Streaming support.
- Retry attempts and request timeout.
- Fallback routes and weighted routes for resilience or traffic splitting.

Pricing can be set to zero for free local models. Budgets only block priced models.

## Users, Keys, Budgets, And Limits

Users can mint and manage their own API keys from `API Keys`.

Administrators can:

- Create, disable, reset, and delete users.
- Create API keys for users.
- Rotate or revoke keys.
- Set per-key model allowlists.
- Set per-key monthly budget, RPM, and TPM limits.
- Set monthly user or department budgets.
- Set enterprise RPM and TPM limits scoped to user, department, provider, or model.

Rate limits use a rolling one-minute ledger window. TPM limits are based on completed
requests already recorded in the ledger, so the request that crosses the threshold can
finish and the next request is blocked.

Budget checks happen before dispatch, but final cost is known after the provider responds.
The request that crosses a monthly budget can finish and the next priced request is
blocked.

## Guardrail Policy

Configure guardrail policy in `Admin -> Guardrails`.

The first built-in detector supports:

- Email addresses.
- US-style phone numbers.
- Social Security numbers.
- Credit-card numbers, validated with Luhn checks.
- Common API key and token patterns.

Administrators can add custom regex patterns for organization-specific identifiers,
internal hostnames, project codes, secrets, or other local data classes. Custom regexes use
Go's RE2 syntax. Invalid regexes are rejected on save and in the preview tool. Each custom
pattern can be disabled, redacted with its own replacement text, or set to block.

Input policy actions:

- `off`: request content is not inspected.
- `redact`: matching values are replaced before provider dispatch.
- `block`: matching requests are rejected before provider dispatch.

Output policy actions:

- `off`: response content is not inspected.
- `redact`: matching values are replaced before the client receives the response.
- `block`: non-stream responses are replaced with a policy error after provider return.

When output action is `block`, streaming requests are rejected before dispatch. This keeps
hard blocking semantics clear because streamed bytes cannot be recalled after they are
written to the client.

Use the built-in preview panel to test unsaved policy changes against sample text. Preview
samples are inspected in memory and are not written to SQLite.

Guardrail inspection is in memory. Phlox-GW does not store prompt text, response text, or
tool contents by default.

## Linux systemd Example

Create a dedicated user and data directory:

```bash
sudo useradd --system --home /var/lib/phlox-gw --shell /usr/sbin/nologin phlox-gw
sudo mkdir -p /opt/phlox-gw /var/lib/phlox-gw
sudo cp phlox-gw /opt/phlox-gw/phlox-gw
sudo chown -R phlox-gw:phlox-gw /var/lib/phlox-gw
```

Create `/etc/phlox-gw.env`:

```text
PHLOX_GW_ADDR=127.0.0.1:8080
PHLOX_GW_DATA_DIR=/var/lib/phlox-gw
PHLOX_GW_SESSION_SECRET=<long-random-secret>
OPENAI_API_KEY=<optional>
ANTHROPIC_API_KEY=<optional>
AWS_PROFILE=<optional>
```

Create `/etc/systemd/system/phlox-gw.service`:

```ini
[Unit]
Description=Phlox-GW LLM Gateway
After=network-online.target
Wants=network-online.target

[Service]
User=phlox-gw
Group=phlox-gw
EnvironmentFile=/etc/phlox-gw.env
WorkingDirectory=/var/lib/phlox-gw
ExecStart=/opt/phlox-gw/phlox-gw
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now phlox-gw
sudo systemctl status phlox-gw
```

## macOS LaunchAgent Sketch

For a user-level service, place the binary in a stable path and create a LaunchAgent plist
that sets the same environment variables. The most important settings are
`PHLOX_GW_DATA_DIR` and `PHLOX_GW_SESSION_SECRET`.

For development, running `./phlox-gw` directly is usually simpler.

## Windows Service Sketch

Phlox-GW runs as a normal console executable on Windows:

```powershell
$env:PHLOX_GW_ADDR="127.0.0.1:8080"
$env:PHLOX_GW_DATA_DIR="C:\ProgramData\Phlox-GW"
$env:PHLOX_GW_SESSION_SECRET="<long-random-secret>"
.\phlox-gw.exe
```

To run it as a service, use your organization's preferred Windows service wrapper or
service manager and configure the same environment variables.

## Reverse Proxy And TLS

Terminate TLS in front of Phlox-GW. A minimal Nginx location looks like:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;
}
```

`proxy_buffering off` is useful for streaming chat completions.

When using OIDC behind a proxy, set `PHLOX_GW_OIDC_REDIRECT_URL` to the public HTTPS URL
if the derived callback URL does not match the identity provider registration.

## Backup And Restore

For simple deployments:

1. Stop Phlox-GW.
2. Copy `phlox-gw.db` to backup storage.
3. Start Phlox-GW.

For higher uptime environments, use SQLite backup tooling appropriate for your platform.
Always test restore into a separate data directory before relying on a backup process.

Restore:

1. Stop Phlox-GW.
2. Replace `phlox-gw.db` in the configured data directory.
3. Start Phlox-GW.
4. Check `/api/health` and sign in as an admin.

## Upgrade

1. Back up `phlox-gw.db`.
2. Stop the running process.
3. Replace the binary.
4. Start the process.
5. Check logs and `/api/health`.

Schema migrations run during startup through the store initialization path.

## Health Check

```bash
curl -sS http://127.0.0.1:8080/api/health
```

Expected response:

```json
{
  "name": "phlox-gw",
  "status": "ok",
  "time": "2026-06-24T00:00:00Z"
}
```

## Troubleshooting

### Browser Login Fails After Restart

Set a stable `PHLOX_GW_SESSION_SECRET`. The development secret is generated on startup.

### OIDC Callback Fails

Check:

- `PHLOX_GW_OIDC_ISSUER_URL`.
- Client ID and client secret.
- Redirect URI registered in the identity provider.
- Public callback URL when behind a reverse proxy.
- System clock on the gateway host.

### API Key Request Returns 401

Check:

- The `Authorization: Bearer <key>` header for OpenAI-compatible calls.
- The `x-api-key: <key>` or bearer header for Anthropic-compatible calls.
- Key active state, expiry, and owner active state.
- Whether the key was rotated and the client still has the old plaintext key.

### API Key Request Returns 403

Check model allowlists, route names, user status, and whether an admin disabled the
provider or model.

### API Key Request Returns 402

A user, department, or API-key monthly budget is exhausted for a priced model.

### API Key Request Returns 429

A key, user, department, provider, or model RPM/TPM limit is currently exceeded.

### Provider Is Marked Down

Repeated transport errors, 401/403, 429, or 5xx responses open a provider circuit for a
cooldown period. Check provider credentials, endpoint reachability, upstream quota, and
the provider's last error in the admin UI.

### Tokens Or Cost Are Zero

For providers that return usage metadata, check whether the upstream response includes it.
For OpenAI-compatible text calls that omit usage metadata, Phlox-GW estimates tokens in
memory for future requests. Older rows cannot be backfilled because prompt and response
content are not stored by default.
