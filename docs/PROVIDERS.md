# Provider Setup Guide

Provider rows describe where Phlox-GW sends requests after model routing. Create them in
`Admin -> Providers`, then attach model rows in `Admin -> Models`. This guide covers every
provider type with the exact values to enter in each field.

## Summary

| Provider type | Typical base URL | Notes |
| --- | --- | --- |
| `openai` | `https://api.openai.com/v1` | Also works for OpenRouter, LiteLLM, vLLM, Ollama, LM Studio, and other OpenAI-compatible APIs. |
| `anthropic` | `https://api.anthropic.com` | Phlox-GW appends `/v1/messages`. |
| `azure-openai` | `https://myresource.openai.azure.com` | Azure OpenAI deployments via the per-deployment data-plane API. |
| `azure-anthropic` | `https://myresource.services.ai.azure.com/anthropic` | Claude deployments in Azure AI Foundry via the Anthropic Messages API. |
| `google` | blank | Google Gemini through the Gemini API (AI Studio API keys). |
| `bedrock` | blank | AWS Bedrock in the configured region; no base URL or API-key field. |

## API Keys And Secrets

Every non-Bedrock provider accepts either a direct API key or an environment variable
reference:

- **API key env var**: the name of an environment variable (for example `OPENAI_API_KEY`)
  that holds the key on the machine running Phlox-GW. Preferred for production because
  the key never enters the database.
- **Direct API key**: the key itself, stored on the gateway. Convenient for local
  testing. When both are set, the environment variable wins.

Stored secrets (direct API keys, AWS secret keys, session tokens, and Bedrock API keys)
are write-only in the admin UI: they are never returned to the browser, and saving a
provider with a blank secret field keeps the stored value. Changing a provider's type or
authentication method clears credentials that no longer apply.

After creating a provider and a model, use the model's **Test** button in
`Admin -> Models` or the `Admin -> Playground` to verify the configuration end to end.

## OpenAI And OpenAI-Compatible Services

The `openai` type covers OpenAI itself and any service exposing an OpenAI-compatible
chat-completions API. Phlox-GW appends `/chat/completions`, so the base URL should
normally end at `/v1`.

| Field | Value |
| --- | --- |
| Type | OpenAI-compatible (`openai`) |
| Base URL | `https://api.openai.com/v1` |
| API key | An OpenAI API key, or an env var reference such as `OPENAI_API_KEY` |
| Model: upstream model id | The model name, for example `gpt-4o` or `gpt-5.5` |

Common OpenAI-compatible services and their base URLs:

| Service | Base URL | Notes |
| --- | --- | --- |
| Ollama | `http://localhost:11434/v1` | Must be reachable from the Phlox-GW host; no API key needed by default. |
| vLLM | `http://localhost:8000/v1` | |
| LM Studio | `http://localhost:1234/v1` | |
| OpenRouter | `https://openrouter.ai/api/v1` | |
| LiteLLM | your LiteLLM proxy URL ending in `/v1` | |

### Reasoning Models

Reasoning-family models (the GPT-5 family, o-series) reject the legacy `max_tokens`
parameter in favor of `max_completion_tokens`, and only accept the default temperature.
Phlox-GW detects this rejection and retries automatically with adjusted parameters —
on model health tests, the admin playground, Anthropic-protocol requests translated to
an OpenAI-compatible route, and `/v1/chat/completions` requests (streaming and
non-streaming). The retry is reactive: requests are passed through unchanged first, and
only a 400 that names the offending parameter triggers a resend, so providers that
accept the original payload (Ollama, OpenRouter, vLLM, and the like) are unaffected.
This applies to all OpenAI-protocol provider types, including Azure OpenAI.

## Anthropic

| Field | Value |
| --- | --- |
| Type | Anthropic-compatible (`anthropic`) |
| Base URL | `https://api.anthropic.com` |
| API key | An Anthropic API key, or an env var reference such as `ANTHROPIC_API_KEY` |
| Model: upstream model id | The model name, for example `claude-sonnet-4-5` |

Phlox-GW appends `/v1/messages` and forwards Anthropic version and beta headers. The
client-facing `/anthropic/v1/messages` endpoint can also translate Anthropic Messages
requests to OpenAI-compatible and Bedrock routes, including streaming text and tool-use
events, so Anthropic-protocol clients are not limited to Anthropic providers.

The translation also works in the other direction: `/v1/chat/completions` requests that
route to an Anthropic-protocol provider (`anthropic` or `azure-anthropic`) are converted
to Anthropic Messages requests, including streaming, images, and tool calls, so
OpenAI-protocol clients can use Claude models directly or in Azure AI Foundry. Requests
that omit `max_tokens` get a default of 4096, since the Messages API requires it. Newer
Claude models (Opus 4.7+, Sonnet 5) reject the `temperature`, `top_p`, and `top_k`
sampling parameters that OpenAI clients often hardcode; when the upstream 400 names the
offending parameter, the gateway retries with it removed, so those clients keep working
unchanged.

## Azure OpenAI

Azure exposes two different APIs, so Phlox-GW has two Azure provider types. This one is
for Azure OpenAI deployments.

| Field | Value |
| --- | --- |
| Type | Azure OpenAI (`azure-openai`) |
| Base URL | `https://myresource.openai.azure.com` — the resource endpoint only, with any path dropped |
| API version | Optional, for example `2024-12-01-preview`; blank uses `2024-10-21` |
| API key | The Azure OpenAI resource's API key, or an env var reference |
| Model: upstream model id | The **deployment name** (for example `gpt-4o` or whatever you named the deployment), not the underlying model name |

Both values come from the deployment's details page in the Azure portal / Foundry: the
endpoint URL supplies the base URL (drop everything after the host), and the key is shown
alongside it. Phlox-GW sends requests to
`/openai/deployments/{deployment}/chat/completions?api-version=...` with the `api-key`
header.

Azure resources using the newer Azure OpenAI v1 API surface
(`https://myresource.openai.azure.com/openai/v1`) can also be added as a plain `openai`
provider, since that surface accepts a Bearer token and takes the model name in the
request body. The `azure-openai` type targets the classic per-deployment data-plane API.

## Claude In Azure AI Foundry

Claude models in Azure AI Foundry only speak the Anthropic Messages API, so they use
their own provider type.

| Field | Value |
| --- | --- |
| Type | Azure Anthropic (Foundry) (`azure-anthropic`) |
| Base URL | `https://myresource.services.ai.azure.com/anthropic` — the `/anthropic` suffix is required; without it Azure returns 404 |
| API key | The Claude deployment's API key, or an env var reference |
| Model: upstream model id | The deployment name, for example `claude-sonnet-5` |

Phlox-GW appends `/v1/messages` and calls the deployment through the Anthropic Messages
API, so these routes support the same pass-through and streaming behavior as any
Anthropic-compatible provider. They are also reachable from `/v1/chat/completions`
through the gateway's OpenAI-to-Anthropic request/response translation.

### Anthropic Beta Header Filtering

Anthropic clients such as Claude Code advertise optional beta features through the
`anthropic-beta` request header (for example
`anthropic-beta: advisor-tool-2026-03-01,interleaved-thinking-2025-05-14`). The
first-party Anthropic API ignores values it does not recognize, but Azure AI Foundry
validates the header and rejects the entire request with a 400 error when any value is
unknown to it:

```
API Error: 400 Unexpected value(s) `advisor-tool-2026-03-01` for the `anthropic-beta` header.
```

To prevent this, Phlox-GW filters the `anthropic-beta` header per provider before
forwarding. For `azure-anthropic` providers, values are kept only when they match a
built-in allowlist of feature prefixes that Foundry supports (extended thinking,
fine-grained tool streaming, computer use, context management, files, skills, code
execution, MCP, prompt caching, and related families). Values from betas that exist only
on the first-party API — such as `advisor-tool-*`, `claude-code-*`, `fast-mode-*`, and
`task-budgets-*` — are dropped. Matching is on the prefix before the date suffix, so the
filter keeps working when Anthropic bumps a beta's version date. Plain `anthropic`
providers are never filtered by default.

The defaults can be overridden per provider with the **Beta header allowlist** field on
the provider form (API field: `beta_header_prefixes`), one prefix per line:

```
interleaved-thinking-
fine-grained-tool-streaming-
computer-use-
```

- **Blank** (the default) uses the built-in allowlist for the provider type.
- **A list of prefixes** replaces the built-in defaults entirely — list every family the
  provider should receive. Matching is a case-insensitive prefix match against each
  comma-separated header value.
- **A single `*` entry** disables filtering and passes every client value through
  unchanged.

The override is also honored on plain `anthropic` providers, where it can be used to
restrict which betas clients may request. When values are dropped, the gateway logs a
debug-level `dropped unsupported anthropic-beta values` entry naming the provider and the
removed values, so you can tell why a client-requested beta feature is inactive.

## Google Gemini

The `google` provider type calls the Gemini API (the Google AI Studio service) through
Google's OpenAI-compatible surface with standard Bearer authentication.

| Field | Value |
| --- | --- |
| Type | Google Gemini (`google`) |
| Base URL | Leave blank — defaults to `https://generativelanguage.googleapis.com/v1beta/openai` |
| API key | An API key from [Google AI Studio](https://aistudio.google.com/), or an env var reference such as `GEMINI_API_KEY` |
| Model: upstream model id | The Gemini model name, for example `gemini-3.5-flash` |

Streamed requests automatically ask Gemini to include token usage in the final stream
chunk, so streaming usage is billed from real counts.

Google's **Vertex AI** is a different service: it uses GCP project- and region-scoped
endpoints with OAuth or service-account credentials rather than API keys, and is not yet
supported (see the roadmap).

## AWS Bedrock

Bedrock providers have no base URL or API-key field; they call Bedrock Converse and
ConverseStream in the configured AWS region.

| Field | Value |
| --- | --- |
| Type | AWS Bedrock (`bedrock`) |
| AWS region | For example `us-east-1`; blank falls back to `AWS_REGION` on the gateway host |
| Authentication | One of the three methods below |
| Model: upstream model id | The Bedrock model or inference-profile ID, for example `us.anthropic.claude-sonnet-4-5-20250929-v1:0` or `amazon.nova-pro-v1:0` |

Bedrock models are exposed through both the OpenAI-compatible `/v1/chat/completions`
surface and the Anthropic-compatible `/anthropic/v1/messages` surface, with streaming,
image input, and function tool-call translation where the selected model supports them.

### Authentication Methods

**AWS credential chain** (default). No credentials are stored on the provider. The AWS
SDK resolves credentials from standard sources on the gateway host:

```bash
export AWS_PROFILE="my-sso-profile"
export AWS_REGION="us-east-1"
```

or:

```bash
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_SESSION_TOKEN="..."
export AWS_REGION="us-east-1"
```

Instance roles, task roles, and SSO-backed profiles are also supported by the AWS SDK. An
`AWS_BEARER_TOKEN_BEDROCK` environment variable is honored as well.

**Access key & secret.** Credentials entered on the provider row and stored on the
gateway, used instead of the environment credential chain:

| Field | Value |
| --- | --- |
| Access key id | The IAM access key id, for example `AKIA...` |
| Secret access key | The matching secret key (write-only) |
| Session token | Optional; only for temporary credentials (write-only) |

**Bedrock API key.** A single Bedrock API key entered on the provider row and sent to
Bedrock as a Bearer token:

| Field | Value |
| --- | --- |
| Bedrock API key | The key generated in the AWS console (write-only) |
