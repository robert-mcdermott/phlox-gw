import { useStore } from '@/store'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { AdminPanel, EmptyNote, TableScroll } from './shared'
import { fmt } from '@/lib/format'

function auditDetails(details: string): string {
  if (!details) return ''
  try {
    const parsed = JSON.parse(details) as Record<string, unknown>
    return Object.entries(parsed)
      .map(([key, value]) => `${key}: ${value === null ? '' : String(value)}`)
      .join(', ')
  } catch {
    return details
  }
}

export function AuditLogsTab() {
  const auditLogs = useStore((s) => s.auditLogs)

  return (
    <AdminPanel title="Audit log" note="Recent local auth, admin, and API key lifecycle events.">
      {auditLogs.length === 0 ? (
        <EmptyNote>No audit events yet.</EmptyNote>
      ) : (
        <TableScroll>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>Actor</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Details</TableHead>
                <TableHead>IP</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {auditLogs.map((item) => (
                <TableRow key={item.id}>
                  <TableCell>{fmt(item.created_at)}</TableCell>
                  <TableCell className="font-mono">{item.actor_username || item.actor_user_id || ''}</TableCell>
                  <TableCell className="font-mono">{item.action}</TableCell>
                  <TableCell>
                    <span className="font-mono">{item.target_type}</span> {item.target_display || item.target_id || ''}
                  </TableCell>
                  <TableCell className="max-w-72 whitespace-normal break-words text-xs">{auditDetails(item.details)}</TableCell>
                  <TableCell className="font-mono">{item.ip_address || ''}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableScroll>
      )}
    </AdminPanel>
  )
}
