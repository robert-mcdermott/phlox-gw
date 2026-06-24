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

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/models` | List enabled models visible through the OpenAI-compatible surface. |
| `POST` | `/v1/chat/completions` | OpenAI-compatible chat completions for OpenAI-compatible providers and Bedrock routes. |
| `POST` | `/anthropic/v1/messages` | Anthropic-compatible messages for Anthropic-compatible providers. |
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
`anthropic-beta`. The model field must be a Phlox-GW route ID backed by an
Anthropic-compatible provider.

Streaming Anthropic-compatible responses are proxied when `stream` is `true`. Usage is
captured from compatible `message_start` and `message_delta` stream events when present.

## Common Status Codes

| Status | Meaning |
| --- | --- |
| `200` | Request completed or stream started successfully. |
| `400` | Invalid JSON, missing model, or invalid request shape. |
| `401` | Missing, invalid, expired, or revoked API key. |
| `402` | A user, department, or API-key budget is exhausted for a priced model. |
| `403` | The key or user is not allowed to use the requested route. |
| `404` | Unknown enabled model route, depending on endpoint and request shape. |
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
