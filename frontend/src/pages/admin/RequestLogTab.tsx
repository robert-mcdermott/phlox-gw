import { useState } from 'react'
import { useStore, DEFAULT_REQUEST_FILTERS } from '@/store'
import { AdminRequestLog, saveBlob } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { StatusCodePill } from '@/components/common'
import { AdminPanel, EmptyNote, FormField, TableScroll, useAdminAction } from './shared'
import { compact, money, fmt } from '@/lib/format'
import type { RequestLogFilters } from '@/types'

export function RequestLogTab() {
  const providers = useStore((s) => s.providers)
  const adminModels = useStore((s) => s.adminModels)
  const users = useStore((s) => s.users)
  const requestLog = useStore((s) => s.requestLog)
  const filters = useStore((s) => s.requestFilters)
  const setRequestFilters = useStore((s) => s.setRequestFilters)
  const setRequestLogOffset = useStore((s) => s.setRequestLogOffset)
  const setRequestLog = useStore.setState
  const setError = useStore((s) => s.setError)
  const run = useAdminAction()

  // Local draft of the filter form; committed to the store on Apply.
  const [draft, setDraft] = useState<RequestLogFilters>(filters)
  const patch = (p: Partial<RequestLogFilters>) => setDraft((prev) => ({ ...prev, ...p }))

  const limit = requestLog.limit || 100
  const offset = Number(requestLog.offset || 0)
  const total = Number(requestLog.total || 0)
  const rows = requestLog.items || []
  const start = rows.length ? offset + 1 : 0
  const end = offset + rows.length

  const search = async (f: RequestLogFilters, nextOffset: number) => {
    try {
      const result = await AdminRequestLog.search(f, { limit, offset: nextOffset })
      setRequestLog({ requestLog: result })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const apply = () => {
    setRequestFilters(draft)
    setRequestLogOffset(0)
    void search(draft, 0)
  }

  const reset = () => {
    const fresh = { ...DEFAULT_REQUEST_FILTERS }
    setDraft(fresh)
    setRequestFilters(fresh)
    setRequestLogOffset(0)
    void search(fresh, 0)
  }

  const page = (delta: number) => {
    const next = Math.max(0, offset + delta * limit)
    setRequestLogOffset(next)
    void search(filters, next)
  }

  const exportCsv = () =>
    run(
      async () => {
        const blob = await AdminRequestLog.exportCsv(filters)
        saveBlob(blob, `phlox-gw-requests-${new Date().toISOString().slice(0, 10)}.csv`)
      },
      { refresh: false },
    )

  const departments = [...new Set(users.map((u) => u.department).filter(Boolean))]

  return (
    <div className="space-y-6">
      <AdminPanel
        title="Request metadata search"
        note="Operational metadata only. Prompt text, response text, image bytes, tool contents, and secrets are not stored."
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <FormField label="Search">
            <Input placeholder="request id, user, key, provider, model, error" value={draft.q} onChange={(e) => patch({ q: e.target.value })} />
          </FormField>
          <FormField label="Days">
            <Input type="number" min={1} max={365} step={1} value={draft.days} onChange={(e) => patch({ days: e.target.value })} />
          </FormField>
          <FormField label="Status">
            <NativeSelect value={draft.status} onChange={(e) => patch({ status: e.target.value })}>
              <option value="any">Any</option>
              <option value="success">Success</option>
              <option value="error">Error</option>
              <option value="4xx">4xx</option>
              <option value="5xx">5xx</option>
            </NativeSelect>
          </FormField>
          <FormField label="Protocol">
            <NativeSelect value={draft.protocol} onChange={(e) => patch({ protocol: e.target.value })}>
              <option value="">Any</option>
              <option value="openai">OpenAI</option>
              <option value="anthropic">Anthropic</option>
              <option value="bedrock">Bedrock</option>
            </NativeSelect>
          </FormField>
          <FormField label="Streaming">
            <NativeSelect value={draft.streaming} onChange={(e) => patch({ streaming: e.target.value })}>
              <option value="">Any</option>
              <option value="true">Streaming</option>
              <option value="false">Non-streaming</option>
            </NativeSelect>
          </FormField>
          <FormField label="Provider">
            <NativeSelect value={draft.provider_id} onChange={(e) => patch({ provider_id: e.target.value })}>
              <option value="">Any</option>
              {providers.map((p) => (
                <option key={p.id} value={p.id}>{p.id}</option>
              ))}
            </NativeSelect>
          </FormField>
          <FormField label="Model route">
            <NativeSelect value={draft.model} onChange={(e) => patch({ model: e.target.value })}>
              <option value="">Any</option>
              {adminModels.map((m) => (
                <option key={m.id} value={m.route}>{m.route}</option>
              ))}
            </NativeSelect>
          </FormField>
          <FormField label="Department">
            <Input list="request-departments" placeholder="Department" value={draft.department} onChange={(e) => patch({ department: e.target.value })} />
            <datalist id="request-departments">
              {departments.map((d) => (
                <option key={d} value={d} />
              ))}
            </datalist>
          </FormField>
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button onClick={apply}>Apply</Button>
          <Button variant="outline" onClick={reset}>Reset</Button>
          <Button variant="outline" onClick={exportCsv}>Export CSV</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Request log">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <span className="text-sm text-muted-foreground">
            {compact(total)} matching requests · showing {compact(start)}-{compact(end)}
          </span>
          <div className="flex gap-1">
            <Button size="sm" variant="outline" disabled={offset <= 0} onClick={() => page(-1)}>Previous</Button>
            <Button size="sm" variant="outline" disabled={end >= total} onClick={() => page(1)}>Next</Button>
          </div>
        </div>
        {rows.length === 0 ? (
          <EmptyNote>No request metadata matches the current filters.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Request</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>Department</TableHead>
                  <TableHead>Key</TableHead>
                  <TableHead>Provider</TableHead>
                  <TableHead>Model</TableHead>
                  <TableHead>Protocol</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Stream</TableHead>
                  <TableHead>Tokens</TableHead>
                  <TableHead>Cost</TableHead>
                  <TableHead>Latency</TableHead>
                  <TableHead>Error</TableHead>
                  <TableHead>Client IP</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((item) => (
                  <TableRow key={item.id}>
                    <TableCell>{fmt(item.created_at)}</TableCell>
                    <TableCell className="font-mono">{item.request_id}</TableCell>
                    <TableCell className="font-mono">{item.username || item.user_id || ''}</TableCell>
                    <TableCell>{item.department || ''}</TableCell>
                    <TableCell><span className="font-mono">{item.api_key_prefix || item.api_key_id || ''}</span> {item.api_key_name || ''}</TableCell>
                    <TableCell><span className="font-mono">{item.provider_id || ''}</span> <span className="text-muted-foreground">{item.provider_type || ''}</span></TableCell>
                    <TableCell><span className="font-mono">{item.model_route || ''}</span><br /><span className="text-muted-foreground">{item.upstream_model_id || ''}</span></TableCell>
                    <TableCell className="font-mono">{item.protocol || ''}</TableCell>
                    <TableCell className="font-mono">{item.method || ''} {item.endpoint || ''}</TableCell>
                    <TableCell><StatusCodePill code={item.status_code} errorText={item.error_text} /></TableCell>
                    <TableCell>{item.streaming ? <Badge variant="success">on</Badge> : <Badge variant="destructive">off</Badge>}</TableCell>
                    <TableCell>{compact(item.total_tokens || 0)} <span className="text-muted-foreground">{compact(item.input_tokens || 0)}/{compact(item.output_tokens || 0)}</span></TableCell>
                    <TableCell>{money(item.cost_usd || 0)}</TableCell>
                    <TableCell>{compact(item.latency_ms || 0)} ms</TableCell>
                    <TableCell className="max-w-48 whitespace-normal break-words text-xs">{item.error_text || ''}</TableCell>
                    <TableCell className="font-mono">{item.client_ip || ''}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableScroll>
        )}
      </AdminPanel>
    </div>
  )
}
