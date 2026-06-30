import { useStore } from '@/store'
import { AdminUsage, saveBlob } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { StatCard } from '@/components/common'
import { BarChart } from '@/components/charts-lazy'
import { money } from '@/lib/format'

export function UsagePage() {
  const usage = useStore((s) => s.usage)
  const user = useStore((s) => s.user)
  const setError = useStore((s) => s.setError)
  const u = usage
  const byModel = u?.by_model || []

  // Aggregate cost per model route for the chart (rows may repeat per
  // department/user); show the top routes by spend.
  const costByModel = (() => {
    const totals = new Map<string, number>()
    for (const r of byModel) {
      totals.set(r.model, (totals.get(r.model) || 0) + Number(r.cost_usd || 0))
    }
    return [...totals.entries()]
      .map(([model, cost_usd]) => ({ model, cost_usd }))
      .sort((a, b) => b.cost_usd - a.cost_usd)
      .slice(0, 10)
  })()

  const exportCsv = async () => {
    try {
      const blob = await AdminUsage.exportCsv()
      saveBlob(blob, `phlox-gw-usage-${new Date().toISOString().slice(0, 10)}.csv`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Requests" value={u?.requests || 0} sub="Your gateway calls" />
        <StatCard label="Input tokens" value={u?.input_tokens || 0} sub="Prompt tokens" />
        <StatCard label="Output tokens" value={u?.output_tokens || 0} sub="Completion tokens" />
        <StatCard label="Cost" value={money(u?.cost_usd || 0)} sub="Priced models only" />
      </div>

      {byModel.length > 0 ? (
        <BarChart
          title="Cost by model"
          data={costByModel}
          dataKey="cost_usd"
          xKey="model"
          formatter={money}
        />
      ) : null}

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle>Usage by model</CardTitle>
          {user?.role === 'admin' ? (
            <Button size="sm" variant="outline" onClick={exportCsv}>Export CSV</Button>
          ) : null}
        </CardHeader>
        <CardContent>
          {byModel.length === 0 ? (
            <p className="text-sm text-muted-foreground">No records yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Model</TableHead>
                  <TableHead>Department</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>Requests</TableHead>
                  <TableHead>Tokens</TableHead>
                  <TableHead>Cost</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {byModel.map((r, i) => (
                  <TableRow key={`${r.model}-${i}`}>
                    <TableCell className="font-mono">{r.model}</TableCell>
                    <TableCell>{r.department || ''}</TableCell>
                    <TableCell>{r.username || ''}</TableCell>
                    <TableCell>{r.requests}</TableCell>
                    <TableCell>{r.total_tokens}</TableCell>
                    <TableCell>{money(r.cost_usd)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
