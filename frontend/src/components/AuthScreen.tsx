import { useState } from 'react'
import { useStore } from '@/store'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import phloxLogo from '/phlox-logo.svg'

export function AuthScreen() {
  const login = useStore((s) => s.login)
  const oidcConfig = useStore((s) => s.oidcConfig)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('admin')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      await login(username, password)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="min-h-svh flex items-center justify-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="flex items-center gap-3">
            <img src={phloxLogo} alt="" className="h-12 w-12" />
            <div>
              <h1 className="text-xl font-semibold tracking-tight">Phlox-GW</h1>
              <p className="text-sm text-muted-foreground">Enterprise LLM gateway</p>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <p className="mb-4 text-sm text-muted-foreground">
            Sign in with the local admin account to configure models, keys,
            budgets, and usage reporting.
          </p>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            {error ? (
              <p className="text-sm text-destructive" role="alert">
                {error}
              </p>
            ) : null}
            <Button type="submit" className="w-full" disabled={submitting}>
              {submitting ? 'Signing in…' : 'Sign in'}
            </Button>
            {oidcConfig?.enabled ? (
              <Button
                type="button"
                variant="outline"
                className="w-full"
                onClick={() => {
                  window.location.href = '/api/auth/oidc/login'
                }}
              >
                Sign in with {oidcConfig.display_name || 'SSO'}
              </Button>
            ) : null}
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
