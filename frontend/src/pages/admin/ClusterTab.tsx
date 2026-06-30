import { useStore } from '@/store'
import { Badge } from '@/components/ui/badge'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { StatusPill } from '@/components/common'
import { AdminPanel, EmptyNote, MetricStrip, MiniMetric, TableScroll } from './shared'
import { compact, fmt } from '@/lib/format'

export function ClusterTab() {
  const status = useStore((s) => s.clusterStatus)
  const nodes = status?.nodes ?? []

  return (
    <AdminPanel
      title="Cluster status"
      note="Cluster mode is explicit. SQLite remains the default single-instance deployment."
    >
      <div className="space-y-5">
        <MetricStrip>
          <MiniMetric label="Mode" value={status?.deployment_mode || 'unknown'} />
          <MiniMetric label="Cluster" value={status?.cluster_enabled ? 'enabled' : 'disabled'} />
          <MiniMetric label="Status" value={status?.status || 'unknown'} />
          <MiniMetric label="Active nodes" value={status?.active_node_count || 0} />
          <MiniMetric label="Stale nodes" value={status?.stale_node_count || 0} />
          <MiniMetric label="Database" value={status?.database_driver || 'unknown'} />
        </MetricStrip>

        <div className="grid gap-3 sm:grid-cols-2">
          <div className="rounded-lg border bg-card px-4 py-3">
            <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Current node</div>
            <strong className="font-mono">{status?.instance_id || ''}</strong>
            <div className="text-sm text-muted-foreground">
              {status?.hostname || ''} {status?.addr ? `· ${status.addr}` : ''}
            </div>
            <div className="text-sm text-muted-foreground">Started {fmt(status?.started_at)}</div>
            <div className="text-sm text-muted-foreground">Last heartbeat {fmt(status?.last_heartbeat_at)}</div>
          </div>
          <div className="rounded-lg border bg-card px-4 py-3">
            <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Database target</div>
            <strong className="font-mono break-all">{status?.database_target || ''}</strong>
            <div className="text-sm text-muted-foreground">
              Heartbeat every {compact(status?.heartbeat_interval_seconds || 0)}s; stale after{' '}
              {compact(status?.node_stale_after_seconds || 0)}s
            </div>
            <div className="text-sm text-muted-foreground">
              Signing key: {status?.signing_key_shared ? 'shared file configured' : 'local/default path'}
            </div>
          </div>
        </div>

        {(status?.notes ?? []).length ? (
          <div className="rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
            {status!.notes.map((note, i) => (
              <p key={i}>{note}</p>
            ))}
          </div>
        ) : null}

        <TableScroll>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Node</TableHead>
                <TableHead>Host</TableHead>
                <TableHead>Address</TableHead>
                <TableHead>Mode</TableHead>
                <TableHead>DB</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Started</TableHead>
                <TableHead>Last seen</TableHead>
                <TableHead>Age</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {nodes.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10}>
                    <EmptyNote>No registered nodes.</EmptyNote>
                  </TableCell>
                </TableRow>
              ) : (
                nodes.map((node) => (
                  <TableRow key={node.instance_id}>
                    <TableCell>
                      <span className="font-mono">{node.instance_id}</span>{' '}
                      {node.current ? <Badge variant="success">current</Badge> : null}
                    </TableCell>
                    <TableCell>{node.hostname || ''}</TableCell>
                    <TableCell className="font-mono">{node.addr || ''}</TableCell>
                    <TableCell>{node.deployment_mode || ''}</TableCell>
                    <TableCell>{node.db_driver || ''}</TableCell>
                    <TableCell>{node.version || ''}</TableCell>
                    <TableCell>
                      {node.stale ? <Badge variant="destructive">stale</Badge> : <StatusPill status={node.status || 'unknown'} />}
                    </TableCell>
                    <TableCell>{fmt(node.started_at)}</TableCell>
                    <TableCell>{fmt(node.last_seen_at)}</TableCell>
                    <TableCell>{compact(node.age_seconds || 0)}s</TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableScroll>
      </div>
    </AdminPanel>
  )
}
