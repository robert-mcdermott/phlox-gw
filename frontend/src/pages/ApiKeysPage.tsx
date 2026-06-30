import { useState } from 'react'
import { useStore } from '@/store'
import { Keys } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { fmt } from '@/lib/format'
import type { ApiKey } from '@/types'

function keyStatus(k: ApiKey) {
  if (!k.is_active) return <Badge variant="destructive">revoked</Badge>
  if (k.expires_at && new Date(k.expires_at).getTime() <= Date.now())
    return <Badge variant="destructive">expired</Badge>
  return <Badge variant="success">active</Badge>
}

export function ApiKeysPage() {
  const keys = useStore((s) => s.keys)
  const secret = useStore((s) => s.secret)
  const setSecret = useStore((s) => s.setSecret)
  const setNotice = useStore((s) => s.setNotice)
  const setError = useStore((s) => s.setError)
  const refresh = useStore((s) => s.refresh)

  const [name, setName] = useState('Development key')
  const [expires, setExpires] = useState('')
  // row-local edits keyed by id
  const [edits, setEdits] = useState<Record<string, { name: string; expires_at: string }>>({})

  const editOf = (k: ApiKey) => edits[k.id] ?? { name: k.name, expires_at: k.expires_at || '' }

  const run = async (fn: () => Promise<unknown>) => {
    try {
      await fn()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const create = () =>
    run(async () => {
      const resp = await Keys.create(name || 'API key', expires.trim())
      setSecret(resp.key)
      await refresh()
    })

  const save = (k: ApiKey) =>
    run(async () => {
      await Keys.update(k.id, editOf(k))
      setNotice('API key saved.')
      await refresh()
    })

  const rotate = (k: ApiKey) =>
    run(async () => {
      if (!window.confirm(`Rotate API key ${k.id}? The old secret will stop working immediately.`)) return
      const resp = await Keys.rotate(k.id)
      setSecret(resp.key)
      setNotice('API key rotated.')
      await refresh()
    })

  const revoke = (k: ApiKey) =>
    run(async () => {
      await Keys.revoke(k.id)
      await refresh()
    })

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Mint API key</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap gap-2">
            <Input className="max-w-xs" placeholder="Key name" value={name} onChange={(e) => setName(e.target.value)} />
            <Input className="max-w-xs" placeholder="Expires RFC3339 (optional)" value={expires} onChange={(e) => setExpires(e.target.value)} />
            <Button onClick={create}>Create</Button>
          </div>
          {secret ? (
            <div className="rounded-md border border-emerald-500/40 bg-emerald-500/10 px-3 py-2 font-mono text-sm break-all text-emerald-700 dark:text-emerald-400">
              New key: {secret}
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Your API keys</CardTitle>
        </CardHeader>
        <CardContent>
          {keys.length === 0 ? (
            <p className="text-sm text-muted-foreground">No API keys yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Prefix</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead>Last used</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {keys.map((k) => {
                  const e = editOf(k)
                  return (
                    <TableRow key={k.id}>
                      <TableCell>
                        <Input
                          value={e.name}
                          disabled={!k.is_active}
                          onChange={(ev) => setEdits((p) => ({ ...p, [k.id]: { ...e, name: ev.target.value } }))}
                        />
                      </TableCell>
                      <TableCell className="font-mono">{k.prefix}</TableCell>
                      <TableCell>{keyStatus(k)}</TableCell>
                      <TableCell>
                        <Input
                          value={e.expires_at}
                          placeholder="RFC3339 or blank"
                          disabled={!k.is_active}
                          onChange={(ev) => setEdits((p) => ({ ...p, [k.id]: { ...e, expires_at: ev.target.value } }))}
                        />
                      </TableCell>
                      <TableCell>{fmt(k.last_used_at)}</TableCell>
                      <TableCell>
                        {k.is_active ? (
                          <div className="flex gap-1">
                            <Button size="sm" variant="outline" onClick={() => save(k)}>Save</Button>
                            <Button size="sm" variant="outline" onClick={() => rotate(k)}>Rotate</Button>
                            <Button size="sm" variant="destructive" onClick={() => revoke(k)}>Revoke</Button>
                          </div>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
