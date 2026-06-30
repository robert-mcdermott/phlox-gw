import { useState } from 'react'
import { useStore } from '@/store'
import { AdminModels } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { NativeSelect } from '@/components/ui/native-select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, CheckField, EmptyNote, FormField, TableScroll, useAdminAction } from './shared'
import type { Model } from '@/types'

interface NewModel {
  provider_id: string
  model_id: string
  route: string
  display_name: string
  input_cost_per_million: number
  output_cost_per_million: number
  context_window: number
  fallback_routes: string
  weighted_routes: string
  retry_attempts: number
  request_timeout_ms: number
  health_routing_enabled: boolean
  supports_streaming: boolean
  enabled: boolean
}

function emptyNew(providerId: string): NewModel {
  return {
    provider_id: providerId,
    model_id: '',
    route: '',
    display_name: '',
    input_cost_per_million: 0,
    output_cost_per_million: 0,
    context_window: 0,
    fallback_routes: '',
    weighted_routes: '',
    retry_attempts: 0,
    request_timeout_ms: 0,
    health_routing_enabled: true,
    supports_streaming: true,
    enabled: true,
  }
}

type Edit = Omit<NewModel, never>

function editFromModel(m: Model): Edit {
  return {
    provider_id: m.provider_id,
    model_id: m.model_id,
    route: m.route,
    display_name: m.display_name,
    input_cost_per_million: m.input_cost_per_million,
    output_cost_per_million: m.output_cost_per_million,
    context_window: m.context_window,
    fallback_routes: m.fallback_routes || '',
    weighted_routes: m.weighted_routes || '',
    retry_attempts: m.retry_attempts || 0,
    request_timeout_ms: m.request_timeout_ms || 0,
    health_routing_enabled: m.health_routing_enabled !== false,
    supports_streaming: m.supports_streaming,
    enabled: m.enabled,
  }
}

export function AdminModelsTab() {
  const models = useStore((s) => s.adminModels)
  const providers = useStore((s) => s.providers)
  const setNotice = useStore((s) => s.setNotice)
  const setError = useStore((s) => s.setError)
  const run = useAdminAction()

  const [draft, setDraft] = useState<NewModel>(() => emptyNew(providers[0]?.id ?? ''))
  const [edits, setEdits] = useState<Record<string, Edit>>({})

  const editOf = (m: Model): Edit => edits[m.id] ?? editFromModel(m)
  const patchEdit = (m: Model, patch: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [m.id]: { ...editOf(m), ...patch } }))

  const create = () =>
    run(() => AdminModels.create(draft), { notice: 'Model added.' }).then(() =>
      setDraft(emptyNew(providers[0]?.id ?? '')),
    )

  const save = (m: Model) =>
    run(() => AdminModels.update(m.id, { id: m.id, ...editOf(m) }), { notice: 'Model pricing saved.' })

  const test = async (m: Model) => {
    setNotice('Testing model...')
    try {
      const result = await AdminModels.test(m.id)
      setNotice(
        `Model test ${result.ok ? 'passed' : 'failed'} in ${result.latency_ms || 0}ms (${result.status_code || 'n/a'}).`,
      )
    } catch (err) {
      setNotice('')
      setError(`Model test failed: ${err instanceof Error ? err.message : String(err)}`)
    }
  }

  const remove = (m: Model) => {
    if (!window.confirm(`Delete model ${m.route}?`)) return
    run(() => AdminModels.remove(m.id), { notice: 'Model deleted.' })
  }

  return (
    <div className="space-y-6">
      <AdminPanel
        title="Add model"
        note="Route defaults to provider/model. Fallback and weighted routes reference route ids below. Prices are USD per 1M tokens."
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <FormField label="Provider">
            <NativeSelect value={draft.provider_id} onChange={(e) => setDraft({ ...draft, provider_id: e.target.value })}>
              {providers.map((p) => (
                <option key={p.id} value={p.id}>{p.id} · {p.name}</option>
              ))}
            </NativeSelect>
          </FormField>
          <FormField label="Upstream model id">
            <Input placeholder="e.g. llama3.1:8b or claude-3-5-sonnet" value={draft.model_id} onChange={(e) => setDraft({ ...draft, model_id: e.target.value })} />
          </FormField>
          <FormField label="Route id" help="Public model name clients send. Blank becomes provider/model.">
            <Input placeholder="e.g. chat/default" value={draft.route} onChange={(e) => setDraft({ ...draft, route: e.target.value })} />
          </FormField>
          <FormField label="Display name">
            <Input placeholder="Human-friendly name" value={draft.display_name} onChange={(e) => setDraft({ ...draft, display_name: e.target.value })} />
          </FormField>
          <FormField label="Input cost / 1M tokens">
            <Input type="number" min={0} step={0.0001} value={draft.input_cost_per_million} onChange={(e) => setDraft({ ...draft, input_cost_per_million: Number(e.target.value || 0) })} />
          </FormField>
          <FormField label="Output cost / 1M tokens">
            <Input type="number" min={0} step={0.0001} value={draft.output_cost_per_million} onChange={(e) => setDraft({ ...draft, output_cost_per_million: Number(e.target.value || 0) })} />
          </FormField>
          <FormField label="Context window tokens">
            <Input type="number" min={0} step={1} value={draft.context_window} onChange={(e) => setDraft({ ...draft, context_window: Number.parseInt(e.target.value || '0', 10) })} />
          </FormField>
          <FormField label="Fallback routes" help="Existing route ids to try in order after a failure.">
            <Textarea placeholder={'openai/gpt-4o-mini\nlocal-ollama/gemma'} value={draft.fallback_routes} onChange={(e) => setDraft({ ...draft, fallback_routes: e.target.value })} />
          </FormField>
          <FormField label="Weighted routes" help="Existing route id plus relative traffic weight.">
            <Textarea placeholder={'openai/gpt-4o-mini 80\nlocal-vllm/llama-3.1-8b 20'} value={draft.weighted_routes} onChange={(e) => setDraft({ ...draft, weighted_routes: e.target.value })} />
          </FormField>
          <FormField label="Retries per candidate">
            <Input type="number" min={0} max={5} step={1} value={draft.retry_attempts} onChange={(e) => setDraft({ ...draft, retry_attempts: Number.parseInt(e.target.value || '0', 10) })} />
          </FormField>
          <FormField label="Request timeout ms">
            <Input type="number" min={0} step={1000} value={draft.request_timeout_ms} onChange={(e) => setDraft({ ...draft, request_timeout_ms: Number.parseInt(e.target.value || '0', 10) })} />
          </FormField>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-4">
          <CheckField label="Streaming" checked={draft.supports_streaming} onChange={(e) => setDraft({ ...draft, supports_streaming: e.target.checked })} />
          <CheckField label="Enabled" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} />
          <CheckField label="Health routing" checked={draft.health_routing_enabled} onChange={(e) => setDraft({ ...draft, health_routing_enabled: e.target.checked })} />
          <Button onClick={create}>Add model</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Models and pricing">
        {models.length === 0 ? (
          <EmptyNote>No models yet.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Route</TableHead>
                  <TableHead>Provider</TableHead>
                  <TableHead>Model id</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Input</TableHead>
                  <TableHead>Output</TableHead>
                  <TableHead>Context</TableHead>
                  <TableHead>Fallback routes</TableHead>
                  <TableHead>Weighted routes</TableHead>
                  <TableHead>Retries</TableHead>
                  <TableHead>Timeout ms</TableHead>
                  <TableHead>Health routing</TableHead>
                  <TableHead>Streaming</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {models.map((m) => {
                  const e = editOf(m)
                  return (
                    <TableRow key={m.id}>
                      <TableCell><Input className="min-w-32" value={e.route} onChange={(ev) => patchEdit(m, { route: ev.target.value })} /></TableCell>
                      <TableCell>
                        <NativeSelect value={e.provider_id} onChange={(ev) => patchEdit(m, { provider_id: ev.target.value })}>
                          {providers.map((p) => (
                            <option key={p.id} value={p.id}>{p.id}</option>
                          ))}
                        </NativeSelect>
                      </TableCell>
                      <TableCell><Input className="min-w-32" value={e.model_id} onChange={(ev) => patchEdit(m, { model_id: ev.target.value })} /></TableCell>
                      <TableCell><Input className="min-w-32" value={e.display_name} onChange={(ev) => patchEdit(m, { display_name: ev.target.value })} /></TableCell>
                      <TableCell><Input className="w-24" type="number" min={0} step={0.0001} value={e.input_cost_per_million} onChange={(ev) => patchEdit(m, { input_cost_per_million: Number(ev.target.value || 0) })} /></TableCell>
                      <TableCell><Input className="w-24" type="number" min={0} step={0.0001} value={e.output_cost_per_million} onChange={(ev) => patchEdit(m, { output_cost_per_million: Number(ev.target.value || 0) })} /></TableCell>
                      <TableCell><Input className="w-24" type="number" min={0} step={1} value={e.context_window} onChange={(ev) => patchEdit(m, { context_window: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                      <TableCell><Textarea className="min-w-40" value={e.fallback_routes} onChange={(ev) => patchEdit(m, { fallback_routes: ev.target.value })} /></TableCell>
                      <TableCell><Textarea className="min-w-40" value={e.weighted_routes} onChange={(ev) => patchEdit(m, { weighted_routes: ev.target.value })} /></TableCell>
                      <TableCell><Input className="w-16" type="number" min={0} max={5} step={1} value={e.retry_attempts} onChange={(ev) => patchEdit(m, { retry_attempts: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                      <TableCell><Input className="w-24" type="number" min={0} step={1000} value={e.request_timeout_ms} onChange={(ev) => patchEdit(m, { request_timeout_ms: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.health_routing_enabled} onChange={(ev) => patchEdit(m, { health_routing_enabled: ev.target.checked })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.supports_streaming} onChange={(ev) => patchEdit(m, { supports_streaming: ev.target.checked })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.enabled} onChange={(ev) => patchEdit(m, { enabled: ev.target.checked })} /></TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => test(m)}>Test</Button>
                          <Button size="sm" variant="outline" onClick={() => save(m)}>Save</Button>
                          <Button size="sm" variant="destructive" onClick={() => remove(m)}>Delete</Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </TableScroll>
        )}
      </AdminPanel>
    </div>
  )
}
