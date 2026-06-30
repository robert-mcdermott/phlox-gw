import { useStore } from '@/store'
import { ADMIN_SECTIONS } from '@/lib/nav'
import { OperationsTab } from './admin/OperationsTab'
import { ClusterTab } from './admin/ClusterTab'
import { RequestLogTab } from './admin/RequestLogTab'
import { GuardrailTab } from './admin/GuardrailTab'
import { ConfigTab } from './admin/ConfigTab'
import { ProvidersTab } from './admin/ProvidersTab'
import { AdminModelsTab } from './admin/AdminModelsTab'
import { UsersTab } from './admin/UsersTab'
import { AdminKeysTab } from './admin/AdminKeysTab'
import { RateLimitsTab } from './admin/RateLimitsTab'
import { BudgetsTab } from './admin/BudgetsTab'
import { AuditLogsTab } from './admin/AuditLogsTab'

function AdminTabContent() {
  const adminTab = useStore((s) => s.adminTab)
  switch (adminTab) {
    case 'cluster':
      return <ClusterTab />
    case 'requests':
      return <RequestLogTab />
    case 'guardrails':
      return <GuardrailTab />
    case 'config':
      return <ConfigTab />
    case 'providers':
      return <ProvidersTab />
    case 'models':
      return <AdminModelsTab />
    case 'users':
      return <UsersTab />
    case 'keys':
      return <AdminKeysTab />
    case 'limits':
      return <RateLimitsTab />
    case 'budgets':
      return <BudgetsTab />
    case 'audit':
      return <AuditLogsTab />
    case 'operations':
    default:
      return <OperationsTab />
  }
}

export function AdminPage() {
  const user = useStore((s) => s.user)
  const adminTab = useStore((s) => s.adminTab)

  if (user?.role !== 'admin') {
    return (
      <div className="rounded-xl border bg-card p-8 text-card-foreground">
        <p className="text-sm text-muted-foreground">Admin role required.</p>
      </div>
    )
  }

  const section = ADMIN_SECTIONS.find((s) => s.id === adminTab) ?? ADMIN_SECTIONS[0]

  return (
    <div className="space-y-5">
      <div>
        <h3 className="text-lg font-semibold">{section.label}</h3>
        <p className="text-sm text-muted-foreground">{section.description}</p>
      </div>
      <AdminTabContent />
    </div>
  )
}
