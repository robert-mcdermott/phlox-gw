import { useEffect } from 'react'
import { useStore } from '@/store'
import { AuthScreen } from '@/components/AuthScreen'
import { Layout } from '@/components/Layout'
import { OverviewPage } from '@/pages/OverviewPage'
import { ModelsPage } from '@/pages/ModelsPage'
import { ApiKeysPage } from '@/pages/ApiKeysPage'
import { UsagePage } from '@/pages/UsagePage'
import { AdminPage } from '@/pages/AdminPage'
import { THEMES } from '@/lib/theme'

function AppearancePage() {
  const theme = useStore((s) => s.theme)
  const active = THEMES.find((t) => t.id === theme)
  return (
    <div className="rounded-xl border bg-card p-8 text-card-foreground">
      <h3 className="text-base font-semibold">Theme</h3>
      <p className="mt-1 text-sm text-muted-foreground">
        Use the swatches in the sidebar to switch themes. Current theme:{' '}
        <span className="font-medium text-foreground">{active?.name ?? theme}</span>{' '}
        ({active?.dark ? 'Dark' : 'Light'}). Selection is remembered on this device.
      </p>
    </div>
  )
}

function Page() {
  const tab = useStore((s) => s.tab)
  switch (tab) {
    case 'keys':
      return <ApiKeysPage />
    case 'models':
      return <ModelsPage />
    case 'usage':
      return <UsagePage />
    case 'appearance':
      return <AppearancePage />
    case 'admin':
      return <AdminPage />
    default:
      return <OverviewPage />
  }
}

function App() {
  const token = useStore((s) => s.token)
  const user = useStore((s) => s.user)
  const refresh = useStore((s) => s.refresh)

  useEffect(() => {
    void refresh()
  }, [refresh])

  if (!token || !user) {
    return <AuthScreen />
  }

  return (
    <Layout>
      <Page />
    </Layout>
  )
}

export default App
