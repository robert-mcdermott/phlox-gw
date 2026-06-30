import { Check } from 'lucide-react'
import { useStore } from '@/store'
import type { AdminTab } from '@/store'
import { cn } from '@/lib/utils'
import { ADMIN_SECTIONS, TOP_TABS } from '@/lib/nav'
import { THEMES } from '@/lib/theme'
import phloxLogo from '/phlox-logo.svg'

function useAdminSectionCount(): (id: AdminTab) => string | number | null {
  const clusterStatus = useStore((s) => s.clusterStatus)
  const requestLog = useStore((s) => s.requestLog)
  const guardrailPolicy = useStore((s) => s.guardrailPolicy)
  const providers = useStore((s) => s.providers)
  const adminModels = useStore((s) => s.adminModels)
  const users = useStore((s) => s.users)
  const adminKeys = useStore((s) => s.adminKeys)
  const rateLimits = useStore((s) => s.rateLimits)
  const budgets = useStore((s) => s.budgets)
  const auditLogs = useStore((s) => s.auditLogs)

  const counts: Record<string, string | number> = {
    cluster: clusterStatus?.cluster_enabled ? clusterStatus?.active_node_count || 0 : 'off',
    requests: requestLog?.total || 0,
    guardrails: guardrailPolicy?.enabled ? 'on' : 'off',
    config: 'JSON',
    providers: providers.length,
    models: adminModels.length,
    users: users.length,
    keys: adminKeys.length,
    limits: rateLimits.length,
    budgets: budgets.length,
    audit: auditLogs.length,
  }
  return (id: AdminTab) => (id in counts ? counts[id] : null)
}

function AdminSubnav() {
  const adminTab = useStore((s) => s.adminTab)
  const setAdminTab = useStore((s) => s.setAdminTab)
  const sectionCount = useAdminSectionCount()

  return (
    <div className="mt-1 mb-1 ml-3 flex flex-col gap-0.5 border-l border-sidebar-border/40 pl-2">
      {ADMIN_SECTIONS.map((section) => {
        const Icon = section.icon
        const count = sectionCount(section.id)
        const active = adminTab === section.id
        return (
          <button
            key={section.id}
            type="button"
            onClick={() => setAdminTab(section.id)}
            className={cn(
              'flex items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors',
              active
                ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                : 'text-sidebar-foreground/70 hover:bg-sidebar-accent/50 hover:text-sidebar-foreground',
            )}
          >
            <Icon className="size-4 shrink-0" />
            <span className="flex-1 truncate">{section.label}</span>
            {count !== null ? (
              <span className="text-xs text-sidebar-foreground/50">{count}</span>
            ) : null}
          </button>
        )
      })}
    </div>
  )
}

function ThemePicker() {
  const theme = useStore((s) => s.theme)
  const setTheme = useStore((s) => s.setTheme)
  return (
    <div className="mt-auto border-t border-sidebar-border/40 p-3">
      <p className="mb-2 px-1 text-xs font-medium uppercase tracking-wide text-sidebar-foreground/50">
        Theme
      </p>
      <div className="grid grid-cols-4 gap-1.5">
        {THEMES.map((t) => {
          const active = theme === t.id
          return (
            <button
              key={t.id}
              type="button"
              title={t.name}
              aria-label={t.name}
              aria-pressed={active}
              onClick={() => setTheme(t.id)}
              className={cn(
                'relative flex h-8 items-center justify-center overflow-hidden rounded-md border transition-all',
                active
                  ? 'border-sidebar-foreground ring-1 ring-sidebar-foreground'
                  : 'border-sidebar-border/50 hover:border-sidebar-foreground/60',
              )}
            >
              <span className="flex h-full w-full">
                {t.swatch.map((color, i) => (
                  <span
                    key={i}
                    className="h-full flex-1"
                    style={{ background: color }}
                  />
                ))}
              </span>
              {active ? (
                <span className="absolute inset-0 flex items-center justify-center">
                  <Check className="size-3.5 text-white drop-shadow" />
                </span>
              ) : null}
            </button>
          )
        })}
      </div>
    </div>
  )
}

export function Sidebar() {
  const tab = useStore((s) => s.tab)
  const setTab = useStore((s) => s.setTab)
  const user = useStore((s) => s.user)
  const isAdmin = user?.role === 'admin'

  return (
    <aside className="flex w-64 shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground">
      <div className="flex items-center gap-3 p-4">
        <img src={phloxLogo} alt="" className="h-9 w-9" />
        <div>
          <h1 className="text-sm font-semibold leading-tight">Phlox-GW</h1>
          <p className="text-xs text-sidebar-foreground/60">LLM gateway</p>
        </div>
      </div>
      <nav className="flex flex-1 flex-col gap-0.5 overflow-y-auto px-3">
        {TOP_TABS.map((t) => {
          if (t.id === 'admin' && !isAdmin) return null
          const Icon = t.icon
          const active = tab === t.id
          return (
            <div key={t.id}>
              <button
                type="button"
                onClick={() => setTab(t.id)}
                className={cn(
                  'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left text-sm font-medium transition-colors',
                  active
                    ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                    : 'text-sidebar-foreground/80 hover:bg-sidebar-accent/50 hover:text-sidebar-foreground',
                )}
              >
                <Icon className="size-4 shrink-0" />
                <span>{t.label}</span>
              </button>
              {t.id === 'admin' && active && isAdmin ? <AdminSubnav /> : null}
            </div>
          )
        })}
      </nav>
      <ThemePicker />
    </aside>
  )
}
