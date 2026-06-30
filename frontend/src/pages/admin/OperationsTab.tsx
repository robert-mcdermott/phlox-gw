import { useStore } from '@/store'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { StatCard } from '@/components/common'
import { BarChart } from '@/components/charts-lazy'
import { AdminPanel, EmptyNote, MetricStrip, MiniMetric, TableScroll } from './shared'
import { money, compact, percent, fmt, weightedAverage } from '@/lib/format'
import type { UsageDrilldownRow } from '@/types'

function DrilldownTable({
  rows,
  withModel,
}: {
  rows: UsageDrilldownRow[]
  withModel: boolean
}) {
  if (!rows.length) {
    return <EmptyNote>No {withModel ? 'model' : 'provider'} usage in the last 30 days.</EmptyNote>
  }
  return (
    <TableScroll>
      <Table>
        <TableHeader>
          <TableRow>
            {withModel ? <TableHead>Model</TableHead> : null}
            <TableHead>Provider</TableHead>
            <TableHead>Requests</TableHead>
            <TableHead>Errors</TableHead>
            <TableHead>Error rate</TableHead>
            <TableHead>Tokens</TableHead>
            <TableHead>Cost</TableHead>
            <TableHead>Avg latency</TableHead>
            <TableHead>Last used</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r, i) => (
            <TableRow key={`${r.provider_id}-${r.model ?? ''}-${i}`}>
              {withModel ? <TableCell className="font-mono">{r.model || '(none)'}</TableCell> : null}
              <TableCell className="font-mono">{r.provider_id || '(none)'}</TableCell>
              <TableCell>{compact(r.requests)}</TableCell>
              <TableCell>{compact(r.errors)}</TableCell>
              <TableCell>{percent(r.error_rate)}</TableCell>
              <TableCell>{compact(r.total_tokens)}</TableCell>
              <TableCell>{money(r.cost_usd)}</TableCell>
              <TableCell>{Math.round(Number(r.avg_latency_ms || 0))} ms</TableCell>
              <TableCell>{fmt(r.last_used_at)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableScroll>
  )
}

export function OperationsTab() {
  const usage = useStore((s) => s.adminUsage)
  const users = useStore((s) => s.users)
  const providers = useStore((s) => s.providers)
  const budgets = useStore((s) => s.budgets)
  const rateLimits = useStore((s) => s.rateLimits)
  const adminKeys = useStore((s) => s.adminKeys)
  const auditLogs = useStore((s) => s.auditLogs)
  const series = useStore((s) => s.usageSeries)
  const drilldowns = useStore((s) => s.usageDrilldowns)

  const totalRequests = series.reduce((sum, r) => sum + Number(r.requests || 0), 0)
  const totalErrors = series.reduce((sum, r) => sum + Number(r.errors || 0), 0)
  const errorRate = totalRequests ? totalErrors / totalRequests : 0
  const avgLatency = weightedAverage(series, 'avg_latency_ms', 'requests')

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-7">
        <StatCard label="Users" value={users.length} sub="Local accounts" />
        <StatCard label="Providers" value={providers.length} sub="Configured providers" />
        <StatCard label="Budgets" value={budgets.length} sub="Monthly limits" />
        <StatCard label="Rate limits" value={rateLimits.length} sub="By scope" />
        <StatCard label="API keys" value={adminKeys.length} sub="User-owned" />
        <StatCard label="Audit events" value={auditLogs.length} sub="Recent activity" />
        <StatCard
          label="Total spend"
          value={money(usage?.cost_usd || 0)}
          sub={`${usage?.requests || 0} requests`}
        />
      </div>

      <AdminPanel title="Operations" note="Last 30 days">
        {series.length === 0 ? (
          <EmptyNote>No usage data yet.</EmptyNote>
        ) : (
          <div className="space-y-5">
            <MetricStrip>
              <MiniMetric label="30d requests" value={compact(totalRequests)} />
              <MiniMetric label="30d errors" value={compact(totalErrors)} />
              <MiniMetric label="Error rate" value={percent(errorRate)} />
              <MiniMetric label="Avg latency" value={`${Math.round(avgLatency)} ms`} />
            </MetricStrip>
            <div className="grid gap-4 lg:grid-cols-2">
              <BarChart title="Daily cost" data={series} dataKey="cost_usd" formatter={money} />
              <BarChart title="Daily tokens" data={series} dataKey="total_tokens" formatter={compact} />
              <BarChart title="Daily requests" data={series} dataKey="requests" formatter={compact} />
              <BarChart title="Daily errors" data={series} dataKey="errors" formatter={compact} />
            </div>
          </div>
        )}
      </AdminPanel>

      <AdminPanel title="Provider drilldown" note="Last 30 days">
        <DrilldownTable rows={drilldowns.providers} withModel={false} />
      </AdminPanel>

      <AdminPanel title="Model drilldown" note="Last 30 days">
        <DrilldownTable rows={drilldowns.models} withModel />
      </AdminPanel>
    </div>
  )
}
