# Architecture

This is a code map for contributors: what the major pieces are, how a request
flows through them, and which files to open for a given kind of change. It
deliberately describes roles and boundaries rather than listing functions —
file contents change often, but the shape described here should stay stable.
If a refactor changes that shape, update this document in the same PR.

For product scope and system design decisions, see [DESIGN.md](DESIGN.md).
For operating the gateway, see [OPERATIONS.md](OPERATIONS.md).

## Bird's eye view

Phlox-GW is a single Go binary: an LLM gateway that exposes OpenAI-compatible
and Anthropic-compatible endpoints in front of many upstream providers
(OpenAI-compatible servers, Anthropic, Azure OpenAI, Azure AI Foundry, Google
Gemini, AWS Bedrock), plus an embedded admin dashboard. Everything persists to
a single database (SQLite by default, Postgres optional).

```text
cmd/phlox-gw/        Entry point: loads config, opens the store, builds the
                     HTTP handler, starts the server
internal/config/     Environment variable loading (PHLOX_GW_*) into a Config struct
internal/store/      All persistence: schema, migrations, and query methods on *Store
internal/httpapi/    All HTTP: admin API, auth, gateway endpoints, protocol bridges
internal/auth/       Password hashing, API key generation/hashing, signed session tokens
internal/telemetry/  Prometheus metrics and OpenTelemetry tracing
frontend/            Dashboard source (src/static) and built assets (dist/, embedded
                     into the binary via embed.go at the repo root)
scripts/             Local run, release build, and demo seeding scripts
```

There are only two big packages, and they have a strict one-way relationship:
`httpapi` calls `store`; `store` knows nothing about HTTP.

## The gateway request lifecycle

The most useful thing to internalize is the path a `/v1/chat/completions` or
`/anthropic/v1/messages` request takes. Each step names the file that owns it
(all in `internal/httpapi/`):

1. **`server.go`** — `New()` registers every route on the mux and wraps the
   whole tree in the request-logging/telemetry middleware. If a URL doesn't
   behave, start here to see what handler it maps to.
2. **`middleware.go`** — `requireAPIKey` authenticates the caller's gateway
   key (`requireSession`/`requireAdmin` do the same for dashboard routes).
3. **`gateway.go`** — the thin entry points (`openAIChatCompletions`,
   `anthropicMessages`, `openAIModels`). They parse the body, apply input
   guardrails, and orchestrate the steps below.
4. **`guardrails.go`** — PII/API-key/custom-regex evaluation and redaction,
   applied to input here and to output later in the protocol files.
5. **`routing.go`** — `resolveRoutePlan` turns the requested model route into
   an ordered list of candidates (primary, weighted splits, fallbacks), and
   `executeOpenAIPlan`/`executeAnthropicPlan` walk the candidates with retry
   and per-attempt timeouts. Route-level admission is checked per candidate.
6. **`policy.go`** — the atomic gates routing consults: API key allowlists
   and key budgets, user/department budgets, RPM/TPM rate limits, and the
   provider circuit-breaker write path.
7. **Protocol execution** — a candidate is called using its provider's native
   protocol:
   - **`openai.go`** — OpenAI-protocol calls and SSE streaming, token
     estimation for streams without usage chunks, and the retry that swaps
     `max_tokens` for `max_completion_tokens` on reasoning-model rejections.
   - **`anthropic.go`** — Anthropic Messages calls and SSE streaming.
   - **`bridge_anthropic_openai.go`** — translation layer used when an
     Anthropic-format request routes to an OpenAI-protocol provider:
     request/response/tool-call conversion and the streaming state machine.
   - **`bridge_bedrock.go`** — translation to/from Bedrock Converse and
     ConverseStream, including image and tool mapping, plus the Bedrock
     client construction and credential handling.
8. **`usage.go`** — `recordUsage` writes the usage ledger row (tokens, cost,
   latency, status) that budgets, rate limits, and analytics all read. Every
   protocol path ends here, streaming or not.

Provider health outcomes (`recordProviderOutcome` in `policy.go`) feed the
circuit breaker that `routing.go` and model health checks respect.

## internal/httpapi — the rest of the files

Admin/dashboard REST handlers, one file per resource: **`users.go`**,
**`apikeys.go`** (self-service and admin), **`providers.go`** (also owns the
per-provider-type protocol helpers: endpoints, auth headers, Azure
api-version handling), **`models.go`** (CRUD plus health-check execution),
**`budgets.go`**, **`ratelimits.go`**.

Supporting files: **`auth.go`** (local login and the full OIDC/Entra ID
flow), **`analytics.go`** (usage summaries, drilldowns, chargeback,
audit-log and request-log reads/CSV, and the `audit()` writer),
**`playground.go`** (admin playground chat), **`cluster.go`** (heartbeats,
node status, readiness gating), **`config_export.go`** (signed config
export), **`types.go`** (small shared types like `upstreamResult`), and
**`server.go`** (the `Options`/`Server` structs, DI interfaces for Bedrock
and OIDC, mux wiring, `/health` and `/ready`).

## internal/store

Everything is a method on `*store.Store`. **This package cannot be split into
sub-packages**: Go forbids defining methods on a type from another package,
so new persistence code goes in a file here, in the same package.

One file per entity, each containing the entity's struct, CRUD methods, and
row scanner: **`users.go`**, **`apikeys.go`**, **`providers.go`**,
**`models.go`** (including route resolution used by the gateway),
**`budgets.go`**, **`ratelimits.go`**, **`guardrails.go`**, **`usage.go`**
(ledger writes plus all reporting queries), **`requestlog.go`**,
**`auditlog.go`**, **`cluster.go`**.

Infrastructure: **`store.go`** (the `Store` struct, `Open`, dialect handling,
and the low-level `exec`/`query`/`rebind` primitives), **`schema.go`** (DDL,
migrations, and seed data — SQLite and Postgres variants live together here),
**`helpers.go`** (cross-cutting formatting/parsing helpers).

## Things that will trip you up

- **Two `guardrails.go` files is intentional.** `internal/store/guardrails.go`
  is policy persistence; `internal/httpapi/guardrails.go` is evaluation,
  redaction, and the admin handlers.
- **Dialect differences live in `store`.** Queries are written once and
  `rebind` adapts placeholders for Postgres; schema differences are handled
  in `schema.go`. Don't branch on driver anywhere else.
- **External dependencies are injected via `httpapi.Options`.** Tests replace
  the Bedrock client (`BedrockClientFactory`), the OIDC provider
  (`OIDCAuthenticator`), and the upstream HTTP client (`HTTPClient`). If you
  add a new external integration, add a seam like these rather than calling
  out directly, or it won't be testable.
- **Secrets never leave redacted.** Provider API keys are write-only through
  the admin API and redacted in exports; follow the existing patterns in
  `providers.go` and `config_export.go`.

## Tests

Test files mirror source files in both packages (`providers_test.go` tests
`providers.go`, etc.), with two deviations in `httpapi`:

- **`helpers_test.go`** holds shared fixtures: `jsonRequest`, `sessionToken`,
  `decodeRecorder`, `fakeBedrockClient`, `fakeOIDCAuthenticator`,
  `roundTripFunc`.
- **`gateway_test.go`** holds the end-to-end integration tests that drive a
  full request through the mux (routing, fallback, streaming, guardrails,
  provider-specific paths). If your test constructs a handler with `New()`
  and hits a gateway endpoint, it probably belongs here; if it tests one
  function directly, put it in that function's file.

Store tests open a real throwaway SQLite database per test — there are no
mocks of the store, and migrations are exercised on every `Open`.

## Common changes and where to make them

- **New provider type**: protocol helpers and validation in
  `httpapi/providers.go`; if it speaks the OpenAI or Anthropic protocol you
  may only need endpoint/auth-header cases there. A genuinely new protocol
  needs its own execution path (see `bridge_bedrock.go` as the template) and
  a case in the gateway/routing dispatch. Also update the provider type
  check in `store/schema.go` (it's enforced by a DB constraint) and
  [PROVIDERS.md](PROVIDERS.md).
- **New admin resource**: store entity file + schema addition in
  `store/schema.go`; handler file in `httpapi` + routes in `server.go`'s
  `New()`; audit events via `analytics.go`'s `audit()`; matching test files.
- **New policy/limit**: the gate in `httpapi/policy.go`, wired into
  `checkRouteAdmission` in `routing.go`; persistence in the relevant store
  file.
- **New usage/reporting view**: query in `store/usage.go`, handler in
  `httpapi/analytics.go`.
- **Config option**: `internal/config/config.go` (env var), consumed via
  `Options.Config`; document it in [OPERATIONS.md](OPERATIONS.md).

## Development

```sh
go build ./...                # build everything
go test ./...                 # full test suite (uses throwaway SQLite files)
gofmt -l internal/ cmd/       # must print nothing
go vet ./...                  # must be clean
scripts/run-local.sh          # run locally with a dev data directory
```

The frontend is plain static assets: edit `frontend/src/static`, build into
`frontend/dist` with `frontend/build.mjs` (see `frontend/package.json`), and
the Go binary embeds `frontend/dist` at compile time.
