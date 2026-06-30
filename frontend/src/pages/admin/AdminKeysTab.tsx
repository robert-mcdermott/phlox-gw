import { useState } from 'react'
import { useStore } from '@/store'
import { AdminKeys } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, CheckField, EmptyNote, TableScroll, useAdminAction } from './shared'
import { money, fmt } from '@/lib/format'
import type { AdminApiKey } from '@/types'

type Edit = {
  name: string
  is_active: boolean
  budget_usd: number
  rpm_limit: number
  tpm_limit: number
  model_allowlist: string
  expires_at: string
}

function editFromKey(k: AdminApiKey): Edit {
  return {
    name: k.name,
    is_active: k.is_active,
    budget_usd: k.budget_usd,
    rpm_limit: k.rpm_limit,
    tpm_limit: k.tpm_limit,
    model_allowlist: k.model_allowlist || '',
    expires_at: k.expires_at || '',
  }
}

export function AdminKeysTab() {
  const adminKeys = useStore((s) => s.adminKeys)
  const secret = useStore((s) => s.secret)
  const setSecret = useStore((s) => s.setSecret)
  const run = useAdminAction()

  const [edits, setEdits] = useState<Record<string, Edit>>({})
  const editOf = (k: AdminApiKey): Edit => edits[k.id] ?? editFromKey(k)
  const patchEdit = (k: AdminApiKey, patch: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [k.id]: { ...editOf(k), ...patch } }))

  const save = (k: AdminApiKey) =>
    run(() => AdminKeys.update(k.id, editOf(k)), { notice: 'API key controls saved.' })

  const rotate = (k: AdminApiKey) => {
    if (!window.confirm(`Rotate API key ${k.id}? The old secret will stop working immediately.`)) return
    run(
      async () => {
        const resp = await AdminKeys.rotate(k.id)
        setSecret(resp.key)
      },
      { notice: 'API key rotated.' },
    )
  }

  const revoke = (k: AdminApiKey) => {
    if (!window.confirm(`Revoke API key ${k.id}?`)) return
    run(() => AdminKeys.revoke(k.id), { notice: 'API key revoked.' })
  }

  return (
    <AdminPanel title="API key governance" note="Empty allowlist means all enabled models. Limits of 0 are unlimited.">
      {secret ? (
        <div className="mb-4 rounded-md border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 font-mono text-sm break-all text-emerald-700 dark:text-emerald-400">
          New rotated key: {secret}
        </div>
      ) : null}
      {adminKeys.length === 0 ? (
        <EmptyNote>No API keys yet.</EmptyNote>
      ) : (
        <TableScroll>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Owner</TableHead>
                <TableHead>Department</TableHead>
                <TableHead>Prefix</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Active</TableHead>
                <TableHead>Monthly budget</TableHead>
                <TableHead>RPM</TableHead>
                <TableHead>TPM</TableHead>
                <TableHead>Model allowlist</TableHead>
                <TableHead>Month spend</TableHead>
                <TableHead>Expires</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead>Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {adminKeys.map((k) => {
                const e = editOf(k)
                return (
                  <TableRow key={k.id}>
                    <TableCell className="font-mono">{k.username || k.user_id}</TableCell>
                    <TableCell>{k.department || ''}</TableCell>
                    <TableCell className="font-mono">{k.prefix}</TableCell>
                    <TableCell><Input value={e.name} onChange={(ev) => patchEdit(k, { name: ev.target.value })} /></TableCell>
                    <TableCell><CheckField label="" checked={e.is_active} onChange={(ev) => patchEdit(k, { is_active: ev.target.checked })} /></TableCell>
                    <TableCell><Input className="w-24" type="number" min={0} step={0.01} value={e.budget_usd} onChange={(ev) => patchEdit(k, { budget_usd: Number(ev.target.value || 0) })} /></TableCell>
                    <TableCell><Input className="w-20" type="number" min={0} step={1} value={e.rpm_limit} onChange={(ev) => patchEdit(k, { rpm_limit: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                    <TableCell><Input className="w-20" type="number" min={0} step={1} value={e.tpm_limit} onChange={(ev) => patchEdit(k, { tpm_limit: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                    <TableCell><Textarea className="min-w-40" rows={2} placeholder="provider/model, one per line" value={e.model_allowlist} onChange={(ev) => patchEdit(k, { model_allowlist: ev.target.value })} /></TableCell>
                    <TableCell>{money(k.monthly_spend_usd || 0)}</TableCell>
                    <TableCell><Input className="min-w-40" placeholder="RFC3339 or blank" value={e.expires_at} onChange={(ev) => patchEdit(k, { expires_at: ev.target.value })} /></TableCell>
                    <TableCell>{fmt(k.last_used_at)}</TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <Button size="sm" variant="outline" onClick={() => save(k)}>Save</Button>
                        {k.is_active ? <Button size="sm" variant="outline" onClick={() => rotate(k)}>Rotate</Button> : null}
                        <Button size="sm" variant="destructive" onClick={() => revoke(k)}>Revoke</Button>
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
  )
}
