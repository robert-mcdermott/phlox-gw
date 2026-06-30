import { LogOut } from 'lucide-react'
import { useStore } from '@/store'
import { Button } from '@/components/ui/button'
import { TOP_TABS } from '@/lib/nav'

export function TopBar() {
  const tab = useStore((s) => s.tab)
  const user = useStore((s) => s.user)
  const logout = useStore((s) => s.logout)
  const meta = TOP_TABS.find((t) => t.id === tab) ?? TOP_TABS[0]

  return (
    <header className="flex items-center justify-between border-b bg-background px-6 py-4">
      <div>
        <h2 className="text-lg font-semibold tracking-tight">{meta.title}</h2>
        <p className="text-sm text-muted-foreground">{meta.subtitle}</p>
      </div>
      <div className="flex items-center gap-3">
        <span className="text-sm text-muted-foreground">
          {user?.username}
          {user?.role ? <span className="text-muted-foreground/60"> · {user.role}</span> : null}
        </span>
        <Button variant="outline" size="sm" onClick={logout}>
          <LogOut className="size-4" />
          Sign out
        </Button>
      </div>
    </header>
  )
}
