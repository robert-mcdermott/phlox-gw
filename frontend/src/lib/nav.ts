// Navigation metadata for the dashboard shell.
// Ported from the tab list and ADMIN_SECTIONS in the previous vanilla
// frontend (frontend/src/static/app.js).

import {
  BarChart3,
  Cpu,
  FileText,
  Gauge,
  KeyRound,
  LayoutGrid,
  type LucideIcon,
  Network,
  Palette,
  Server,
  Shield,
  Users,
  Wallet,
} from 'lucide-react'
import type { AdminTab, TopTab } from '@/store'

export interface TopTabMeta {
  id: TopTab
  label: string
  icon: LucideIcon
  title: string
  subtitle: string
}

export const TOP_TABS: TopTabMeta[] = [
  {
    id: 'overview',
    label: 'Overview',
    icon: LayoutGrid,
    title: 'Gateway overview',
    subtitle: 'Provider-neutral access with cost and budget controls.',
  },
  {
    id: 'keys',
    label: 'API Keys',
    icon: KeyRound,
    title: 'API keys',
    subtitle: 'Mint and revoke user-owned keys for SDK access.',
  },
  {
    id: 'models',
    label: 'Models',
    icon: Cpu,
    title: 'Model catalog',
    subtitle: 'Enabled model routes and administrator-assigned pricing.',
  },
  {
    id: 'usage',
    label: 'Usage',
    icon: BarChart3,
    title: 'Usage and cost',
    subtitle: 'Per-user tokens, request counts, and chargeback cost.',
  },
  {
    id: 'appearance',
    label: 'Appearance',
    icon: Palette,
    title: 'Appearance',
    subtitle: 'Theme selection and local display preferences.',
  },
  {
    id: 'admin',
    label: 'Admin',
    icon: Shield,
    title: 'Administration',
    subtitle: 'Users, providers, budgets, and aggregate reporting.',
  },
]

export interface AdminSectionMeta {
  id: AdminTab
  label: string
  icon: LucideIcon
  description: string
}

export const ADMIN_SECTIONS: AdminSectionMeta[] = [
  { id: 'operations', label: 'Operations', icon: BarChart3, description: '30-day usage, latency, cost, and error movement.' },
  { id: 'cluster', label: 'Cluster', icon: Network, description: 'Deployment mode, database backend, readiness, and node heartbeats.' },
  { id: 'requests', label: 'Requests', icon: FileText, description: 'Search gateway request metadata without prompt or response bodies.' },
  { id: 'guardrails', label: 'Guardrails', icon: Shield, description: 'Configure built-in PII detection, redaction, and blocking policies.' },
  { id: 'config', label: 'Configuration', icon: FileText, description: 'Export signed, sanitized admin configuration for review or migration.' },
  { id: 'providers', label: 'Providers', icon: Server, description: 'Configure upstream providers and health state.' },
  { id: 'models', label: 'Models', icon: Cpu, description: 'Expose model routes, prices, context metadata, and health tests.' },
  { id: 'users', label: 'Users', icon: Users, description: 'Manage local users, departments, roles, and passwords.' },
  { id: 'keys', label: 'API Keys', icon: KeyRound, description: 'Govern user-owned API keys, allowlists, budgets, and per-key limits.' },
  { id: 'limits', label: 'Rate Limits', icon: Gauge, description: 'Set RPM and TPM controls by user, department, provider, or model.' },
  { id: 'budgets', label: 'Budgets', icon: Wallet, description: 'Cap monthly spend by user or department.' },
  { id: 'audit', label: 'Audit Log', icon: FileText, description: 'Review recent local auth, admin, and key lifecycle events.' },
]
