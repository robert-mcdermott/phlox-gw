import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'

export function OnOffPill({ on }: { on: boolean }) {
  return <Badge variant={on ? 'success' : 'destructive'}>{on ? 'on' : 'off'}</Badge>
}

export function StatusPill({ status }: { status: string }) {
  const n = String(status || 'unknown').toLowerCase()
  const variant = n === 'healthy' ? 'success' : n === 'unknown' ? 'secondary' : 'destructive'
  return <Badge variant={variant}>{n}</Badge>
}

export function StatusCodePill({ code, errorText }: { code: number; errorText?: string }) {
  const c = Number(code || 0)
  const hasError = c >= 400 || Boolean(errorText)
  const variant = hasError ? 'destructive' : c >= 200 && c < 400 ? 'success' : 'secondary'
  return <Badge variant={variant}>{c || 'n/a'}</Badge>
}

export function StatCard({
  label,
  value,
  sub,
}: {
  label: string
  value: React.ReactNode
  sub?: string
}) {
  return (
    <Card>
      <CardContent className="space-y-1">
        <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {label}
        </div>
        <div className="text-2xl font-semibold tracking-tight">{value}</div>
        {sub ? <div className="text-xs text-muted-foreground">{sub}</div> : null}
      </CardContent>
    </Card>
  )
}
