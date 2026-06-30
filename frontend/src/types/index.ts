// Shared API types for the Phlox-GW dashboard.
//
// Field names and casing mirror the Go JSON responses in
// internal/store/store.go and internal/httpapi/server.go. Go `time.Time`
// fields serialize to RFC3339 strings, so timestamps are typed as `string`
// (nullable timestamps may be `null` or absent).

export type Role = 'user' | 'admin'

export type ProviderType = 'openai' | 'anthropic' | 'bedrock'

export type ScopeType = 'user' | 'department' | 'provider' | 'model'

export type GuardrailAction = 'off' | 'redact' | 'block'

export type GuardrailPhase = 'input' | 'output'

// ---------------------------------------------------------------------------
// Auth & session
// ---------------------------------------------------------------------------

export interface User {
  id: string
  username: string
  email: string
  display_name: string
  department: string
  role: Role
  auth_provider: string
  is_active: boolean
  created_at: string
  updated_at: string
  last_login_at?: string | null
}

export interface LoginResponse {
  token: string
  user: User
}

export interface Health {
  status: string
  name: string
  time: string
  deployment_mode: string
  instance_id: string
}

export interface OidcConfig {
  enabled: boolean
  display_name: string
}

// ---------------------------------------------------------------------------
// Models & providers
// ---------------------------------------------------------------------------

export interface Model {
  id: string
  provider_id: string
  model_id: string
  route: string
  display_name: string
  input_cost_per_million: number
  output_cost_per_million: number
  context_window: number
  supports_streaming: boolean
  enabled: boolean
  fallback_routes: string
  weighted_routes: string
  retry_attempts: number
  request_timeout_ms: number
  health_routing_enabled: boolean
  created_at: string
  updated_at: string
}

export interface Provider {
  id: string
  name: string
  type: ProviderType
  base_url: string
  api_key_env: string
  aws_region: string
  enabled: boolean
  health_status: string
  consecutive_failures: number
  last_health_check_at?: string | null
  last_error: string
  circuit_open_until?: string | null
  created_at: string
  updated_at: string
}

export interface ModelHealthResult {
  ok: boolean
  provider_id: string
  model: string
  protocol: string
  status_code: number
  latency_ms: number
  error?: string
  snippet?: string
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

export interface ApiKey {
  id: string
  user_id: string
  name: string
  prefix: string
  is_active: boolean
  expires_at?: string | null
  last_used_at?: string | null
  created_at: string
  budget_usd: number
  rpm_limit: number
  tpm_limit: number
  model_allowlist: string
}

// Admin view embeds ApiKey plus owner/department/spend.
export interface AdminApiKey extends ApiKey {
  username: string
  department: string
  monthly_spend_usd: number
}

// POST /api/api-keys and rotate endpoints return the plaintext secret once.
export interface NewApiKeyResponse {
  key: string
  record: ApiKey
}

// ---------------------------------------------------------------------------
// Budgets & rate limits
// ---------------------------------------------------------------------------

export interface Budget {
  id: string
  scope_type: 'user' | 'department'
  scope_value: string
  limit_usd: number
  warn_pct: number
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface RateLimit {
  id: string
  scope_type: ScopeType
  scope_value: string
  rpm_limit: number
  tpm_limit: number
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface BudgetBurnDownItem {
  budget: Budget
  spend_usd: number
  remaining_usd: number
  ratio: number
  daily_average_usd: number
  projected_month_end_usd: number
  projected_ratio: number
  days_elapsed: number
  days_remaining: number
  blocked: boolean
  warning: boolean
}

export interface BudgetLineItem {
  budget: Budget
  spend_usd: number
  ratio: number
  blocked: boolean
  warning: boolean
}

export interface BudgetStatus {
  blocked: boolean
  warning: boolean
  reason: string
  items: BudgetLineItem[]
}

// ---------------------------------------------------------------------------
// Usage & analytics
// ---------------------------------------------------------------------------

export interface UsageSummaryByModel {
  model: string
  provider_id: string
  department?: string
  username?: string
  requests: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
}

export interface UsageSummary {
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  requests: number
  by_model: UsageSummaryByModel[]
}

export interface UsageTimeSeriesPoint {
  date: string
  requests: number
  errors: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
}

export interface UsageDrilldownRow {
  provider_id: string
  model?: string
  requests: number
  errors: number
  error_rate: number
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  avg_latency_ms: number
  last_used_at?: string | null
}

export interface UsageDrilldowns {
  providers: UsageDrilldownRow[]
  models: UsageDrilldownRow[]
}

// ---------------------------------------------------------------------------
// Request log & audit
// ---------------------------------------------------------------------------

export interface RequestLogRecord {
  id: string
  request_id: string
  user_id: string
  username: string
  department: string
  api_key_id: string
  api_key_prefix: string
  api_key_name: string
  provider_id: string
  provider_type: string
  model_route: string
  upstream_model_id: string
  protocol: string
  method: string
  endpoint: string
  streaming: boolean
  input_tokens: number
  output_tokens: number
  total_tokens: number
  cost_usd: number
  latency_ms: number
  status_code: number
  error_text: string
  client_ip: string
  user_agent: string
  created_at: string
}

export interface RequestLogSearchResult {
  items: RequestLogRecord[]
  total: number
  limit: number
  offset: number
}

export interface RequestLogFilters {
  q: string
  days: string
  status: string
  protocol: string
  provider_id: string
  model: string
  department: string
  streaming: string
}

export interface AuditLog {
  id: string
  actor_user_id: string
  actor_username: string
  action: string
  target_type: string
  target_id: string
  target_display: string
  details: string
  ip_address: string
  user_agent: string
  created_at: string
}

// ---------------------------------------------------------------------------
// Guardrails
// ---------------------------------------------------------------------------

export interface GuardrailCustomPattern {
  id: string
  name: string
  pattern: string
  action: GuardrailAction
  redaction_text: string
  enabled: boolean
}

export interface GuardrailPolicy {
  id: string
  enabled: boolean
  input_action: GuardrailAction
  output_action: GuardrailAction
  detect_email: boolean
  detect_phone: boolean
  detect_ssn: boolean
  detect_credit_card: boolean
  detect_api_key: boolean
  custom_patterns: GuardrailCustomPattern[]
  redaction_text: string
  streaming_block_mode: string
  created_at?: string
  updated_at?: string
}

export interface GuardrailPreviewResult {
  phase: string
  action: string
  findings: string[]
  redacted: boolean
  blocked: boolean
  output: string
}

// ---------------------------------------------------------------------------
// Cluster
// ---------------------------------------------------------------------------

export interface ClusterNodeResponse {
  instance_id: string
  hostname: string
  version: string
  addr: string
  deployment_mode: string
  db_driver: string
  status: string
  started_at: string
  last_seen_at: string
  age_seconds: number
  stale: boolean
  current: boolean
  metadata: string
}

export interface ClusterStatus {
  deployment_mode: string
  cluster_enabled: boolean
  database_driver: string
  database_target: string
  instance_id: string
  hostname: string
  addr: string
  version: string
  started_at: string
  last_heartbeat_at?: string | null
  last_heartbeat_error: string
  heartbeat_interval_seconds: number
  node_stale_after_seconds: number
  status: string
  active_node_count: number
  stale_node_count: number
  total_node_count: number
  signing_key_shared: boolean
  signing_key_path: string
  notes: string[]
  nodes: ClusterNodeResponse[]
}
