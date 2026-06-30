// Global app store (Zustand). Holds auth/session state and all data slices,
// and exposes a `refresh()` action that mirrors the load sequence of the
// previous vanilla frontend (frontend/src/static/app.js).

import { create } from 'zustand'
import {
  AdminAudit,
  AdminBudgets,
  AdminCluster,
  AdminGuardrails,
  AdminKeys,
  AdminModels,
  AdminProviders,
  AdminRateLimits,
  AdminRequestLog,
  AdminUsage,
  AdminUsers,
  ApiError,
  Auth,
  Keys,
  Models,
  Usage,
  health as fetchHealth,
  setAuthToken,
} from '@/lib/api'
import { applyTheme, initialTheme } from '@/lib/theme'
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
  Model,
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

const TOKEN_STORAGE_KEY = 'phlox_gw_token'

export const DEFAULT_REQUEST_FILTERS: RequestLogFilters = {
  q: '',
  days: '7',
  status: 'any',
  protocol: '',
  provider_id: '',
  model: '',
  department: '',
  streaming: '',
}

const EMPTY_REQUEST_LOG: RequestLogSearchResult = {
  items: [],
  total: 0,
  limit: 100,
  offset: 0,
}

export function defaultGuardrailPolicy(): GuardrailPolicy {
  return {
    id: 'default',
    enabled: false,
    input_action: 'redact',
    output_action: 'redact',
    detect_email: true,
    detect_phone: true,
    detect_ssn: true,
    detect_credit_card: true,
    detect_api_key: true,
    custom_patterns: [],
    redaction_text: '[REDACTED]',
    streaming_block_mode: 'reject',
  }
}

function readToken(): string {
  try {
    return localStorage.getItem(TOKEN_STORAGE_KEY) || ''
  } catch {
    return ''
  }
}

export type TopTab = 'overview' | 'keys' | 'models' | 'usage' | 'appearance' | 'admin'
export type AdminTab =
  | 'operations'
  | 'cluster'
  | 'requests'
  | 'guardrails'
  | 'config'
  | 'providers'
  | 'models'
  | 'users'
  | 'keys'
  | 'limits'
  | 'budgets'
  | 'audit'

export interface AppState {
  // session
  token: string
  user: User | null
  tab: TopTab
  adminTab: AdminTab
  theme: string

  // status flags
  loading: boolean
  error: string
  notice: string

  // public data
  health: Health | null
  oidcConfig: OidcConfig

  // user data
  models: Model[]
  keys: ApiKey[]
  usage: UsageSummary | null
  secret: string

  // admin data
  providers: Provider[]
  users: User[]
  budgets: Budget[]
  rateLimits: RateLimit[]
  adminUsage: UsageSummary | null
  adminModels: Model[]
  adminKeys: AdminApiKey[]
  auditLogs: AuditLog[]
  requestLog: RequestLogSearchResult
  requestFilters: RequestLogFilters
  guardrailPolicy: GuardrailPolicy | null
  guardrailPreview: GuardrailPreviewResult | null
  guardrailPreviewText: string
  clusterStatus: ClusterStatus | null
  usageSeries: UsageTimeSeriesPoint[]
  usageDrilldowns: UsageDrilldowns
  budgetBurnDown: BudgetBurnDownItem[]

  // actions
  setTab: (tab: TopTab) => void
  setAdminTab: (tab: AdminTab) => void
  setTheme: (id: string) => void
  setError: (message: string) => void
  setNotice: (message: string) => void
  setSecret: (secret: string) => void
  setGuardrailPolicy: (policy: GuardrailPolicy) => void
  setGuardrailPreview: (preview: GuardrailPreviewResult | null) => void
  setGuardrailPreviewText: (text: string) => void
  setRequestFilters: (filters: RequestLogFilters) => void
  setRequestLogOffset: (offset: number) => void
  login: (username: string, password: string) => Promise<void>
  logout: () => void
  refresh: () => Promise<void>
}

export const useStore = create<AppState>((set, get) => {
  const initialToken = readToken()
  setAuthToken(initialToken)

  return {
    token: initialToken,
    user: null,
    tab: 'overview',
    adminTab: 'operations',
    theme: initialTheme(),

    loading: false,
    error: '',
    notice: '',

    health: null,
    oidcConfig: { enabled: false, display_name: 'Entra ID' },

    models: [],
    keys: [],
    usage: null,
    secret: '',

    providers: [],
    users: [],
    budgets: [],
    rateLimits: [],
    adminUsage: null,
    adminModels: [],
    adminKeys: [],
    auditLogs: [],
    requestLog: EMPTY_REQUEST_LOG,
    requestFilters: { ...DEFAULT_REQUEST_FILTERS },
    guardrailPolicy: null,
    guardrailPreview: null,
    guardrailPreviewText:
      'Contact me at jane@example.com, call 206-555-2407, employee EMP-12345.',
    clusterStatus: null,
    usageSeries: [],
    usageDrilldowns: { providers: [], models: [] },
    budgetBurnDown: [],

    setTab: (tab) => set({ tab }),
    setAdminTab: (adminTab) => set({ adminTab }),
    setTheme: (id) => set({ theme: applyTheme(id) }),
    setError: (error) => set({ error }),
    setNotice: (notice) => set({ notice }),
    setSecret: (secret) => set({ secret }),
    setGuardrailPolicy: (guardrailPolicy) => set({ guardrailPolicy }),
    setGuardrailPreview: (guardrailPreview) => set({ guardrailPreview }),
    setGuardrailPreviewText: (guardrailPreviewText) => set({ guardrailPreviewText }),
    setRequestFilters: (requestFilters) => set({ requestFilters }),
    setRequestLogOffset: (offset) =>
      set((state) => ({ requestLog: { ...state.requestLog, offset } })),

    login: async (username, password) => {
      const resp = await Auth.login(username, password)
      setAuthToken(resp.token)
      try {
        localStorage.setItem(TOKEN_STORAGE_KEY, resp.token)
      } catch {
        // ignore storage failures
      }
      set({ token: resp.token })
      await get().refresh()
    },

    logout: () => {
      setAuthToken('')
      try {
        localStorage.removeItem(TOKEN_STORAGE_KEY)
      } catch {
        // ignore storage failures
      }
      set({ token: '', user: null })
    },

    refresh: async () => {
      set({ loading: true, error: '' })
      try {
        const [health, oidcConfig] = await Promise.all([
          fetchHealth(),
          Auth.oidcConfig(),
        ])
        set({ health, oidcConfig })

        if (!get().token) {
          set({ loading: false })
          return
        }

        const user = await Auth.me()
        const [models, keys, usage] = await Promise.all([
          Models.list(),
          Keys.list(),
          Usage.summary(),
        ])
        set({
          user,
          models: models || [],
          keys: keys || [],
          usage,
        })

        if (user.role === 'admin') {
          const { requestFilters, requestLog } = get()
          const [
            providers,
            users,
            budgets,
            rateLimits,
            adminUsage,
            adminModels,
            adminKeys,
            auditLogs,
            requestLogResult,
            guardrailPolicy,
            clusterStatus,
            usageSeries,
            usageDrilldowns,
            budgetBurnDown,
          ] = await Promise.all([
            AdminProviders.list(),
            AdminUsers.list(),
            AdminBudgets.list(),
            AdminRateLimits.list(),
            AdminUsage.summary(),
            AdminModels.list(),
            AdminKeys.list(),
            AdminAudit.list(100),
            AdminRequestLog.search(requestFilters, {
              limit: requestLog.limit || 100,
              offset: requestLog.offset || 0,
            }),
            AdminGuardrails.get(),
            AdminCluster.status(),
            AdminUsage.timeseries(30),
            AdminUsage.drilldowns(30),
            AdminBudgets.burndown(),
          ])
          set({
            providers: providers || [],
            users: users || [],
            budgets: budgets || [],
            rateLimits: rateLimits || [],
            adminUsage,
            adminModels: adminModels || [],
            adminKeys: adminKeys || [],
            auditLogs: auditLogs || [],
            requestLog: requestLogResult || EMPTY_REQUEST_LOG,
            guardrailPolicy: guardrailPolicy || defaultGuardrailPolicy(),
            clusterStatus: clusterStatus || null,
            usageSeries: usageSeries || [],
            usageDrilldowns: usageDrilldowns || { providers: [], models: [] },
            budgetBurnDown: budgetBurnDown || [],
          })
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err)
        set({ error: message })
        if (
          err instanceof ApiError &&
          (message.includes('invalid session') || message.includes('missing bearer'))
        ) {
          get().logout()
        }
      } finally {
        set({ loading: false })
      }
    },
  }
})
