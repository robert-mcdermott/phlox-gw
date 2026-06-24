# Phlox-GW API Usage

This guide covers the client-facing gateway APIs. Browser and admin configuration is
available through the embedded dashboard.

## Authentication

Phlox-GW gateway endpoints use Phlox-GW API keys, not browser session cookies.

OpenAI-compatible endpoints accept:

```text
Authorization: Bearer pgw-sk-...
```

Anthropic-compatible endpoints accept either:

```text
x-api-key: pgw-sk-...
```

or:

```text
Authorization: Bearer pgw-sk-...
```

Plaintext API keys are shown only once when minted or rotated. The database stores a hash
and the key prefix for identification.

## Claude Code

Claude Code can target Phlox-GW through the Anthropic-compatible endpoint:

```bash
env \
  ANTHROPIC_BASE_URL="http://127.0.0.1:8080/anthropic" \
  ANTHROPIC_API_KEY="pgw-sk-your-key" \
  ANTHROPIC_MODEL="glm-5.2:cloud" \
  claude
```

Claude Code uses streaming and tools during normal operation. For OpenAI-compatible
routes, Phlox-GW translates Anthropic Messages requests and stream events to and from
OpenAI chat completions. If Claude Code warns that both claude.ai and `ANTHROPIC_API_KEY`
are configured, run `claude /logout` when you want it to use only the gateway API key.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/models` | List enabled models visible through the OpenAI-compatible surface. |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions for OpenAI-compatible providers and Bedrock routes. |
| `POST` | `/anthropic/v1/messages` | Anthropic-compatible messages for Anthropic-compatible providers, with request/response translation to OpenAI-compatible routes and non-streaming translation to Bedrock routes. |
| `GET` | `/api/health` | Unauthenticated process health check. |

## Model Names

Clients send Phlox-GW route IDs in the `model` field. Route IDs are configured in
`Admin -> Models`.

If a model's route ID is blank, Phlox-GW creates:

```text
provider_id/model_id
```

Example:

```text
local-ollama/gemma4:31b-cloud
```

Administrators can also create stable aliases such as:

```text
chat/default
coding/fast
finance/reasoning
```

See [Model Routing](ROUTING.md) for fallback and weighted-routing behavior.

## List Models

```bash
curl -sS http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer pgw-sk-your-key"
```

Successful response shape:

```json
{
  "object": "list",
  "data": [
    {
      "id": "local-ollama/gemma4:31b-cloud",
      "object": "model",
      "owned_by": "local-ollama"
    }
  ]
}
```

## OpenAI-Compatible Chat

```bash
curl -sS http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer pgw-sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "local-ollama/gemma4:31b-cloud",
    "messages": [
      { "role": "system", "content": "Be concise." },
      { "role": "user", "content": "What is Phlox-GW?" }
    ]
  }'
```

Phlox-GW forwards the request to the selected upstream route after enforcing key policy,
budgets, rate limits, and provider health.

For OpenAI-compatible upstreams, Phlox-GW rewrites only the `model` field to the configured
upstream model ID. Other request fields are passed through.

For Bedrock routes, Phlox-GW maps the OpenAI-shaped request into Bedrock Converse or
ConverseStream and maps the response back into an OpenAI-compatible shape.

## Streaming Chat

```bash
curl -N http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer pgw-sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat/default",
    "stream": true,
    "messages": [
      { "role": "user", "content": "Give me a short deployment checklist." }
    ]
  }'
```

Streaming responses are proxied as Server-Sent Events. Disable buffering in reverse
proxies for best results.

When the upstream includes usage metadata, Phlox-GW records that exact usage. When an
OpenAI-compatible text stream omits usage metadata, Phlox-GW estimates text tokens in
memory and records the estimate for chargeback.

## Tool Calls And Images

OpenAI-compatible providers receive tool and image fields as-is.

Bedrock routes support a subset mapped through Bedrock Converse:

- Text chat messages.
- Data URL image inputs where the selected Bedrock model supports images.
- Function tool definitions and tool-call/tool-result round trips where the selected
  Bedrock model supports tools.

Unsupported upstream features fail according to the selected provider's response behavior.

## Anthropic-Compatible Messages

```bash
curl -sS http://127.0.0.1:8080/anthropic/v1/messages \
  -H "x-api-key: pgw-sk-your-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic/claude-3-5-sonnet-latest",
    "max_tokens": 256,
    "messages": [
      { "role": "user", "content": "Explain this gateway in one paragraph." }
    ]
  }'
```

Phlox-GW preserves Anthropic protocol headers such as `anthropic-version` and
`anthropic-beta` when the selected route is backed by an Anthropic-compatible provider.
The model field may also be a Phlox-GW route ID backed by an OpenAI-compatible provider,
such as Ollama, vLLM, LM Studio, OpenRouter, or LiteLLM. In that case Phlox-GW translates
Anthropic Messages requests, tools, tool results, and stream events to OpenAI chat shape
for the upstream call, then returns Anthropic-shaped responses to the client.
Non-streaming Anthropic requests can also target Bedrock routes.

Streaming Anthropic-compatible responses are proxied when `stream` is `true` and the
selected route is backed by an Anthropic-compatible provider. When the selected route is
backed by an OpenAI-compatible provider, Phlox-GW translates streamed OpenAI chat chunks
to Anthropic `message_start`, `content_block_delta`, `tool_use`, and `message_stop`
events. Anthropic streaming translation to Bedrock routes is not implemented yet; use
non-streaming `/anthropic/v1/messages` or `/v1/chat/completions` for those routes. Usage
is captured from compatible stream usage events when present, with token estimates used
when upstream providers omit streaming usage.

## Guardrails

Administrators can enable the guardrail policy from `Admin -> Guardrails`. The built-in
plugin detects email addresses, phone numbers, SSNs, credit-card numbers, and common API
key/token patterns in JSON text fields. Administrators can also add any number of
RE2-compatible custom regex patterns. Custom patterns can redact with a pattern-specific
replacement token or block matching content.

Input actions:

- `off`: do not inspect request content.
- `redact`: replace detected values before dispatching to the upstream provider.
- `block`: reject the request before dispatch.

Output actions:

- `off`: do not inspect response content.
- `redact`: replace detected values before returning the response to the client.
- `block`: block non-streaming responses after provider return. Streaming requests are
  rejected while this mode is active because partial streamed output cannot be recalled.

Guardrail inspection happens in memory. Prompt text and response text are still not stored
by default. The admin preview tool sends sample text only to the local preview endpoint and
does not persist it.

## Common Status Codes

| Status | Meaning |
| --- | --- |
| `200` | Request completed or stream started successfully. |
| `400` | Invalid JSON, missing model, or invalid request shape. |
| `401` | Missing, invalid, expired, or revoked API key. |
| `402` | A user, department, or API-key budget is exhausted for a priced model. |
| `403` | The key or user is not allowed to use the requested route. |
| `404` | Unknown enabled model route, depending on endpoint and request shape. |
| `422` | Non-streaming provider output was blocked by a guardrail policy. |
| `429` | Key, user, department, provider, or model rate limit exceeded. |
| `502` | Upstream provider transport or gateway failure. |
| `503` | Provider circuit open or no usable candidate route. |

Provider responses can also pass through provider-specific error details. The request
metadata log stores bounded error text for troubleshooting.

## Usage Accounting

Each gateway request records:

- Request ID.
- User and department snapshot.
- API key ID, prefix, and display name.
- Provider ID and provider type.
- Model route and upstream model ID.
- Protocol, method, endpoint, and streaming flag.
- Status code and bounded error text.
- Input, output, and total tokens.
- Calculated cost.
- Latency, client IP, and user agent.

Prompt text, response text, image bytes, tool contents, API keys, and provider secrets are
not stored by default.

## Using Existing SDKs

Most OpenAI-compatible SDKs can point at Phlox-GW by setting:

```text
base_url = http://127.0.0.1:8080/v1
api_key = pgw-sk-your-key
model = <Phlox-GW route id>
```

Most Anthropic-compatible SDKs can point at:

```text
base_url = http://127.0.0.1:8080/anthropic
api_key = pgw-sk-your-key
model = <Phlox-GW route id>
```

Exact SDK configuration names vary by language and client library.
