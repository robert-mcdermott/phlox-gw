import { useStore } from '@/store'
import { AdminConfig, saveBlob } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { AdminPanel, MetricStrip, MiniMetric, useAdminAction } from './shared'

export function ConfigTab() {
  const providers = useStore((s) => s.providers)
  const adminModels = useStore((s) => s.adminModels)
  const budgets = useStore((s) => s.budgets)
  const rateLimits = useStore((s) => s.rateLimits)
  const guardrailPolicy = useStore((s) => s.guardrailPolicy)
  const run = useAdminAction()

  const download = () =>
    run(
      async () => {
        const blob = await AdminConfig.export()
        saveBlob(blob, `phlox-gw-config-${new Date().toISOString().slice(0, 10)}.json`)
      },
      { refresh: false },
    )

  return (
    <AdminPanel
      title="Signed configuration export"
      note="Downloads provider, model, pricing, budget, rate-limit, and guardrail configuration without secrets or usage data."
    >
      <div className="space-y-4">
        <MetricStrip>
          <MiniMetric label="Providers" value={providers.length} />
          <MiniMetric label="Models" value={adminModels.length} />
          <MiniMetric label="Budgets" value={budgets.length} />
          <MiniMetric label="Rate limits" value={rateLimits.length} />
          <MiniMetric label="Guardrails" value={guardrailPolicy?.enabled ? 'enabled' : 'disabled'} />
        </MetricStrip>
        <div className="flex flex-wrap items-center gap-3">
          <Button onClick={download}>Download signed JSON</Button>
          <span className="text-xs text-muted-foreground">
            Excludes direct provider secrets, user credentials, API key hashes, sessions, usage ledger
            rows, request logs, and audit logs.
          </span>
        </div>
      </div>
    </AdminPanel>
  )
}
