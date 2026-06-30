import { useStore } from '@/store'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableRow } from '@/components/ui/table'
import { StatCard } from '@/components/common'
import { money } from '@/lib/format'

export function OverviewPage() {
  const health = useStore((s) => s.health)
  const models = useStore((s) => s.models)
  const keys = useStore((s) => s.keys)
  const usage = useStore((s) => s.usage)

  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Gateway" value={health?.status || 'unknown'} sub="Embedded Go server" />
        <StatCard label="Enabled models" value={models.length} sub="OpenAI/Anthropic catalog" />
        <StatCard label="Your API keys" value={keys.filter((k) => k.is_active).length} sub="Active user-owned keys" />
        <StatCard label="Your spend" value={money(usage?.cost_usd || 0)} sub={`${usage?.total_tokens || 0} tokens`} />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Gateway endpoints</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableBody>
              <TableRow>
                <TableCell className="font-medium">OpenAI-compatible</TableCell>
                <TableCell className="font-mono">POST /v1/chat/completions</TableCell>
              </TableRow>
              <TableRow>
                <TableCell className="font-medium">Model list</TableCell>
                <TableCell className="font-mono">GET /v1/models</TableCell>
              </TableRow>
              <TableRow>
                <TableCell className="font-medium">Anthropic-compatible</TableCell>
                <TableCell className="font-mono">POST /anthropic/v1/messages</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  )
}
