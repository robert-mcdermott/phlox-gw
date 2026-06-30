// Shared building blocks for the admin section, ported from the panel /
// metric-strip / progress-bar markup in frontend/src/static/app.js.

import * as React from 'react'
import { Card } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { useStore } from '@/store'
import { percent } from '@/lib/format'

export function AdminPanel({
  title,
  note,
  children,
}: {
  title: string
  note?: string
  children: React.ReactNode
}) {
  return (
    <Card className="gap-0 p-5 py-5">
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-base font-semibold">{title}</h3>
        {note ? <span className="text-xs text-muted-foreground">{note}</span> : null}
      </div>
      {children}
    </Card>
  )
}

export function MetricStrip({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">{children}</div>
  )
}

export function MiniMetric({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="rounded-lg border bg-card px-3 py-2">
      <div className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      <strong className="text-lg font-semibold tracking-tight">{value}</strong>
    </div>
  )
}

export function ProgressBar({ ratio, label }: { ratio: number; label?: string }) {
  const pct = Math.max(0, Math.min(100, Number(ratio || 0) * 100))
  const danger = pct >= 100
  const warn = pct >= 90
  return (
    <div className="flex items-center gap-2" title={label ?? `${percent(ratio)} used`}>
      <div className="h-2 w-24 overflow-hidden rounded-full bg-muted">
        <div
          className={
            danger
              ? 'h-full bg-destructive'
              : warn
                ? 'h-full bg-amber-500'
                : 'h-full bg-primary'
          }
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-xs text-muted-foreground">{label ?? `${percent(ratio)} used`}</span>
    </div>
  )
}

export function FormField({
  label,
  help,
  className,
  children,
}: {
  label: string
  help?: string
  className?: string
  children: React.ReactNode
}) {
  return (
    <label className={`flex flex-col gap-1 ${className ?? ''}`}>
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
      {help ? <small className="text-xs text-muted-foreground">{help}</small> : null}
    </label>
  )
}

export function CheckField({
  label,
  ...props
}: { label: string } & React.ComponentProps<'input'>) {
  return (
    <Label className="flex cursor-pointer items-center gap-2 text-sm font-normal">
      <input type="checkbox" className="size-4 accent-primary" {...props} />
      {label}
    </Label>
  )
}

export function TableScroll({ children }: { children: React.ReactNode }) {
  return <div className="overflow-x-auto">{children}</div>
}

export function EmptyNote({ children }: { children: React.ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>
}

/**
 * Returns a runner that executes an async admin mutation, surfacing errors via
 * the store, optionally setting a success notice, and refreshing data — the
 * same lifecycle the vanilla handlers used.
 */
export function useAdminAction() {
  const setError = useStore((s) => s.setError)
  const setNotice = useStore((s) => s.setNotice)
  const refresh = useStore((s) => s.refresh)

  return React.useCallback(
    async (fn: () => Promise<unknown>, opts?: { notice?: string; refresh?: boolean }) => {
      try {
        await fn()
        if (opts?.notice) setNotice(opts.notice)
        if (opts?.refresh !== false) await refresh()
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      }
    },
    [setError, setNotice, refresh],
  )
}
