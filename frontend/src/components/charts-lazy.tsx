// Lazy wrappers around the Recharts-backed chart cards. Recharts (with its
// D3 internals) is large, so we code-split it: the chunk only loads when a
// chart first renders, keeping the initial bundle lean.

import { lazy, Suspense } from 'react'
import type { ComponentProps } from 'react'
import type { BarChartCard, AreaChartCard } from '@/components/charts'

const LazyBar = lazy(() =>
  import('@/components/charts').then((m) => ({ default: m.BarChartCard })),
)
const LazyArea = lazy(() =>
  import('@/components/charts').then((m) => ({ default: m.AreaChartCard })),
)

function ChartFallback({ title, height = 200 }: { title: string; height?: number }) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-2 text-sm font-medium text-card-foreground">{title}</div>
      <div
        className="flex items-center justify-center rounded-md bg-muted/40 text-xs text-muted-foreground"
        style={{ height }}
      >
        Loading chart…
      </div>
    </div>
  )
}

export function BarChart<T>(props: ComponentProps<typeof BarChartCard<T>>) {
  return (
    <Suspense fallback={<ChartFallback title={props.title} height={props.height} />}>
      <LazyBar {...(props as ComponentProps<typeof BarChartCard>)} />
    </Suspense>
  )
}

export function AreaChart<T>(props: ComponentProps<typeof AreaChartCard<T>>) {
  return (
    <Suspense fallback={<ChartFallback title={props.title} height={props.height} />}>
      <LazyArea {...(props as ComponentProps<typeof AreaChartCard>)} />
    </Suspense>
  )
}
