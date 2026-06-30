import { useState } from 'react'
import { useStore, defaultGuardrailPolicy } from '@/store'
import { AdminGuardrails } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { NativeSelect } from '@/components/ui/native-select'
import { Badge } from '@/components/ui/badge'
import { AdminPanel, CheckField, FormField, MetricStrip, MiniMetric, useAdminAction } from './shared'
import type { GuardrailAction, GuardrailCustomPattern, GuardrailPhase, GuardrailPolicy } from '@/types'

type BuiltinField =
  | 'detect_email'
  | 'detect_phone'
  | 'detect_ssn'
  | 'detect_credit_card'
  | 'detect_api_key'

const BUILTIN_PATTERNS: { label: string; token: string; field: BuiltinField; regex: string }[] = [
  { label: 'Email address', token: '[EMAIL]', field: 'detect_email', regex: String.raw`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b` },
  { label: 'Phone number', token: '[PHONE]', field: 'detect_phone', regex: String.raw`\b(?:\+?1[\s.\-]?)?(?:\([2-9][0-9]{2}\)|[2-9][0-9]{2})[\s.\-]?[0-9]{3}[\s.\-]?[0-9]{4}\b` },
  { label: 'Social Security number', token: '[SSN]', field: 'detect_ssn', regex: String.raw`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b` },
  { label: 'Credit card number', token: '[CREDIT_CARD]', field: 'detect_credit_card', regex: String.raw`\b(?:[0-9][ -]*?){13,19}\b with Luhn validation` },
  { label: 'API key or token', token: '[API_KEY]', field: 'detect_api_key', regex: String.raw`(?i)\b(?:pgw-sk-[A-Za-z0-9_\-]{16,}|sk-[A-Za-z0-9_\-]{16,}|...)\b` },
]

const ACTIONS: { value: GuardrailAction; label: string }[] = [
  { value: 'off', label: 'Off' },
  { value: 'redact', label: 'Redact' },
  { value: 'block', label: 'Block' },
]

export function GuardrailTab() {
  const stored = useStore((s) => s.guardrailPolicy)
  const preview = useStore((s) => s.guardrailPreview)
  const setPreview = useStore((s) => s.setGuardrailPreview)
  const previewText = useStore((s) => s.guardrailPreviewText)
  const setPreviewText = useStore((s) => s.setGuardrailPreviewText)
  const setError = useStore((s) => s.setError)
  const run = useAdminAction()

  // Local working copy of the policy.
  const [policy, setPolicy] = useState<GuardrailPolicy>(() => ({
    ...defaultGuardrailPolicy(),
    ...(stored || {}),
  }))
  const [phase, setPhase] = useState<GuardrailPhase>('input')

  const patch = (p: Partial<GuardrailPolicy>) => setPolicy((prev) => ({ ...prev, ...p }))
  const patternsActive = (policy.custom_patterns || []).filter((c) => c.enabled && c.pattern).length

  const setCustom = (index: number, p: Partial<GuardrailCustomPattern>) =>
    setPolicy((prev) => ({
      ...prev,
      custom_patterns: (prev.custom_patterns || []).map((c, i) => (i === index ? { ...c, ...p } : c)),
    }))

  const addCustom = () =>
    setPolicy((prev) => {
      const list = prev.custom_patterns || []
      return {
        ...prev,
        custom_patterns: [
          ...list,
          { id: `custom-${list.length + 1}`, name: '', pattern: '', action: 'redact', redaction_text: '', enabled: true },
        ],
      }
    })

  const removeCustom = (index: number) =>
    setPolicy((prev) => ({
      ...prev,
      custom_patterns: (prev.custom_patterns || []).filter((_, i) => i !== index),
    }))

  const save = () => run(() => AdminGuardrails.update(policy), { notice: 'Guardrail policy saved.' })

  const runPreview = async () => {
    try {
      const result = await AdminGuardrails.preview(policy, previewText, phase)
      setPreview(result)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const previewOutput = preview
    ? preview.blocked
      ? `Blocked by policy. Matching patterns: ${(preview.findings || []).join(', ')}`
      : preview.output
    : ''

  return (
    <div className="space-y-6">
      <AdminPanel
        title="PII policy"
        note="Built-in detector for email, phone, SSN, credit card, and API key patterns. Content is inspected in memory and not stored."
      >
        <div className="space-y-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CheckField label="Enabled" checked={policy.enabled} onChange={(e) => patch({ enabled: e.target.checked })} />
            <Button onClick={save}>Save policy</Button>
          </div>

          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <FormField label="Input action">
              <NativeSelect value={policy.input_action} onChange={(e) => patch({ input_action: e.target.value as GuardrailAction })}>
                {ACTIONS.map((a) => (
                  <option key={a.value} value={a.value}>{a.label}</option>
                ))}
              </NativeSelect>
            </FormField>
            <FormField label="Output action" help="Output block rejects streaming requests and blocks non-streaming responses after provider return.">
              <NativeSelect value={policy.output_action} onChange={(e) => patch({ output_action: e.target.value as GuardrailAction })}>
                {ACTIONS.map((a) => (
                  <option key={a.value} value={a.value}>{a.label}</option>
                ))}
              </NativeSelect>
            </FormField>
            <FormField label="Redaction text">
              <Input value={policy.redaction_text || '[REDACTED]'} onChange={(e) => patch({ redaction_text: e.target.value })} />
            </FormField>
          </div>

          {/* Built-in patterns */}
          <div>
            <div className="mb-2">
              <div className="text-sm font-semibold">Built-in patterns</div>
              <p className="text-xs text-muted-foreground">Email, phone, SSN, credit card, and API key detectors.</p>
            </div>
            <div className="space-y-2">
              {BUILTIN_PATTERNS.map((pattern) => (
                <div key={pattern.field} className="flex flex-wrap items-center gap-3 rounded-md border px-3 py-2">
                  <CheckField label={pattern.label} checked={Boolean(policy[pattern.field])} onChange={(e) => patch({ [pattern.field]: e.target.checked } as Partial<GuardrailPolicy>)} />
                  <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{pattern.regex}</code>
                  <span className="font-mono text-xs text-muted-foreground">{pattern.token}</span>
                  <Badge variant="secondary">global {policy.input_action || 'redact'}/{policy.output_action || 'redact'}</Badge>
                </div>
              ))}
            </div>
          </div>

          {/* Custom patterns */}
          <div>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div>
                <div className="text-sm font-semibold">Custom patterns</div>
                <p className="text-xs text-muted-foreground">RE2-compatible regular expressions for organization-specific identifiers.</p>
              </div>
              <Button variant="outline" size="sm" onClick={addCustom}>Add pattern</Button>
            </div>
            <div className="space-y-3">
              {(policy.custom_patterns || []).length === 0 ? (
                <p className="text-sm text-muted-foreground">No custom patterns yet.</p>
              ) : (
                (policy.custom_patterns || []).map((pattern, index) => (
                  <div key={pattern.id || index} className="grid items-end gap-2 rounded-md border px-3 py-3 sm:grid-cols-2 lg:grid-cols-6">
                    <CheckField label="Enabled" checked={pattern.enabled !== false} onChange={(e) => setCustom(index, { enabled: e.target.checked })} />
                    <FormField label="Name"><Input placeholder="Employee ID" value={pattern.name || ''} onChange={(e) => setCustom(index, { name: e.target.value })} /></FormField>
                    <FormField label="Regex"><Input placeholder="EMP-[0-9]+" value={pattern.pattern || ''} onChange={(e) => setCustom(index, { pattern: e.target.value })} /></FormField>
                    <FormField label="Action">
                      <NativeSelect value={pattern.action || 'redact'} onChange={(e) => setCustom(index, { action: e.target.value as GuardrailAction })}>
                        <option value="redact">Redact</option>
                        <option value="block">Block</option>
                      </NativeSelect>
                    </FormField>
                    <FormField label="Replacement"><Input placeholder="[REDACTED]" value={pattern.redaction_text || ''} onChange={(e) => setCustom(index, { redaction_text: e.target.value })} /></FormField>
                    <Button variant="destructive" size="sm" onClick={() => removeCustom(index)}>Remove</Button>
                  </div>
                ))
              )}
            </div>
          </div>

          {/* Preview */}
          <div>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div>
                <div className="text-sm font-semibold">Test patterns</div>
                <p className="text-xs text-muted-foreground">Preview redaction or blocking against sample text. Samples are sent only to the local admin API and are not stored.</p>
              </div>
              <div className="flex gap-2">
                <NativeSelect className="w-40" value={phase} onChange={(e) => setPhase(e.target.value as GuardrailPhase)}>
                  <option value="input">Input policy</option>
                  <option value="output">Output policy</option>
                </NativeSelect>
                <Button variant="outline" size="sm" onClick={runPreview}>Preview</Button>
              </div>
            </div>
            <Textarea rows={3} placeholder="Paste sample text to test..." value={previewText} onChange={(e) => setPreviewText(e.target.value)} />
            <div className="mt-2 rounded-md border px-3 py-2">
              {preview ? (
                <>
                  <div className="mb-1 flex items-center gap-2">
                    <Badge variant={preview.blocked ? 'destructive' : preview.redacted ? 'success' : 'secondary'}>
                      {preview.blocked ? 'blocked' : preview.redacted ? 'redacted' : 'no match'}
                    </Badge>
                    <span className="text-sm text-muted-foreground">{(preview.findings || []).join(', ') || 'No findings'}</span>
                  </div>
                  <pre className="whitespace-pre-wrap break-words text-sm">{previewOutput}</pre>
                </>
              ) : (
                <span className="text-sm text-muted-foreground">Run preview to see what the provider or client would receive.</span>
              )}
            </div>
          </div>
        </div>
      </AdminPanel>

      <MetricStrip>
        <MiniMetric label="Policy" value={policy.enabled ? 'enabled' : 'disabled'} />
        <MiniMetric label="Input" value={policy.input_action || 'redact'} />
        <MiniMetric label="Output" value={policy.output_action || 'redact'} />
        <MiniMetric label="Custom patterns" value={patternsActive} />
        <MiniMetric label="Streaming block" value={policy.output_action === 'block' ? 'rejected' : 'allowed'} />
      </MetricStrip>
    </div>
  )
}
