# Model Routing

Phlox-GW exposes model routes to clients. A route is the model name a client sends in
`model` for `/v1/chat/completions` or `/anthropic/v1/messages`.

## Fields

### Provider

The provider is the backend profile that owns the upstream endpoint or service. Examples:

- `local-ollama`
- `openai`
- `anthropic`
- `bedrock`

### Upstream Model ID

The upstream model id is the model name Phlox-GW sends to that provider after routing.
Examples:

- `gemma4:31b-cloud`
- `gpt-4o-mini`
- `claude-3-5-sonnet-20241022`
- `anthropic.claude-3-5-sonnet-20241022-v2:0`

### Route ID

The route id is the public model name clients use when calling Phlox-GW.

If `Route id` is blank, Phlox-GW creates the route as:

```text
provider_id/model_id
```

For example, with provider `local-ollama` and upstream model id
`gemma4:31b-cloud`, the default route is:

```text
local-ollama/gemma4:31b-cloud
```

You can also set a stable custom route, such as:

```text
chat/default
coding/fast
finance/reasoning
```

Clients then use that route:

```json
{
  "model": "chat/default",
  "messages": [
    { "role": "user", "content": "Hello" }
  ]
}
```

## Fallback Routes

Fallback routes are existing route ids to try when the selected backend fails or is blocked
by an open health circuit. Enter one route id per line, in priority order.

Example:

```text
openai/gpt-4o-mini
local-vllm/llama-3.1-8b
local-ollama/gemma4:31b-cloud
```

Behavior:

- Phlox-GW tries the requested route first, unless weighted routing selects another first
  candidate.
- If that candidate fails, Phlox-GW tries fallback routes in order.
- Retry attempts are applied per candidate before moving to the next fallback.
- Unknown or disabled fallback route ids are ignored at dispatch time.
- Usage, latency, health, and cost are recorded against the actual backend route that was
  attempted.

## Weighted Routes

Weighted routes split traffic across existing route ids. Enter one route id and one
positive integer weight per line.

Example:

```text
openai/gpt-4o-mini 80
local-vllm/llama-3.1-8b 20
```

The weights are relative. In this example, roughly 80 percent of requests select
`openai/gpt-4o-mini` first, and roughly 20 percent select `local-vllm/llama-3.1-8b`
first.

Equivalent `=` syntax is also accepted:

```text
openai/gpt-4o-mini=80
local-vllm/llama-3.1-8b=20
```

Behavior:

- Weighted routes choose the first backend candidate for a request.
- If weighted routes are blank, Phlox-GW starts with the requested route.
- Unknown or disabled weighted route ids are ignored.
- If every weighted entry is invalid, Phlox-GW starts with the requested route.
- Fallback routes are still the ordered failover list after the selected backend.

If a weighted backend should also be tried as a failover after another weighted backend
fails, include it in `Fallback routes` as well.

## Common Patterns

### Direct Model

Use the default route when you want clients to target a specific provider/model pair.

```text
Provider: local-ollama
Upstream model id: gemma4:31b-cloud
Route id: blank
Resulting route: local-ollama/gemma4:31b-cloud
```

### Stable Alias

Use a custom route when clients should not care which provider hosts the model.

```text
Provider: local-ollama
Upstream model id: gemma4:31b-cloud
Route id: chat/default
```

Clients call `chat/default`. Administrators can later change the provider, upstream model,
fallback routes, weighted routes, or prices without changing client configuration.

### Failover

Use fallback routes when one primary route should have ordered backups.

```text
Route id: chat/default
Fallback routes:
openai/gpt-4o-mini
local-vllm/llama-3.1-8b
```

### Traffic Split

Use weighted routes when one public route should distribute requests across multiple
backends.

```text
Route id: chat/default
Weighted routes:
openai/gpt-4o-mini 80
local-vllm/llama-3.1-8b 20
Fallback routes:
openai/gpt-4o-mini
local-vllm/llama-3.1-8b
local-ollama/gemma4:31b-cloud
```

This keeps the client-facing route stable while administrators shift traffic between
backends.
