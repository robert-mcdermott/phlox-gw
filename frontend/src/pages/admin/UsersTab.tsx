import { useState } from 'react'
import { useStore } from '@/store'
import { AdminUsers } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { NativeSelect } from '@/components/ui/native-select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, CheckField, EmptyNote, TableScroll, useAdminAction } from './shared'
import type { Role, User } from '@/types'

interface NewUser {
  username: string
  password: string
  email: string
  display_name: string
  department: string
  role: Role
}

const EMPTY_NEW: NewUser = {
  username: '',
  password: '',
  email: '',
  display_name: '',
  department: '',
  role: 'user',
}

type Edit = { email: string; display_name: string; department: string; role: Role; is_active: boolean }

export function UsersTab() {
  const users = useStore((s) => s.users)
  const setError = useStore((s) => s.setError)
  const run = useAdminAction()

  const [draft, setDraft] = useState<NewUser>(EMPTY_NEW)
  const [edits, setEdits] = useState<Record<string, Edit>>({})
  const [passwords, setPasswords] = useState<Record<string, string>>({})

  const editOf = (u: User): Edit =>
    edits[u.id] ?? {
      email: u.email,
      display_name: u.display_name,
      department: u.department,
      role: u.role,
      is_active: u.is_active,
    }
  const patchEdit = (u: User, p: Partial<Edit>) =>
    setEdits((prev) => ({ ...prev, [u.id]: { ...editOf(u), ...p } }))

  const create = () =>
    run(() => AdminUsers.create(draft), { notice: 'User created.' }).then(() => setDraft(EMPTY_NEW))

  const save = (u: User) => run(() => AdminUsers.update(u.id, editOf(u)), { notice: 'User saved.' })

  const reset = (u: User) => {
    const password = (passwords[u.id] || '').trim()
    if (!password) {
      setError('Enter a new password before resetting.')
      return
    }
    run(() => AdminUsers.resetPassword(u.id, password), { notice: 'Password reset.' }).then(() =>
      setPasswords((p) => ({ ...p, [u.id]: '' })),
    )
  }

  const remove = (u: User) => {
    if (
      !window.confirm(
        `Delete user ${u.username}? API keys will be revoked and usage ledger rows will remain for chargeback.`,
      )
    )
      return
    run(() => AdminUsers.remove(u.id), { notice: 'User deleted.' })
  }

  return (
    <div className="space-y-6">
      <AdminPanel title="Add user" note="Local users can mint their own API keys after signing in.">
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <Input placeholder="Username" value={draft.username} onChange={(e) => setDraft({ ...draft, username: e.target.value })} />
          <Input type="password" placeholder="Temporary password" value={draft.password} onChange={(e) => setDraft({ ...draft, password: e.target.value })} />
          <Input placeholder="Email" value={draft.email} onChange={(e) => setDraft({ ...draft, email: e.target.value })} />
          <Input placeholder="Display name" value={draft.display_name} onChange={(e) => setDraft({ ...draft, display_name: e.target.value })} />
          <Input placeholder="Department" value={draft.department} onChange={(e) => setDraft({ ...draft, department: e.target.value })} />
          <NativeSelect value={draft.role} onChange={(e) => setDraft({ ...draft, role: e.target.value as Role })}>
            <option value="user">User</option>
            <option value="admin">Admin</option>
          </NativeSelect>
        </div>
        <div className="mt-3">
          <Button onClick={create}>Create user</Button>
        </div>
      </AdminPanel>

      <AdminPanel title="Users">
        {users.length === 0 ? (
          <EmptyNote>No users yet.</EmptyNote>
        ) : (
          <TableScroll>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Username</TableHead>
                  <TableHead>Email</TableHead>
                  <TableHead>Display</TableHead>
                  <TableHead>Department</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Active</TableHead>
                  <TableHead>User budget id</TableHead>
                  <TableHead>Reset password</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => {
                  const e = editOf(u)
                  return (
                    <TableRow key={u.id}>
                      <TableCell className="font-mono">{u.username}</TableCell>
                      <TableCell><Input value={e.email} onChange={(ev) => patchEdit(u, { email: ev.target.value })} /></TableCell>
                      <TableCell><Input value={e.display_name} onChange={(ev) => patchEdit(u, { display_name: ev.target.value })} /></TableCell>
                      <TableCell><Input value={e.department} onChange={(ev) => patchEdit(u, { department: ev.target.value })} /></TableCell>
                      <TableCell>
                        <NativeSelect value={e.role} onChange={(ev) => patchEdit(u, { role: ev.target.value as Role })}>
                          <option value="user">User</option>
                          <option value="admin">Admin</option>
                        </NativeSelect>
                      </TableCell>
                      <TableCell>
                        <CheckField label="" checked={e.is_active} onChange={(ev) => patchEdit(u, { is_active: ev.target.checked })} />
                      </TableCell>
                      <TableCell className="font-mono text-xs">{u.id}</TableCell>
                      <TableCell>
                        <Input
                          type="password"
                          placeholder="new password"
                          value={passwords[u.id] || ''}
                          onChange={(ev) => setPasswords((p) => ({ ...p, [u.id]: ev.target.value }))}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="outline" onClick={() => save(u)}>Save</Button>
                          <Button size="sm" variant="outline" onClick={() => reset(u)}>Reset</Button>
                          <Button size="sm" variant="destructive" onClick={() => remove(u)}>Delete</Button>
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
