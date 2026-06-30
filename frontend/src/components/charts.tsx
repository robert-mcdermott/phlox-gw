// Recharts-based, theme-aware chart components.
//
// Colors are driven by the shadcn CSS variables (`var(--primary)` etc.), so
// charts follow the active [data-theme] palette set in src/index.css.
//
// Recharts reads numeric series; pass already-shaped data plus a value key and
// an optional formatter for tooltip/axis display.

import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

const AXIS_COLOR = 'var(--muted-foreground)'
const GRID_COLOR = 'color-mix(in oklab, var(--border) 70%, transparent)'

interface TooltipPayloadItem {
  value?: number | string
  name?: string
  dataKey?: string | number
}

function ChartTooltip({
  active,
  payload,
  label,
  formatter,
  valueLabel,
}: {
  active?: boolean
  payload?: TooltipPayloadItem[]
  label?: string | number
  formatter?: (v: number) => string
  valueLabel?: string
}) {
  if (!active || !payload || payload.length === 0) return null
  const raw = Number(payload[0]?.value ?? 0)
  const display = formatter ? formatter(raw) : String(payload[0]?.value ?? '')
  return (
    <div className="rounded-md border bg-popover px-3 py-2 text-xs text-popover-foreground shadow-md">
      <div className="font-medium">{label}</div>
      <div className="text-muted-foreground">
        {valueLabel ? `${valueLabel}: ` : ''}
        {display}
      </div>
    </div>
  )
}

export type ChartDatum = Record<string, string | number>

export function BarChartCard<T>({
  title,
  data,
  dataKey,
  xKey = 'date',
  formatter,
  height = 200,
}: {
  title: string
  data: T[]
  dataKey: keyof T & string
  xKey?: string
  formatter?: (v: number) => string
  height?: number
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-2 text-sm font-medium text-card-foreground">{title}</div>
      {data.length === 0 ? (
        <p className="text-sm text-muted-foreground">No data.</p>
      ) : (
        <ResponsiveContainer width="100%" height={height}>
          <BarChart data={data as ChartDatum[]} margin={{ top: 4, right: 4, bottom: 0, left: -16 }}>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID_COLOR} vertical={false} />
            <XAxis dataKey={xKey} tick={{ fontSize: 10, fill: AXIS_COLOR }} tickLine={false} axisLine={{ stroke: GRID_COLOR }} minTickGap={24} />
            <YAxis tick={{ fontSize: 10, fill: AXIS_COLOR }} tickLine={false} axisLine={false} width={48} tickFormatter={formatter} />
            <Tooltip
              cursor={{ fill: 'color-mix(in oklab, var(--primary) 12%, transparent)' }}
              content={<ChartTooltip formatter={formatter} valueLabel={title} />}
            />
            <Bar dataKey={dataKey as string} fill="var(--primary)" radius={[3, 3, 0, 0]} maxBarSize={28} />
          </BarChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}

export function AreaChartCard<T>({
  title,
  data,
  dataKey,
  xKey = 'date',
  formatter,
  height = 220,
}: {
  title: string
  data: T[]
  dataKey: keyof T & string
  xKey?: string
  formatter?: (v: number) => string
  height?: number
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="mb-2 text-sm font-medium text-card-foreground">{title}</div>
      {data.length === 0 ? (
        <p className="text-sm text-muted-foreground">No data.</p>
      ) : (
        <ResponsiveContainer width="100%" height={height}>
          <AreaChart data={data as ChartDatum[]} margin={{ top: 4, right: 4, bottom: 0, left: -16 }}>
            <defs>
              <linearGradient id={`fill-${dataKey as string}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="var(--primary)" stopOpacity={0.35} />
                <stop offset="100%" stopColor="var(--primary)" stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID_COLOR} vertical={false} />
            <XAxis dataKey={xKey} tick={{ fontSize: 10, fill: AXIS_COLOR }} tickLine={false} axisLine={{ stroke: GRID_COLOR }} minTickGap={24} />
            <YAxis tick={{ fontSize: 10, fill: AXIS_COLOR }} tickLine={false} axisLine={false} width={48} tickFormatter={formatter} />
            <Tooltip content={<ChartTooltip formatter={formatter} valueLabel={title} />} />
            <Area type="monotone" dataKey={dataKey as string} stroke="var(--primary)" strokeWidth={2} fill={`url(#fill-${dataKey as string})`} />
          </AreaChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}
