import { useState } from 'react'
import { useStore } from '@/store'
import { AdminProviders } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { StatusPill } from '@/components/common'
import { AdminPanel, CheckField, EmptyNote, TableScroll, useAdminAction } from './shared'
import { fmt } from '@/lib/format'
import type { Provider, ProviderType } from '@/types'

const TYPES: { value: ProviderType; label: string }[] = [
  { value: 'openai', label: 'OpenAI-compatible' },
  { value: 'anthropic', label: 'Anthropic-compatible' },
  { value: 'bedrock', label: 'AWS Bedrock' },
]

interface NewProvider {
  id: string
  name: string
  type: ProviderType
  base_url: string
  api_key_env: string
  api_key: string
  aws_region: string
  enabled: boolean
}

const EMPTY_NEW: NewProvider = {
  id: '',
  name: '',
  type: 'openai',
  base_url: '',
  api_key_env: '',
  api_key: '',
  aws_region: '',
  enabled: true,
}

type Edit = {
  name: string
  type: ProviderType
  base_url: string
  api_key_env: string
  api_key: string
  aws_region: string
  enabled: boolean
}

export function ProvidersTab() {
  const providers = useStore((s) => s.providers)
  const run = useAdminAction()

  const [draft, setDraft] = useState<NewProvider>(EMPTY_NEW)
  const [edits, setEdits] = useState<Record<string, Edit>>({})

  const editOf = (p: Provider): Edit =>
    edits[p.id] ?? {
      name: p.name,
      type: p.type,
      base_url: p.base_url,
      api_key_env: (p.api_key_env || '').replace(' (secret set)', ''),
      api_key: '',
      aws_region: p.aws_region,
      enabled: p.enabled,
    }
  const patchEdit = (p: Provider, patch: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [p.id]: { ...editOf(p), ...patch } }))

  const create = () =>
    run(() => AdminProviders.create(draft), { notice: 'Provider added.' }).then(() =>
      setDraft(EMPTY_NEW),
    )

  const save = (p: Provider) =>
    run(() => AdminProviders.update(p.id, { id: p.id, ...editOf(p) }), { notice: 'Provider saved.' })

  const remove = (p: Provider) => {
    if (!window.confirm(`Delete provider ${p.id}? Models under it will also be removed.`)) return
    run(() => AdminProviders.remove(p.id), { notice: 'Provider deleted.' })
  }

  return (
    <div className="space-y-6">
      <AdminPanel
        title="Add provider"
        note="OpenAI-compatible covers Ollama, vLLM, LM Studio, OpenRouter, and LiteLLM. Bedrock uses AWS region and the AWS credential chain."
      >
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Input placeholder="provider id, e.g. local-vllm" value={draft.id} onChange={(e) => setDraft({ ...draft, id: e.target.value })} />
          <Input placeholder="Display name" value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
          <NativeSelect value={draft.type} onChange={(e) => setDraft({ ...draft, type: e.target.value as ProviderType })}>
            {TYPES.map((t) => (
              <option key={t.value} value={t.value}>{t.label}</option>
            ))}
          </NativeSelect>
          <Input placeholder="Base URL, e.g. http://localhost:8000/v1" value={draft.base_url} onChange={(e) => setDraft({ ...draft, base_url: e.target.value })} />
          <Input placeholder="API key env var, e.g. OPENAI_API_KEY" value={draft.api_key_env} onChange={(e) => setDraft({ ...draft, api_key_env: e.target.value })} />
          <Input type="password" placeholder="Direct API key (optional)" value={draft.api_key} onChange={(e) => setDraft({ ...draft, api_key: e.target.value })} />
          <Input placeholder="AWS region for Bedrock, e.g. us-east-1" value={draft.aws_region} onChange={(e) => setDraft({ ...draft, aws_region: e.target.value })} />
          <CheckField label="Enabled" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} />
        </div>
        <div className="mt-3">
          <Button onClick={create}>Add provider</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Providers">
        {providers.length === 0 ? (
          <EmptyNote>No providers yet.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead>Base URL</TableHead>
                  <TableHead>Key env</TableHead>
                  <TableHead>Direct key</TableHead>
                  <TableHead>AWS region</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead>Health</TableHead>
                  <TableHead>Failures</TableHead>
                  <TableHead>Last check</TableHead>
                  <TableHead>Circuit open</TableHead>
                  <TableHead>Last error</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {providers.map((p) => {
                  const e = editOf(p)
                  return (
                    <TableRow key={p.id}>
                      <TableCell className="font-mono">{p.id}</TableCell>
                      <TableCell><Input value={e.name} onChange={(ev) => patchEdit(p, { name: ev.target.value })} /></TableCell>
                      <TableCell>
                        <NativeSelect value={e.type} onChange={(ev) => patchEdit(p, { type: ev.target.value as ProviderType })}>
                          {TYPES.map((t) => (
                            <option key={t.value} value={t.value}>{t.label}</option>
                          ))}
                        </NativeSelect>
                      </TableCell>
                      <TableCell><Input value={e.base_url} onChange={(ev) => patchEdit(p, { base_url: ev.target.value })} /></TableCell>
                      <TableCell><Input value={e.api_key_env} onChange={(ev) => patchEdit(p, { api_key_env: ev.target.value })} /></TableCell>
                      <TableCell><Input type="password" placeholder="leave blank to keep" value={e.api_key} onChange={(ev) => patchEdit(p, { api_key: ev.target.value })} /></TableCell>
                      <TableCell><Input value={e.aws_region} onChange={(ev) => patchEdit(p, { aws_region: ev.target.value })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.enabled} onChange={(ev) => patchEdit(p, { enabled: ev.target.checked })} /></TableCell>
                      <TableCell><StatusPill status={p.health_status || 'unknown'} /></TableCell>
                      <TableCell>{Number(p.consecutive_failures || 0)}</TableCell>
                      <TableCell>{fmt(p.last_health_check_at)}</TableCell>
                      <TableCell>{fmt(p.circuit_open_until)}</TableCell>
                      <TableCell className="max-w-48 whitespace-normal break-words text-xs">{p.last_error || ''}</TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => save(p)}>Save</Button>
                          <Button size="sm" variant="destructive" onClick={() => remove(p)}>Delete</Button>
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
