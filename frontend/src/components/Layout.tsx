import type { ReactNode } from 'react'
import { useStore } from '@/store'
import { Sidebar } from '@/components/Sidebar'
import { TopBar } from '@/components/TopBar'

export function Layout({ children }: { children: ReactNode }) {
  const error = useStore((s) => s.error)
  const notice = useStore((s) => s.notice)

  return (
    <div className="flex h-svh overflow-hidden">
      <Sidebar />
      <main className="flex flex-1 flex-col overflow-hidden">
        <TopBar />
        <div className="flex-1 overflow-y-auto p-6">
          {error ? (
            <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm text-destructive">
              {error}
            </div>
          ) : null}
          {notice ? (
            <div className="mb-4 rounded-md border border-emerald-500/40 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-700 dark:text-emerald-400">
              {notice}
            </div>
          ) : null}
          {children}
        </div>
      </main>
    </div>
  )
}
