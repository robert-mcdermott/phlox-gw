import { useStore } from '@/store'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { OnOffPill } from '@/components/common'
import { money } from '@/lib/format'

export function ModelsPage() {
  const models = useStore((s) => s.models)

  return (
    <Card>
      <CardHeader>
        <CardTitle>Enabled models</CardTitle>
      </CardHeader>
      <CardContent>
        {models.length === 0 ? (
          <p className="text-sm text-muted-foreground">No records yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Route</TableHead>
                <TableHead>Display name</TableHead>
                <TableHead>Input / 1M</TableHead>
                <TableHead>Output / 1M</TableHead>
                <TableHead>Streaming</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {models.map((m) => (
                <TableRow key={m.id}>
                  <TableCell className="font-mono">{m.route}</TableCell>
                  <TableCell>{m.display_name}</TableCell>
                  <TableCell>{money(m.input_cost_per_million)}</TableCell>
                  <TableCell>{money(m.output_cost_per_million)}</TableCell>
                  <TableCell><OnOffPill on={m.supports_streaming} /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
