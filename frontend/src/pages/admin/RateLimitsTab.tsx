import { useState } from 'react'
import { useStore } from '@/store'
import { AdminRateLimits } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, CheckField, EmptyNote, TableScroll, useAdminAction } from './shared'
import type { RateLimit, ScopeType } from '@/types'

function rateLimitValueOptions(
  users: { id: string; department: string }[],
  providers: { id: string }[],
  models: { route: string }[],
): string[] {
  const values = new Set<string>()
  for (const u of users) {
    values.add(u.id)
    if (u.department) values.add(u.department)
  }
  for (const p of providers) values.add(p.id)
  for (const m of models) values.add(m.route)
  return [...values]
}

const SCOPES: { value: ScopeType; label: string }[] = [
  { value: 'user', label: 'User' },
  { value: 'department', label: 'Department' },
  { value: 'provider', label: 'Provider' },
  { value: 'model', label: 'Model' },
]

type Edit = { scope_type: ScopeType; scope_value: string; rpm_limit: number; tpm_limit: number; is_active: boolean }

export function RateLimitsTab() {
  const rateLimits = useStore((s) => s.rateLimits)
  const users = useStore((s) => s.users)
  const providers = useStore((s) => s.providers)
  const adminModels = useStore((s) => s.adminModels)
  const run = useAdminAction()

  const [scopeType, setScopeType] = useState<ScopeType>('user')
  const [scopeValue, setScopeValue] = useState('')
  const [rpm, setRpm] = useState('0')
  const [tpm, setTpm] = useState('0')
  const [edits, setEdits] = useState<Record<string, Edit>>({})

  const valueOptions = rateLimitValueOptions(users, providers, adminModels)

  const editOf = (rl: RateLimit): Edit =>
    edits[rl.id] ?? {
      scope_type: rl.scope_type,
      scope_value: rl.scope_value,
      rpm_limit: rl.rpm_limit,
      tpm_limit: rl.tpm_limit,
      is_active: rl.is_active,
    }
  const patchEdit = (rl: RateLimit, p: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [rl.id]: { ...editOf(rl), ...p } }))

  const create = () =>
    run(
      () =>
        AdminRateLimits.create({
          scope_type: scopeType,
          scope_value: scopeValue.trim(),
          rpm_limit: Number.parseInt(rpm || '0', 10),
          tpm_limit: Number.parseInt(tpm || '0', 10),
        }),
      { notice: 'Rate limit created.' },
    ).then(() => {
      setScopeValue('')
      setRpm('0')
      setTpm('0')
    })

  const save = (rl: RateLimit) =>
    run(() => AdminRateLimits.update(rl.id, editOf(rl)), { notice: 'Rate limit saved.' })
  const remove = (rl: RateLimit) =>
    run(() => AdminRateLimits.remove(rl.id), { notice: 'Rate limit deleted.' })

  return (
    <div className="space-y-6">
      <AdminPanel title="Add rate limit" note="Scope values use user id, department name, provider id, or model route. Limits of 0 are unlimited.">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <NativeSelect value={scopeType} onChange={(e) => setScopeType(e.target.value as ScopeType)}>
            {SCOPES.map((s) => (
              <option key={s.value} value={s.value}>{s.label}</option>
            ))}
          </NativeSelect>
          <Input placeholder="User id, department, provider, or model" list="rate-limit-values" value={scopeValue} onChange={(e) => setScopeValue(e.target.value)} />
          <Input type="number" min={0} step={1} placeholder="RPM limit" aria-label="Requests per minute limit" value={rpm} onChange={(e) => setRpm(e.target.value)} />
          <Input type="number" min={0} step={1} placeholder="TPM limit" aria-label="Tokens per minute limit" value={tpm} onChange={(e) => setTpm(e.target.value)} />
        </div>
        <datalist id="rate-limit-values">
          {valueOptions.map((v) => (
            <option key={v} value={v} />
          ))}
        </datalist>
        <div className="mt-3">
          <Button onClick={create}>Create limit</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Rate limits">
        {rateLimits.length === 0 ? (
          <EmptyNote>No records yet.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Scope</TableHead>
                  <TableHead>Scope value</TableHead>
                  <TableHead>RPM</TableHead>
                  <TableHead>TPM</TableHead>
                  <TableHead>Active</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rateLimits.map((rl) => {
                  const e = editOf(rl)
                  return (
                    <TableRow key={rl.id}>
                      <TableCell>
                        <NativeSelect value={e.scope_type} onChange={(ev) => patchEdit(rl, { scope_type: ev.target.value as ScopeType })}>
                          {SCOPES.map((s) => (
                            <option key={s.value} value={s.value}>{s.label}</option>
                          ))}
                        </NativeSelect>
                      </TableCell>
                      <TableCell><Input list="rate-limit-values" value={e.scope_value} onChange={(ev) => patchEdit(rl, { scope_value: ev.target.value })} /></TableCell>
                      <TableCell><Input type="number" min={0} step={1} value={e.rpm_limit} onChange={(ev) => patchEdit(rl, { rpm_limit: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                      <TableCell><Input type="number" min={0} step={1} value={e.tpm_limit} onChange={(ev) => patchEdit(rl, { tpm_limit: Number.parseInt(ev.target.value || '0', 10) })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.is_active} onChange={(ev) => patchEdit(rl, { is_active: ev.target.checked })} /></TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => save(rl)}>Save</Button>
                          <Button size="sm" variant="destructive" onClick={() => remove(rl)}>Delete</Button>
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
