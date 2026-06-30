// Value formatters ported from frontend/src/static/app.js.

export function money(v: number | null | undefined): string {
  return `$${Number(v || 0).toFixed(4)}`
}

export function compact(v: number | null | undefined): string {
  return new Intl.NumberFormat(undefined, {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(Number(v || 0))
}

export function percent(v: number | null | undefined): string {
  return `${(Number(v || 0) * 100).toFixed(1)}%`
}

export function fmt(v: string | null | undefined): string {
  return v ? new Date(v).toLocaleString() : ''
}

export function weightedAverage<T>(
  rows: T[],
  valueField: keyof T,
  weightField: keyof T,
): number {
  const totalWeight = rows.reduce((sum, row) => sum + Number(row[weightField] || 0), 0)
  if (!totalWeight) return 0
  const weighted = rows.reduce(
    (sum, row) => sum + Number(row[valueField] || 0) * Number(row[weightField] || 0),
    0,
  )
  return weighted / totalWeight
}
