import { useState } from 'react'
import { useStore } from '@/store'
import { AdminBudgets } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, CheckField, EmptyNote, ProgressBar, TableScroll, useAdminAction } from './shared'
import { BarChart } from '@/components/charts-lazy'
import { money, percent } from '@/lib/format'
import type { Budget, BudgetBurnDownItem } from '@/types'

type BudgetScope = 'department' | 'user'

function budgetValueOptions(users: { id: string; department: string }[]): string[] {
  const values = new Set<string>()
  for (const u of users) {
    if (u.department) values.add(u.department)
    values.add(u.id)
  }
  return [...values]
}

function StatePill({ item }: { item: BudgetBurnDownItem }) {
  if (item.blocked) return <Badge variant="destructive">blocked</Badge>
  if (item.warning) return <Badge variant="warning">warning</Badge>
  return <Badge variant="success">ok</Badge>
}

type Edit = { scope_type: BudgetScope; scope_value: string; limit_usd: number; warn_pct: number; is_active: boolean }

export function BudgetsTab() {
  const budgets = useStore((s) => s.budgets)
  const burndown = useStore((s) => s.budgetBurnDown)
  const users = useStore((s) => s.users)
  const run = useAdminAction()

  const [scopeType, setScopeType] = useState<BudgetScope>('department')
  const [scopeValue, setScopeValue] = useState('')
  const [limit, setLimit] = useState('')
  const [warn, setWarn] = useState('90')
  const [edits, setEdits] = useState<Record<string, Edit>>({})

  const valueOptions = budgetValueOptions(users)

  const editOf = (b: Budget): Edit =>
    edits[b.id] ?? {
      scope_type: b.scope_type,
      scope_value: b.scope_value,
      limit_usd: b.limit_usd,
      warn_pct: b.warn_pct,
      is_active: b.is_active,
    }
  const patchEdit = (b: Budget, p: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [b.id]: { ...editOf(b), ...p } }))

  const create = () =>
    run(
      () =>
        AdminBudgets.create({
          scope_type: scopeType,
          scope_value: scopeValue.trim(),
          limit_usd: Number(limit || 0),
          warn_pct: Number(warn || 90),
        }),
      { notice: 'Budget created.' },
    ).then(() => {
      setScopeValue('')
      setLimit('')
      setWarn('90')
    })

  const save = (b: Budget) => run(() => AdminBudgets.update(b.id, editOf(b)), { notice: 'Budget saved.' })
  const remove = (b: Budget) => run(() => AdminBudgets.remove(b.id), { notice: 'Budget deleted.' })

  return (
    <div className="space-y-6">
      <AdminPanel title="Add budget" note="User budgets use the user id shown in Users. Department budgets use the department name.">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <NativeSelect value={scopeType} onChange={(e) => setScopeType(e.target.value as BudgetScope)}>
            <option value="department">Department</option>
            <option value="user">User</option>
          </NativeSelect>
          <Input placeholder="Department name or user id" list="budget-values" value={scopeValue} onChange={(e) => setScopeValue(e.target.value)} />
          <Input type="number" min={0} step={0.01} placeholder="Monthly limit USD" value={limit} onChange={(e) => setLimit(e.target.value)} />
          <Input type="number" min={1} max={100} step={1} placeholder="Warn %" value={warn} onChange={(e) => setWarn(e.target.value)} />
        </div>
        <datalist id="budget-values">
          {valueOptions.map((v) => (
            <option key={v} value={v} />
          ))}
        </datalist>
        <div className="mt-3">
          <Button onClick={create}>Create budget</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Budget burn-down" note="Current month spend, remaining budget, and projected month-end run rate.">
        {burndown.length === 0 ? (
          <EmptyNote>No budgets yet.</EmptyNote>
        ) : (
          <div className="space-y-5">
            <BarChart
              title="Spend by scope"
              data={burndown.map((item) => ({
                scope: `${item.budget?.scope_type ?? ''}:${item.budget?.scope_value ?? ''}`,
                spend_usd: Number(item.spend_usd || 0),
              }))}
              dataKey="spend_usd"
              xKey="scope"
              formatter={money}
            />
            <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Scope</TableHead>
                  <TableHead>Spend</TableHead>
                  <TableHead>Progress</TableHead>
                  <TableHead>Remaining</TableHead>
                  <TableHead>Projected month-end</TableHead>
                  <TableHead>Daily avg</TableHead>
                  <TableHead>Days left</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {burndown.map((item, i) => {
                  const b = item.budget
                  return (
                    <TableRow key={b?.id ?? i}>
                      <TableCell><span className="font-mono">{b?.scope_type}</span> {b?.scope_value}</TableCell>
                      <TableCell>{money(item.spend_usd)} / {money(b?.limit_usd)}</TableCell>
                      <TableCell><ProgressBar ratio={item.ratio} /></TableCell>
                      <TableCell>{money(item.remaining_usd)}</TableCell>
                      <TableCell>{money(item.projected_month_end_usd)} <span className="text-muted-foreground">({percent(item.projected_ratio)})</span></TableCell>
                      <TableCell>{money(item.daily_average_usd)}</TableCell>
                      <TableCell>{Number(item.days_remaining || 0)}</TableCell>
                      <TableCell><StatePill item={item} /></TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </TableScroll>
          </div>
        )}
      </AdminPanel>

      <AdminPanel title="Budgets">
        {budgets.length === 0 ? (
          <EmptyNote>No records yet.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Scope</TableHead>
                  <TableHead>Scope value</TableHead>
                  <TableHead>Limit</TableHead>
                  <TableHead>Warn</TableHead>
                  <TableHead>Active</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {budgets.map((b) => {
                  const e = editOf(b)
                  return (
                    <TableRow key={b.id}>
                      <TableCell>
                        <NativeSelect value={e.scope_type} onChange={(ev) => patchEdit(b, { scope_type: ev.target.value as BudgetScope })}>
                          <option value="department">Department</option>
                          <option value="user">User</option>
                        </NativeSelect>
                      </TableCell>
                      <TableCell><Input list="budget-values" value={e.scope_value} onChange={(ev) => patchEdit(b, { scope_value: ev.target.value })} /></TableCell>
                      <TableCell><Input type="number" min={0} step={0.01} value={e.limit_usd} onChange={(ev) => patchEdit(b, { limit_usd: Number(ev.target.value || 0) })} /></TableCell>
                      <TableCell><Input type="number" min={1} max={100} step={1} value={e.warn_pct} onChange={(ev) => patchEdit(b, { warn_pct: Number(ev.target.value || 0) })} /></TableCell>
                      <TableCell><CheckField label="" checked={e.is_active} onChange={(ev) => patchEdit(b, { is_active: ev.target.checked })} /></TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => save(b)}>Save</Button>
                          <Button size="sm" variant="destructive" onClick={() => remove(b)}>Delete</Button>
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
