// Typed API client for the Phlox-GW dashboard.
//
// Mirrors the request/response contract of the vanilla `api()` helper in the
// previous frontend (frontend/src/static/app.js): JSON by default, Bearer
// token auth, 204 -> null, and error messages unwrapped from several shapes.

import type {
  AdminApiKey,
  ApiKey,
  AuditLog,
  Budget,
  BudgetBurnDownItem,
  ClusterStatus,
  GuardrailPolicy,
  GuardrailPreviewResult,
  Health,
  LoginResponse,
  Model,
  ModelHealthResult,
  NewApiKeyResponse,
  OidcConfig,
  Provider,
  RateLimit,
  RequestLogFilters,
  RequestLogSearchResult,
  UsageDrilldowns,
  UsageSummary,
  UsageTimeSeriesPoint,
  User,
} from '@/types'

// --- token management -------------------------------------------------------

let authToken = ''

export function setAuthToken(token: string): void {
  authToken = token || ''
}

export function getAuthToken(): string {
  return authToken
}

// --- core request -----------------------------------------------------------

export interface ApiOptions extends Omit<RequestInit, 'body'> {
  body?: BodyInit | null
  /** Set false to omit the Authorization header (public endpoints). */
  auth?: boolean
}

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export async function api<T = unknown>(
  path: string,
  options: ApiOptions = {},
): Promise<T> {
  const { auth = true, headers, ...rest } = options
  const finalHeaders: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(headers as Record<string, string> | undefined),
  }
  if (auth && authToken) {
    finalHeaders.Authorization = `Bearer ${authToken}`
  }

  const res = await fetch(path, { ...rest, headers: finalHeaders })
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const body = await res.json()
      message =
        body?.error?.message ||
        body?.error ||
        body?.message ||
        body?.snippet ||
        message
    } catch {
      // ignore non-JSON error bodies
    }
    throw new ApiError(message, res.status)
  }
  if (res.status === 204) return null as T
  return (await res.json()) as T
}

/** Download a binary response (CSV / signed config) as a Blob with auth. */
async function downloadBlob(path: string): Promise<Blob> {
  const headers: Record<string, string> = {}
  if (authToken) headers.Authorization = `Bearer ${authToken}`
  const res = await fetch(path, { headers })
  if (!res.ok) throw new ApiError(`download failed: ${res.status}`, res.status)
  return res.blob()
}

/** Trigger a browser download of a Blob under the given filename. */
export function saveBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

// --- request-log query builder ----------------------------------------------

export function requestLogParams(
  filters: Partial<RequestLogFilters>,
  paging?: { limit: number; offset: number },
): URLSearchParams {
  const params = new URLSearchParams()
  if (filters.q) params.set('q', filters.q)
  if (filters.days) params.set('days', filters.days)
  if (filters.status && filters.status !== 'any') params.set('status', filters.status)
  if (filters.protocol) params.set('protocol', filters.protocol)
  if (filters.provider_id) params.set('provider_id', filters.provider_id)
  if (filters.model) params.set('model', filters.model)
  if (filters.department) params.set('department', filters.department)
  if (filters.streaming) params.set('streaming', filters.streaming)
  if (paging) {
    params.set('limit', String(paging.limit))
    params.set('offset', String(paging.offset))
  }
  return params
}

function withQuery(path: string, params: URLSearchParams): string {
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

// --- auth --------------------------------------------------------------------

export const Auth = {
  login: (username: string, password: string) =>
    api<LoginResponse>('/api/auth/login', {
      method: 'POST',
      auth: false,
      body: JSON.stringify({ username, password }),
    }),
  me: () => api<User>('/api/auth/me'),
  oidcConfig: () => api<OidcConfig>('/api/auth/oidc/config', { auth: false }),
}

export const health = () => api<Health>('/api/health', { auth: false })

// --- user-facing -------------------------------------------------------------

export const Models = {
  list: () => api<Model[]>('/api/models'),
}

export const Usage = {
  summary: () => api<UsageSummary>('/api/usage'),
  budget: () => api<unknown>('/api/usage/budget'),
}

export const Keys = {
  list: () => api<ApiKey[]>('/api/api-keys'),
  create: (name: string, expires_at: string) =>
    api<NewApiKeyResponse>('/api/api-keys', {
      method: 'POST',
      body: JSON.stringify({ name, expires_at }),
    }),
  update: (id: string, body: { name: string; expires_at: string }) =>
    api<ApiKey>(`/api/api-keys/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  rotate: (id: string) =>
    api<NewApiKeyResponse>(`/api/api-keys/${encodeURIComponent(id)}/rotate`, {
      method: 'POST',
      body: '{}',
    }),
  revoke: (id: string) =>
    api<null>(`/api/api-keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

// --- admin -------------------------------------------------------------------

export const AdminProviders = {
  list: () => api<Provider[]>('/api/admin/providers'),
  create: (body: Partial<Provider> & { api_key?: string }) =>
    api<Provider>('/api/admin/providers', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  update: (id: string, body: Partial<Provider> & { api_key?: string }) =>
    api<Provider>(`/api/admin/providers/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  remove: (id: string) =>
    api<null>(`/api/admin/providers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
}

export const AdminModels = {
  list: () => api<Model[]>('/api/admin/models'),
  create: (body: Partial<Model>) =>
    api<Model>('/api/admin/models', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  update: (id: string, body: Partial<Model>) =>
    api<Model>(`/api/admin/models/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  remove: (id: string) =>
    api<null>(`/api/admin/models/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  test: (id: string) =>
    api<ModelHealthResult>(`/api/admin/models/${encodeURIComponent(id)}/test`, {
      method: 'POST',
      body: '{}',
    }),
}

export const AdminUsers = {
  list: () => api<User[]>('/api/admin/users'),
  create: (body: {
    username: string
    password: string
    email: string
    display_name: string
    department: string
    role: string
  }) =>
    api<User>('/api/admin/users', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  update: (id: string, body: Partial<User>) =>
    api<User>(`/api/admin/users/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  resetPassword: (id: string, password: string) =>
    api<null>(
      `/api/admin/users/${encodeURIComponent(id)}/reset-password`,
      { method: 'POST', body: JSON.stringify({ password }) },
    ),
  remove: (id: string) =>
    api<null>(`/api/admin/users/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
}

export const AdminBudgets = {
  list: () => api<Budget[]>('/api/admin/budgets'),
  create: (body: {
    scope_type: string
    scope_value: string
    limit_usd: number
    warn_pct: number
  }) =>
    api<Budget>('/api/admin/budgets', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  update: (id: string, body: Partial<Budget>) =>
    api<Budget>(`/api/admin/budgets/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  remove: (id: string) =>
    api<null>(`/api/admin/budgets/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  burndown: () => api<BudgetBurnDownItem[]>('/api/admin/budgets/burndown'),
}

export const AdminRateLimits = {
  list: () => api<RateLimit[]>('/api/admin/rate-limits'),
  create: (body: {
    scope_type: string
    scope_value: string
    rpm_limit: number
    tpm_limit: number
  }) =>
    api<RateLimit>('/api/admin/rate-limits', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  update: (id: string, body: Partial<RateLimit>) =>
    api<RateLimit>(`/api/admin/rate-limits/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  remove: (id: string) =>
    api<null>(`/api/admin/rate-limits/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
}

export const AdminKeys = {
  list: () => api<AdminApiKey[]>('/api/admin/api-keys'),
  update: (id: string, body: Partial<AdminApiKey>) =>
    api<AdminApiKey>(`/api/admin/api-keys/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
    }),
  rotate: (id: string) =>
    api<NewApiKeyResponse>(
      `/api/admin/api-keys/${encodeURIComponent(id)}/rotate`,
      { method: 'POST', body: '{}' },
    ),
  revoke: (id: string) =>
    api<null>(`/api/admin/api-keys/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
}

export const AdminAudit = {
  list: (limit = 100) =>
    api<AuditLog[]>(`/api/admin/audit-log?limit=${limit}`),
}

export const AdminCluster = {
  status: () => api<ClusterStatus>('/api/admin/cluster/status'),
  nodes: () => api<ClusterStatus['nodes']>('/api/admin/cluster/nodes'),
}

export const AdminRequestLog = {
  search: (filters: Partial<RequestLogFilters>, paging: { limit: number; offset: number }) =>
    api<RequestLogSearchResult>(
      withQuery('/api/admin/request-log', requestLogParams(filters, paging)),
    ),
  exportCsv: (filters: Partial<RequestLogFilters>) =>
    downloadBlob(
      withQuery('/api/admin/request-log/export.csv', requestLogParams(filters)),
    ),
}

export const AdminGuardrails = {
  get: () => api<GuardrailPolicy>('/api/admin/guardrails'),
  update: (policy: GuardrailPolicy) =>
    api<GuardrailPolicy>('/api/admin/guardrails', {
      method: 'PUT',
      body: JSON.stringify(policy),
    }),
  preview: (policy: GuardrailPolicy, text: string, phase: string) =>
    api<GuardrailPreviewResult>('/api/admin/guardrails/test', {
      method: 'POST',
      body: JSON.stringify({ policy, text, phase }),
    }),
}

export const AdminUsage = {
  summary: () => api<UsageSummary>('/api/admin/usage/summary'),
  timeseries: (days = 30) =>
    api<UsageTimeSeriesPoint[]>(`/api/admin/usage/timeseries?days=${days}`),
  drilldowns: (days = 30) =>
    api<UsageDrilldowns>(`/api/admin/usage/drilldowns?days=${days}`),
  exportCsv: () => downloadBlob('/api/admin/usage/export.csv'),
}

export const AdminConfig = {
  export: () => downloadBlob('/api/admin/config/export'),
}
