const THEME_STORAGE_KEY = 'phlox-gw-theme';
const DEFAULT_THEME = 'phlox-dark';
const THEMES = [
  { id: 'phlox-dark', name: 'Phlox Dark', swatch: ['#160821', '#DF00FF', '#f0e6f7'], dark: true },
  { id: 'phlox-light', name: 'Phlox Light', swatch: ['#faf5fe', '#C200DE', '#2a1438'], dark: false },
  { id: 'fred-hutch', name: 'Fred Hutch', swatch: ['#1B365D', '#00ABC8', '#FFB500'], dark: false },
  { id: 'light', name: 'Light', swatch: ['#ffffff', '#0ea5b7', '#111827'], dark: false },
  { id: 'dark', name: 'Dark', swatch: ['#0f172a', '#22d3ee', '#e2e8f0'], dark: true },
  { id: 'hutch-night', name: 'Hutch Night', swatch: ['#10192b', '#AA4AC4', '#00ABC8'], dark: true },
  { id: 'sandstone', name: 'Sandstone', swatch: ['#faf5ee', '#b8860b', '#1B365D'], dark: false },
  { id: 'terminal', name: 'Terminal', swatch: ['#000000', '#00ff41', '#00ff41'], dark: true }
];

const state = {
  token: localStorage.getItem('phlox_gw_token') || '',
  user: null,
  tab: 'overview',
  theme: initialTheme(),
  health: null,
  models: [],
  providers: [],
  keys: [],
  usage: null,
  adminUsage: null,
  users: [],
  budgets: [],
  rateLimits: [],
  adminKeys: [],
  adminModels: [],
  auditLogs: [],
  requestLog: { items: [], total: 0, limit: 100, offset: 0 },
  requestFilters: { q: '', days: '7', status: 'any', protocol: '', provider_id: '', model: '', department: '', streaming: '' },
  guardrailPolicy: null,
  guardrailPreview: null,
  guardrailPreviewText: 'Contact me at jane@example.com, call 206-555-2407, employee EMP-12345.',
  clusterStatus: null,
  usageSeries: [],
  usageDrilldowns: { providers: [], models: [] },
  budgetBurnDown: [],
  oidcConfig: { enabled: false, display_name: 'Entra ID' },
  adminTab: 'operations',
  playground: { route: '', system: '', draft: '', maxTokens: 1024, busy: false, messages: [] },
  chargeback: { month: '', report: null, loading: false, error: '', collapsed: {} },
  secret: '',
  error: '',
  notice: ''
};

applyTheme(state.theme, false);

const ADMIN_SECTIONS = [
  { id: 'operations', label: 'Operations', icon: 'chart', description: '30-day usage, latency, cost, and error movement.' },
  { id: 'cluster', label: 'Cluster', icon: 'network', description: 'Deployment mode, database backend, readiness, and node heartbeats.' },
  { id: 'requests', label: 'Requests', icon: 'file', description: 'Search gateway request metadata without prompt or response bodies.' },
  { id: 'guardrails', label: 'Guardrails', icon: 'shield', description: 'Configure built-in PII detection, redaction, and blocking policies.' },
  { id: 'config', label: 'Configuration', icon: 'file', description: 'Export signed, sanitized admin configuration for review or migration.' },
  { id: 'providers', label: 'Providers', icon: 'server', description: 'Configure upstream providers and health state.' },
  { id: 'models', label: 'Models', icon: 'cpu', description: 'Expose model routes, prices, context metadata, and health tests.' },
  { id: 'playground', label: 'Playground', icon: 'chat', description: 'Send test chat messages through a model route to validate providers and models.' },
  { id: 'users', label: 'Users', icon: 'users', description: 'Manage local users, departments, roles, and passwords.' },
  { id: 'keys', label: 'API Keys', icon: 'key', description: 'Govern user-owned API keys, allowlists, budgets, and per-key limits.' },
  { id: 'limits', label: 'Rate Limits', icon: 'gauge', description: 'Set RPM and TPM controls by user, department, provider, or model.' },
  { id: 'budgets', label: 'Budgets', icon: 'wallet', description: 'Cap monthly spend by user or department.' },
  { id: 'audit', label: 'Audit Log', icon: 'file', description: 'Review recent local auth, admin, and key lifecycle events.' }
];

const BUILTIN_GUARDRAIL_PATTERNS = [
  {
    key: 'email',
    label: 'Email address',
    token: '[EMAIL]',
    checkboxId: 'guard-detect-email',
    field: 'detect_email',
    regex: String.raw`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`
  },
  {
    key: 'phone',
    label: 'Phone number',
    token: '[PHONE]',
    checkboxId: 'guard-detect-phone',
    field: 'detect_phone',
    regex: String.raw`\b(?:\+?1[\s.\-]?)?(?:\([2-9][0-9]{2}\)|[2-9][0-9]{2})[\s.\-]?[0-9]{3}[\s.\-]?[0-9]{4}\b`
  },
  {
    key: 'ssn',
    label: 'Social Security number',
    token: '[SSN]',
    checkboxId: 'guard-detect-ssn',
    field: 'detect_ssn',
    regex: String.raw`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`
  },
  {
    key: 'credit_card',
    label: 'Credit card number',
    token: '[CREDIT_CARD]',
    checkboxId: 'guard-detect-credit-card',
    field: 'detect_credit_card',
    regex: String.raw`\b(?:[0-9][ -]*?){13,19}\b with Luhn validation`
  },
  {
    key: 'api_key',
    label: 'API key or token',
    token: '[API_KEY]',
    checkboxId: 'guard-detect-api-key',
    field: 'detect_api_key',
    regex: String.raw`(?i)\b(?:pgw-sk-[A-Za-z0-9_\-]{16,}|sk-[A-Za-z0-9_\-]{16,}|xox[baprs]-[A-Za-z0-9\-]{10,}|gh[pousr]_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_\-]{20,}|AKIA[0-9A-Z]{16})\b`
  }
];

const app = document.getElementById('app');

function api(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) };
  if (state.token) headers.Authorization = `Bearer ${state.token}`;
  return fetch(path, { ...options, headers }).then(async (res) => {
    if (!res.ok) {
      let message = `${res.status} ${res.statusText}`;
      try {
        const body = await res.json();
        message = body?.error?.message || body?.error || body?.message || body?.snippet || message;
      } catch (_) {}
      throw new Error(message);
    }
    if (res.status === 204) return null;
    return res.json();
  });
}

function requestLogPath(includePaging = true) {
  const params = requestLogParams(includePaging);
  return `/api/admin/request-log${params.toString() ? `?${params}` : ''}`;
}

function requestLogParams(includePaging = true) {
  const f = state.requestFilters || {};
  const params = new URLSearchParams();
  if (f.q) params.set('q', f.q);
  if (f.days) params.set('days', f.days);
  if (f.status && f.status !== 'any') params.set('status', f.status);
  if (f.protocol) params.set('protocol', f.protocol);
  if (f.provider_id) params.set('provider_id', f.provider_id);
  if (f.model) params.set('model', f.model);
  if (f.department) params.set('department', f.department);
  if (f.streaming) params.set('streaming', f.streaming);
  if (includePaging) {
    params.set('limit', String(state.requestLog?.limit || 100));
    params.set('offset', String(state.requestLog?.offset || 0));
  }
  return params;
}

async function refresh() {
  state.error = '';
  try {
    state.health = await api('/api/health', { headers: {} });
    state.oidcConfig = await api('/api/auth/oidc/config', { headers: {} });
    if (state.token) {
      state.user = await api('/api/auth/me');
      const base = [api('/api/models'), api('/api/api-keys'), api('/api/usage')];
      const [models, keys, usage] = await Promise.all(base);
      state.models = models || [];
      state.keys = keys || [];
      state.usage = usage;
      if (state.user.role === 'admin') {
        const [providers, users, budgets, rateLimits, adminUsage, adminModels, adminKeys, auditLogs, requestLog, guardrailPolicy, clusterStatus, usageSeries, usageDrilldowns, budgetBurnDown] = await Promise.all([
          api('/api/admin/providers'),
          api('/api/admin/users'),
          api('/api/admin/budgets'),
          api('/api/admin/rate-limits'),
          api('/api/admin/usage/summary'),
          api('/api/admin/models'),
          api('/api/admin/api-keys'),
          api('/api/admin/audit-log?limit=100'),
          api(requestLogPath()),
          api('/api/admin/guardrails'),
          api('/api/admin/cluster/status'),
          api('/api/admin/usage/timeseries?days=30'),
          api('/api/admin/usage/drilldowns?days=30'),
          api('/api/admin/budgets/burndown')
        ]);
        state.providers = providers || [];
        state.users = users || [];
        state.budgets = budgets || [];
        state.rateLimits = rateLimits || [];
        state.adminUsage = adminUsage;
        state.adminModels = adminModels || [];
        state.adminKeys = adminKeys || [];
        state.auditLogs = auditLogs || [];
        state.requestLog = requestLog || { items: [], total: 0, limit: 100, offset: 0 };
        state.guardrailPolicy = guardrailPolicy || defaultGuardrailPolicy();
        state.clusterStatus = clusterStatus || null;
        state.usageSeries = usageSeries || [];
        state.usageDrilldowns = usageDrilldowns || { providers: [], models: [] };
        state.budgetBurnDown = budgetBurnDown || [];
      }
    }
  } catch (err) {
    state.error = err.message;
    if (err.message.includes('invalid session') || err.message.includes('missing bearer')) {
      logout();
      return;
    }
  }
  render();
}

function loginView() {
  app.innerHTML = `
    <div class="login">
      <div class="brand">
        <div class="mark logo-mark"><img src="/phlox-logo.svg" alt="" /></div>
        <div><h1>Phlox-GW</h1><p>Enterprise LLM gateway</p></div>
      </div>
      <p>Sign in with the local admin account to configure models, keys, budgets, and usage reporting.</p>
      <div class="field"><label>Username</label><input id="username" autocomplete="username" value="admin" /></div>
      <div class="field"><label>Password</label><input id="password" type="password" autocomplete="current-password" value="admin" /></div>
      <div class="error" id="login-error"></div>
      <button class="btn primary" id="login-btn">Sign in</button>
      ${state.oidcConfig?.enabled ? `<button class="btn sso" id="oidc-login">Sign in with ${esc(state.oidcConfig.display_name || 'SSO')}</button>` : ''}
    </div>
  `;
  document.getElementById('login-btn').onclick = async () => {
    const error = document.getElementById('login-error');
    error.textContent = '';
    try {
      const body = {
        username: document.getElementById('username').value,
        password: document.getElementById('password').value
      };
      const resp = await api('/api/auth/login', { method: 'POST', body: JSON.stringify(body), headers: {} });
      state.token = resp.token;
      localStorage.setItem('phlox_gw_token', state.token);
      await refresh();
    } catch (err) {
      error.textContent = err.message;
    }
  };
  const oidcLogin = document.getElementById('oidc-login');
  if (oidcLogin) {
    oidcLogin.onclick = () => {
      window.location.href = '/api/auth/oidc/login';
    };
  }
}

function shell(content) {
  const tabs = [
    ['overview', 'Overview', 'grid'],
    ['keys', 'API Keys', 'key'],
    ['models', 'Models', 'cpu'],
    ['usage', 'Usage', 'chart'],
    ['appearance', 'Appearance', 'palette'],
    ['admin', 'Admin', 'shield']
  ];
  app.innerHTML = `
    <div class="app">
      <aside class="sidebar">
        <div class="brand">
          <div class="mark logo-mark"><img src="/phlox-logo.svg" alt="" /></div>
          <div><h1>Phlox-GW</h1><p>LLM gateway</p></div>
        </div>
        <nav class="nav">
          ${tabs.map(([id, label, glyph]) => `
            <button data-tab="${id}" class="${state.tab === id ? 'active' : ''}">${icon(glyph)}<span>${label}</span></button>
            ${id === 'admin' ? adminSidebarMenu() : ''}
          `).join('')}
        </nav>
      </aside>
      <main class="main">
        <div class="topbar">
          <div><h2>${titleForTab()}</h2><p>${subtitleForTab()}</p></div>
          <div class="session"><span>${state.user?.username || ''} · ${state.user?.role || ''}</span><button class="btn" id="logout">Sign out</button></div>
        </div>
        ${state.error ? `<div class="panel error">${state.error}</div>` : ''}
        ${state.notice ? `<div class="panel success">${esc(state.notice)}</div>` : ''}
        ${content}
      </main>
    </div>
  `;
  document.querySelectorAll('[data-tab]').forEach((btn) => {
    btn.onclick = () => {
      state.tab = btn.dataset.tab;
      render();
    };
  });
  document.getElementById('logout').onclick = logout;
}

function adminSidebarMenu() {
  if (state.tab !== 'admin' || state.user?.role !== 'admin') return '';
  if (!ADMIN_SECTIONS.some(section => section.id === state.adminTab)) state.adminTab = 'operations';
  return `
    <div class="admin-subnav" aria-label="Admin sections">
      ${ADMIN_SECTIONS.map(section => `
        <button data-admin-tab="${section.id}" class="${state.adminTab === section.id ? 'active' : ''}">
          ${icon(section.icon)}
          <span>${esc(section.label)}</span>
          ${adminSectionCount(section.id)}
        </button>
      `).join('')}
    </div>
  `;
}

function render() {
  if (!state.token || !state.user) {
    loginView();
    return;
  }
  if (state.tab === 'keys') return shell(keysView());
  if (state.tab === 'models') return shell(modelsView());
  if (state.tab === 'usage') return shell(usageView());
  if (state.tab === 'appearance') return shell(appearanceView());
  if (state.tab === 'admin') return shell(adminView());
  return shell(overviewView());
}

function overviewView() {
  const usage = state.usage || {};
  return `
    <section class="grid">
      ${card('Gateway', state.health?.status || 'unknown', 'Embedded Go server')}
      ${card('Enabled models', state.models.length, 'OpenAI/Anthropic catalog')}
      ${card('Your API keys', state.keys.filter(k => k.is_active).length, 'Active user-owned keys')}
      ${card('Your spend', money(usage.cost_usd || 0), `${usage.total_tokens || 0} tokens`)}
    </section>
    <section class="panel">
      <h3>Gateway endpoints</h3>
      <table>
        <tbody>
          <tr><th>OpenAI-compatible</th><td class="mono">POST /v1/chat/completions</td></tr>
          <tr><th>Model list</th><td class="mono">GET /v1/models</td></tr>
          <tr><th>Anthropic-compatible</th><td class="mono">POST /anthropic/v1/messages</td></tr>
        </tbody>
      </table>
    </section>
  `;
}

function keysView() {
  return `
    <section class="panel">
      <h3>Mint API key</h3>
      <div class="row">
        <input id="key-name" placeholder="Key name" value="Development key" />
        <input id="key-expires" placeholder="Expires RFC3339 (optional)" />
        <button class="btn primary" id="create-key">Create</button>
      </div>
      <div class="success" id="new-secret">${state.secret ? `New key: ${state.secret}` : ''}</div>
    </section>
    <section class="panel">
      <h3>Your API keys</h3>
      ${selfKeyRows()}
    </section>
  `;
}

function selfKeyRows() {
  if (!state.keys.length) return '<p>No API keys yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Name</th><th>Prefix</th><th>Status</th><th>Expires</th><th>Last used</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.keys.map(k => `
            <tr data-self-key-row="${esc(k.id)}">
              <td><input data-self-key-field="name" value="${attr(k.name)}" ${k.is_active ? '' : 'disabled'} /></td>
              <td class="mono">${esc(k.prefix)}</td>
              <td>${keyStatusPill(k)}</td>
              <td><input data-self-key-field="expires_at" value="${attr(k.expires_at || '')}" placeholder="RFC3339 or blank" ${k.is_active ? '' : 'disabled'} /></td>
              <td>${fmt(k.last_used_at)}</td>
              <td><div class="actions">${k.is_active ? `<button class="btn" data-save-self-key="${esc(k.id)}">Save</button><button class="btn" data-rotate="${esc(k.id)}">Rotate</button><button class="btn danger" data-revoke="${esc(k.id)}">Revoke</button>` : ''}</div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function modelsView() {
  return `
    <section class="panel">
      <h3>Enabled models</h3>
      ${table(['Route', 'Display name', 'Input / 1M', 'Output / 1M', 'Streaming'], state.models.map(m => [
        `<span class="mono">${esc(m.route)}</span>`, esc(m.display_name), money(m.input_cost_per_million), money(m.output_cost_per_million), pill(m.supports_streaming)
      ]))}
    </section>
  `;
}

function usageView() {
  const u = state.usage || {};
  return `
    <section class="grid">
      ${card('Requests', u.requests || 0, 'Your gateway calls')}
      ${card('Input tokens', u.input_tokens || 0, 'Prompt tokens')}
      ${card('Output tokens', u.output_tokens || 0, 'Completion tokens')}
      ${card('Cost', money(u.cost_usd || 0), 'Priced models only')}
    </section>
    <section class="panel">
      <div class="section-head">
        <h3>Usage by model</h3>
        ${state.user?.role === 'admin' ? '<button class="btn" id="csv-export">Export CSV</button>' : ''}
      </div>
      ${usageTable(u.by_model || [])}
    </section>
  `;
}

function appearanceView() {
  return `
    <section class="panel">
      <div class="section-head">
        <h3 class="section-title">${icon('palette', 'section-icon')}Theme</h3>
        <span>Phlox Dark is the default. Themes apply instantly and are remembered on this device.</span>
      </div>
      <div class="theme-grid">
        ${THEMES.map(t => `
          <button class="theme-card ${state.theme === t.id ? 'active' : ''}" data-theme-id="${attr(t.id)}" aria-pressed="${state.theme === t.id ? 'true' : 'false'}">
            ${state.theme === t.id ? `<span class="theme-check">${icon('check', 'tiny-icon')}</span>` : ''}
            <span class="theme-swatches">
              ${t.swatch.map(color => `<span style="background:${attr(color)}"></span>`).join('')}
            </span>
            <strong>${esc(t.name)}</strong>
            <small>${t.dark ? 'Dark' : 'Light'}</small>
          </button>
        `).join('')}
      </div>
    </section>
  `;
}

function adminView() {
  if (state.user.role !== 'admin') return `<section class="panel">Admin role required.</section>`;
  const usage = state.adminUsage || {};
  if (!ADMIN_SECTIONS.some(section => section.id === state.adminTab)) state.adminTab = 'operations';
  const active = ADMIN_SECTIONS.find(section => section.id === state.adminTab) || ADMIN_SECTIONS[0];
  return `
    <section class="admin-content">
      <section class="admin-heading">
        <div>
          <h3>${icon(active.icon, 'heading-icon')}${esc(active.label)}</h3>
          <p>${esc(active.description)}</p>
        </div>
      </section>
      ${adminContentView(usage)}
    </section>
  `;
}

function adminContentView(usage) {
  if (state.adminTab === 'cluster') {
    return adminPanel('Cluster status', 'network', 'Cluster mode is explicit. SQLite remains the default single-instance deployment.', clusterStatusView());
  }
  if (state.adminTab === 'requests') {
    return `
      ${adminPanel('Request metadata search', 'file', 'Operational request and response metadata only. Prompt text, response text, image bytes, tool contents, and secrets are not stored.', requestLogSearchView())}
      ${adminPanel('Request log', 'file', '', requestLogRows())}
    `;
  }
  if (state.adminTab === 'guardrails') {
    return `
      ${adminPanel('PII policy', 'shield', 'Built-in detector for email, phone, SSN, credit card, and API key patterns. Content is inspected in memory and not stored.', guardrailPolicyView())}
    `;
  }
  if (state.adminTab === 'config') {
    return adminPanel('Signed configuration export', 'file', 'Downloads provider, model, pricing, budget, rate-limit, and guardrail configuration without secrets or usage data.', configExportView());
  }
  if (state.adminTab === 'providers') {
    return `
      ${adminPanel('Add provider', 'server', 'OpenAI-compatible covers Ollama, vLLM, LM Studio, OpenRouter, and LiteLLM. Fields below adapt to the provider type.', `
        <div id="add-provider-form" class="form-stack">
          <div class="form-grid">
            <label class="form-field"><span>Provider id</span><input id="provider-id" placeholder="e.g. local-vllm" /></label>
            <label class="form-field"><span>Display name</span><input id="provider-name" placeholder="e.g. Local vLLM" /></label>
            <label class="form-field"><span>Type</span><select id="provider-type"><option value="openai">OpenAI-compatible</option><option value="anthropic">Anthropic-compatible</option><option value="azure-openai">Azure OpenAI</option><option value="azure-anthropic">Azure Anthropic (Foundry)</option><option value="bedrock">AWS Bedrock</option></select></label>
            <label class="form-field"><span>Status</span><label class="check"><input id="provider-enabled" type="checkbox" checked /> Enabled</label></label>
          </div>
          <div class="form-grid" data-provider-group="api">
            <label class="form-field"><span>Base URL</span><input id="provider-base-url" placeholder="e.g. http://localhost:8000/v1" /></label>
            <label class="form-field"><span>API key env var</span><input id="provider-api-key-env" placeholder="e.g. OPENAI_API_KEY" /></label>
            <label class="form-field"><span>Direct API key (optional)</span><input id="provider-api-key" placeholder="used when the env var is unset" type="password" /></label>
          </div>
          <p class="field-help" data-provider-group="azure-openai-help">Base URL is the Azure OpenAI resource endpoint, e.g. https://myresource.openai.azure.com. Each model's upstream model id is the Azure deployment name. Requests are sent to /openai/deployments/{deployment}/chat/completions with the api-key header.</p>
          <div class="form-grid" data-provider-group="azure-openai">
            <label class="form-field"><span>API version</span><input id="provider-azure-api-version" placeholder="blank uses 2024-10-21" /></label>
          </div>
          <p class="field-help" data-provider-group="azure-anthropic-help">Base URL is the Foundry resource's Anthropic endpoint, e.g. https://myresource.services.ai.azure.com/anthropic. Claude deployments are called through the Anthropic Messages API (/v1/messages).</p>
          <div class="form-grid" data-provider-group="bedrock">
            <label class="form-field"><span>Authentication</span><select id="provider-aws-auth">
              <option value="chain">AWS credential chain (env / IAM role)</option>
              <option value="keys">Access key &amp; secret</option>
              <option value="api_key">Bedrock API key</option>
            </select></label>
            <label class="form-field"><span>AWS region</span><input id="provider-aws-region" placeholder="e.g. us-east-1 (blank uses AWS_REGION)" /></label>
          </div>
          <p class="field-help" data-provider-group="bedrock-chain">Uses the standard AWS credential chain: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN environment variables, shared config profiles, or an attached IAM role (EC2, ECS, EKS). An AWS_BEARER_TOKEN_BEDROCK environment variable is honored as well.</p>
          <div class="form-grid" data-provider-group="bedrock-keys">
            <label class="form-field"><span>Access key id</span><input id="provider-aws-access-key" placeholder="AKIA..." /></label>
            <label class="form-field"><span>Secret access key</span><input id="provider-aws-secret-key" type="password" /></label>
            <label class="form-field"><span>Session token (optional)</span><input id="provider-aws-session-token" type="password" placeholder="for temporary credentials" /></label>
          </div>
          <div class="form-grid" data-provider-group="bedrock-api-key">
            <label class="form-field"><span>Bedrock API key</span><input id="provider-bedrock-api-key" type="password" placeholder="sent as a Bearer token" /></label>
          </div>
          <div><button class="btn primary" id="create-provider">${icon('plus', 'btn-icon')}Add provider</button></div>
        </div>
      `)}
      ${adminPanel('Providers', 'server', '', providerRows())}
    `;
  }
  if (state.adminTab === 'models') {
    return `
      ${adminPanel('Add model', 'cpu', 'Route defaults to provider/model. Fallback and weighted routes reference route ids in the model table below. Prices are USD per 1M tokens.', `
        <div class="form-grid">
          <label class="form-field"><span>Provider</span><select id="model-provider">${state.providers.map(p => `<option value="${esc(p.id)}">${esc(p.id)} · ${esc(p.name)}</option>`).join('')}</select></label>
          <label class="form-field"><span>Upstream model id</span><input id="model-model-id" placeholder="e.g. llama3.1:8b or claude-3-5-sonnet" /></label>
          <label class="form-field"><span>Route id</span><input id="model-route" placeholder="e.g. chat/default" /><small class="field-help">Public model name clients send. Blank becomes provider/model.</small></label>
          <label class="form-field"><span>Display name</span><input id="model-display-name" placeholder="Human-friendly name" /></label>
          <label class="form-field"><span>Input cost / 1M tokens</span><input id="model-input-cost" type="number" min="0" step="0.0001" value="0" /></label>
          <label class="form-field"><span>Output cost / 1M tokens</span><input id="model-output-cost" type="number" min="0" step="0.0001" value="0" /></label>
          <label class="form-field"><span>Context window tokens</span><input id="model-context" type="number" min="0" step="1" value="0" /></label>
          <label class="form-field"><span>Fallback routes</span><textarea id="model-fallback-routes" placeholder="openai/gpt-4o-mini&#10;local-ollama/gemma4:31b-cloud"></textarea><small class="field-help">Existing route ids to try in order after a failure.</small></label>
          <label class="form-field"><span>Weighted routes</span><textarea id="model-weighted-routes" placeholder="openai/gpt-4o-mini 80&#10;local-vllm/llama-3.1-8b 20"></textarea><small class="field-help">Existing route id plus relative traffic weight.</small></label>
          <label class="form-field"><span>Retries per candidate</span><input id="model-retry-attempts" type="number" min="0" max="5" step="1" value="0" /></label>
          <label class="form-field"><span>Request timeout ms</span><input id="model-timeout-ms" type="number" min="0" step="1000" value="0" /></label>
          <label class="check"><input id="model-streaming" type="checkbox" checked /> Streaming</label>
          <label class="check"><input id="model-enabled" type="checkbox" checked /> Enabled</label>
          <label class="check"><input id="model-health-routing" type="checkbox" checked /> Health routing</label>
          <button class="btn primary" id="create-model">${icon('plus', 'btn-icon')}Add model</button>
        </div>
      `)}
      ${adminPanel('Models and pricing', 'cpu', '', modelRows())}
    `;
  }
  if (state.adminTab === 'playground') {
    return playgroundView();
  }
  if (state.adminTab === 'users') {
    return `
      ${adminPanel('Add user', 'users', 'Local users can mint their own API keys after signing in.', `
        <div class="form-grid">
          <input id="user-username" placeholder="Username" />
          <input id="user-password" placeholder="Temporary password" type="password" />
          <input id="user-email" placeholder="Email" />
          <input id="user-display" placeholder="Display name" />
          <input id="user-department" placeholder="Department" />
          <select id="user-role"><option value="user">User</option><option value="admin">Admin</option></select>
          <button class="btn primary" id="create-user">${icon('plus', 'btn-icon')}Create user</button>
        </div>
      `)}
      ${adminPanel('Users', 'users', '', userRows())}
    `;
  }
  if (state.adminTab === 'keys') {
    return adminPanel('API key governance', 'key', 'Empty allowlist means all enabled models. Limits of 0 are unlimited.', keyGovernanceRows());
  }
  if (state.adminTab === 'limits') {
    return `
      ${adminPanel('Add rate limit', 'gauge', 'Scope values use user id, department name, provider id, or model route. Limits of 0 are unlimited.', `
        <div class="form-grid">
          <label class="form-field"><span>Scope</span><select id="rate-scope-type"><option value="user">User</option><option value="department">Department</option><option value="provider">Provider</option><option value="model">Model</option></select></label>
          <label class="form-field"><span>Scope value</span><input id="rate-scope-value" placeholder="User id, department, provider, or model" list="rate-limit-values" /></label>
          <datalist id="rate-limit-values">${rateLimitValueOptions()}</datalist>
          <label class="form-field"><span>Requests/min</span><input id="rate-rpm" aria-label="Requests per minute limit" placeholder="RPM limit" type="number" min="0" step="1" value="0" /></label>
          <label class="form-field"><span>Tokens/min</span><input id="rate-tpm" aria-label="Tokens per minute limit" placeholder="TPM limit" type="number" min="0" step="1" value="0" /></label>
          <button class="btn primary" id="create-rate-limit">${icon('plus', 'btn-icon')}Create limit</button>
        </div>
      `)}
      ${adminPanel('Rate limits', 'gauge', '', rateLimitRows())}
    `;
  }
  if (state.adminTab === 'budgets') {
    return `
      ${adminPanel('Monthly chargeback', 'file', 'Billing report by department and user for a selected month. Download the CSV for finance, or pull the same data as JSON from GET /api/admin/chargeback?month=YYYY-MM for financial-system integration.', chargebackView())}
      ${adminPanel('Budget burn-down', 'chart', 'Current month spend, remaining budget, and projected month-end run rate.', budgetBurnDownView())}
      ${adminPanel('Add budget', 'wallet', 'User budgets use the user id shown in Users. Department budgets use the department name.', `
        <div class="form-grid">
          <select id="budget-scope-type"><option value="department">Department</option><option value="user">User</option></select>
          <input id="budget-scope-value" placeholder="Department name or user id" list="budget-values" />
          <datalist id="budget-values">${budgetValueOptions()}</datalist>
          <input id="budget-limit" placeholder="Monthly limit USD" type="number" min="0" step="0.01" />
          <input id="budget-warn" placeholder="Warn %" type="number" min="1" max="100" step="1" value="90" />
          <button class="btn primary" id="create-budget">${icon('plus', 'btn-icon')}Create budget</button>
        </div>
      `)}
      ${adminPanel('Budgets', 'wallet', '', budgetRows())}
    `;
  }
  if (state.adminTab === 'audit') {
    return adminPanel('Audit log', 'file', 'Recent local auth, admin, and API key lifecycle events.', auditLogRows());
  }
  return `
    <section class="grid">
      ${card('Users', state.users.length, 'Local accounts')}
      ${card('Providers', state.providers.length, 'Configured providers')}
      ${card('Budgets', state.budgets.length, 'Monthly limits')}
      ${card('Rate limits', state.rateLimits.length, 'User, department, provider, model')}
      ${card('API keys', state.adminKeys.length, 'User-owned credentials')}
      ${card('Audit events', state.auditLogs.length, 'Recent admin activity')}
      ${card('Total spend', money(usage.cost_usd || 0), `${usage.requests || 0} requests`)}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Operations</h3><span>Last 30 days</span></div>
      ${monitoringView()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Provider drilldown</h3><span>Last 30 days</span></div>
      ${providerDrilldownRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Model drilldown</h3><span>Last 30 days</span></div>
      ${modelDrilldownRows()}
    </section>
  `;
}

function adminSectionCount(id) {
  const counts = {
    cluster: state.clusterStatus?.cluster_enabled ? (state.clusterStatus?.active_node_count || 0) : 'off',
    requests: state.requestLog?.total || 0,
    guardrails: state.guardrailPolicy?.enabled ? 'on' : 'off',
    config: 'JSON',
    providers: state.providers.length,
    models: state.adminModels.length,
    users: state.users.length,
    keys: state.adminKeys.length,
    limits: state.rateLimits.length,
    budgets: state.budgets.length,
    audit: state.auditLogs.length
  };
  return counts[id] === undefined ? '' : `<small>${counts[id]}</small>`;
}

function adminPanel(title, glyph, note, content) {
  return `
    <section class="panel">
      <div class="section-head">
        <h3 class="section-title">${icon(glyph, 'section-icon')}${esc(title)}</h3>
        ${note ? `<span>${esc(note)}</span>` : ''}
      </div>
      ${content}
    </section>
  `;
}

function configExportView() {
  return `
    <div class="metric-strip">
      ${miniMetric('Providers', state.providers.length)}
      ${miniMetric('Models', state.adminModels.length)}
      ${miniMetric('Budgets', state.budgets.length)}
      ${miniMetric('Rate limits', state.rateLimits.length)}
      ${miniMetric('Guardrails', state.guardrailPolicy?.enabled ? 'enabled' : 'disabled')}
    </div>
    <div class="actions">
      <button class="btn primary" id="config-export">${icon('file', 'btn-icon')}Download signed JSON</button>
      <span class="muted">Excludes direct provider secrets, user credentials, API key hashes, sessions, usage ledger rows, request logs, and audit logs.</span>
    </div>
  `;
}

function clusterStatusView() {
  const status = state.clusterStatus || {};
  const nodes = status.nodes || [];
  return `
    <div class="metric-strip">
      ${miniMetric('Mode', status.deployment_mode || 'unknown')}
      ${miniMetric('Cluster', status.cluster_enabled ? 'enabled' : 'disabled')}
      ${miniMetric('Status', status.status || 'unknown')}
      ${miniMetric('Active nodes', status.active_node_count || 0)}
      ${miniMetric('Stale nodes', status.stale_node_count || 0)}
      ${miniMetric('Database', status.database_driver || 'unknown')}
    </div>
    <div class="cluster-grid">
      <div class="mini-metric cluster-card">
        <div class="label">Current node</div>
        <strong class="mono">${esc(status.instance_id || '')}</strong>
        <span>${esc(status.hostname || '')} ${status.addr ? `· ${esc(status.addr)}` : ''}</span>
        <span>Started ${fmt(status.started_at)}</span>
        <span>Last heartbeat ${fmt(status.last_heartbeat_at)}</span>
      </div>
      <div class="mini-metric cluster-card">
        <div class="label">Database target</div>
        <strong class="mono">${esc(status.database_target || '')}</strong>
        <span>Heartbeat every ${compact(status.heartbeat_interval_seconds || 0)}s; stale after ${compact(status.node_stale_after_seconds || 0)}s</span>
        <span>Signing key: ${status.signing_key_shared ? 'shared file configured' : 'local/default path'}</span>
      </div>
    </div>
    ${(status.notes || []).length ? `<div class="panel-note">${status.notes.map(note => `<p>${esc(note)}</p>`).join('')}</div>` : ''}
    <div class="table-scroll">
      <table>
        <thead><tr><th>Node</th><th>Host</th><th>Address</th><th>Mode</th><th>DB</th><th>Version</th><th>Status</th><th>Started</th><th>Last seen</th><th>Age</th></tr></thead>
        <tbody>
          ${nodes.length ? nodes.map(node => `
            <tr>
              <td><span class="mono">${esc(node.instance_id)}</span>${node.current ? ' <span class="pill on">current</span>' : ''}</td>
              <td>${esc(node.hostname || '')}</td>
              <td class="mono">${esc(node.addr || '')}</td>
              <td>${esc(node.deployment_mode || '')}</td>
              <td>${esc(node.db_driver || '')}</td>
              <td>${esc(node.version || '')}</td>
              <td>${node.stale ? '<span class="pill off">stale</span>' : statusPill(node.status || 'unknown')}</td>
              <td>${fmt(node.started_at)}</td>
              <td>${fmt(node.last_seen_at)}</td>
              <td>${compact(node.age_seconds || 0)}s</td>
            </tr>
          `).join('') : '<tr><td colspan="10">No registered nodes.</td></tr>'}
        </tbody>
      </table>
    </div>
  `;
}

function monitoringView() {
  const rows = state.usageSeries || [];
  if (!rows.length) return '<p>No usage data yet.</p>';
  CHART_REGISTRY.clear();
  const totalRequests = rows.reduce((sum, row) => sum + Number(row.requests || 0), 0);
  const totalErrors = rows.reduce((sum, row) => sum + Number(row.errors || 0), 0);
  const totalCost = rows.reduce((sum, row) => sum + Number(row.cost_usd || 0), 0);
  const totalTokens = rows.reduce((sum, row) => sum + Number(row.total_tokens || 0), 0);
  const errorRate = totalRequests ? totalErrors / totalRequests : 0;
  const avgLatency = weightedAverage(rows, 'avg_latency_ms', 'requests');
  return `
    <div class="metric-strip">
      ${miniMetric('30d cost', money(totalCost))}
      ${miniMetric('30d tokens', compact(totalTokens))}
      ${miniMetric('30d requests', compact(totalRequests))}
      ${miniMetric('30d errors', compact(totalErrors))}
      ${miniMetric('Error rate', percent(errorRate))}
      ${miniMetric('Avg latency', `${Math.round(avgLatency)} ms`)}
    </div>
    <div class="chart-grid">
      ${svgBarChart('Daily cost', rows, {
        series: [{ field: 'cost_usd' }],
        axis: (v) => `$${compact(v)}`,
        total: money(totalCost)
      })}
      ${svgBarChart('Daily tokens', rows, {
        series: [{ field: 'total_tokens' }],
        axis: compact,
        total: compact(totalTokens)
      })}
      ${svgBarChart('Daily requests & errors', rows, {
        series: [{ field: 'requests' }, { field: 'errors', cls: 'bar-error' }],
        axis: compact,
        total: `${compact(totalRequests)} / ${compact(totalErrors)}`,
        legend: [['requests', ''], ['errors', 'sw-error']]
      })}
      ${svgBarChart('Daily avg latency (ms)', rows, {
        series: [{ field: 'avg_latency_ms' }],
        axis: compact,
        total: `${Math.round(avgLatency)} ms`
      })}
    </div>
  `;
}

function miniMetric(label, value) {
  return `<div class="mini-metric"><div class="label">${esc(label)}</div><strong>${esc(String(value))}</strong></div>`;
}

const CHART_REGISTRY = new Map();
const CHART_GEO = { w: 640, h: 200, left: 46, right: 6, top: 10, bottom: 24 };

function niceStep(rawStep) {
  const magnitude = Math.pow(10, Math.floor(Math.log10(rawStep)));
  const norm = rawStep / magnitude;
  if (norm <= 1) return magnitude;
  if (norm <= 2) return 2 * magnitude;
  if (norm <= 5) return 5 * magnitude;
  return 10 * magnitude;
}

function svgBarChart(title, rows, config) {
  const geo = CHART_GEO;
  const plotW = geo.w - geo.left - geo.right;
  const plotH = geo.h - geo.top - geo.bottom;
  const baseline = geo.top + plotH;
  const dataMax = Math.max(...rows.map(row => Math.max(...config.series.map(s => Number(row[s.field] || 0)))), 0);
  const step = niceStep(Math.max(dataMax, 1e-9) / 4);
  const axisMax = Math.max(step, Math.ceil(dataMax / step) * step);
  const ticks = [];
  for (let v = 0; v <= axisMax + step / 2; v += step) ticks.push(v);
  const slot = plotW / rows.length;
  const barW = Math.max(2, slot * 0.62);
  const y = (v) => baseline - (Number(v || 0) / axisMax) * plotH;

  const grid = ticks.map(v => {
    const ty = y(v);
    return `<line class="chart-grid-line" x1="${geo.left}" y1="${ty}" x2="${geo.w - geo.right}" y2="${ty}"/>` +
      `<text class="chart-tick" x="${geo.left - 6}" y="${ty + 3}" text-anchor="end">${esc(config.axis(v))}</text>`;
  }).join('');

  const labelEvery = Math.max(1, Math.ceil(rows.length / 6));
  const xLabels = rows.map((row, i) => {
    if (i % labelEvery !== 0) return '';
    const cx = geo.left + i * slot + slot / 2;
    return `<text class="chart-tick" x="${cx}" y="${geo.h - 8}" text-anchor="middle">${esc(String(row.date || '').slice(5))}</text>`;
  }).join('');

  const bars = config.series.map((s, si) => rows.map((row, i) => {
    const value = Number(row[s.field] || 0);
    if (value <= 0) return '';
    const shrink = si === 0 ? 0 : barW * 0.3;
    const x = geo.left + i * slot + (slot - barW) / 2 + shrink / 2;
    const top = Math.min(y(value), baseline - 1);
    return `<rect class="chart-bar ${s.cls || ''}" x="${x}" y="${top}" width="${barW - shrink}" height="${baseline - top}" rx="1"/>`;
  }).join('')).join('');

  const key = `chart_${CHART_REGISTRY.size}`;
  CHART_REGISTRY.set(key, { rows, geo, slot, primary: config.series.map(s => s.field) });
  const legend = (config.legend || []).map(([label, cls]) =>
    `<span class="chart-legend-item"><i class="chart-swatch ${cls}"></i>${esc(label)}</span>`).join('');
  return `
    <div class="chart-card" data-chart-key="${key}">
      <div class="chart-title"><span>${esc(title)}</span><span class="chart-title-right">${legend}<span class="chart-total">${esc(config.total || '')}</span></span></div>
      <svg class="chart-svg" viewBox="0 0 ${geo.w} ${geo.h}" role="img" aria-label="${esc(title)}">
        <rect class="chart-hover-band" x="0" y="${geo.top}" width="0" height="${plotH}" style="display:none"/>
        ${grid}
        ${bars}
        <line class="chart-axis" x1="${geo.left}" y1="${baseline}" x2="${geo.w - geo.right}" y2="${baseline}"/>
        ${xLabels}
      </svg>
    </div>
  `;
}

function chartTooltipHTML(row, primary) {
  const requests = Number(row.requests || 0);
  const errors = Number(row.errors || 0);
  const line = (field, label, value) =>
    `<div class="chart-tip-row ${primary.includes(field) ? 'primary' : ''}"><span>${esc(label)}</span><strong>${esc(value)}</strong></div>`;
  return `
    <div class="chart-tip-date">${esc(row.date || '')}</div>
    ${line('cost_usd', 'Cost', money(row.cost_usd))}
    ${line('total_tokens', 'Tokens', `${compact(row.total_tokens)} (${compact(row.input_tokens)} in / ${compact(row.output_tokens)} out)`)}
    ${line('requests', 'Requests', compact(requests))}
    ${line('errors', 'Errors', requests ? `${compact(errors)} (${percent(errors / requests)})` : compact(errors))}
    ${line('avg_latency_ms', 'Avg latency', `${Math.round(Number(row.avg_latency_ms || 0))} ms`)}
  `;
}

function ensureChartTooltip() {
  let tip = document.getElementById('chart-tooltip');
  if (!tip) {
    tip = document.createElement('div');
    tip.id = 'chart-tooltip';
    document.body.appendChild(tip);
  }
  tip.style.display = 'none';
  return tip;
}

function wireCharts() {
  const cards = document.querySelectorAll('[data-chart-key]');
  if (!cards.length) return;
  const tooltip = ensureChartTooltip();
  cards.forEach((card) => {
    const cfg = CHART_REGISTRY.get(card.dataset.chartKey);
    const svg = card.querySelector('.chart-svg');
    const band = svg?.querySelector('.chart-hover-band');
    if (!cfg || !svg || !band) return;
    svg.addEventListener('mousemove', (e) => {
      const rect = svg.getBoundingClientRect();
      const xView = ((e.clientX - rect.left) / rect.width) * cfg.geo.w;
      let idx = Math.floor((xView - cfg.geo.left) / cfg.slot);
      idx = Math.max(0, Math.min(cfg.rows.length - 1, idx));
      band.setAttribute('x', cfg.geo.left + idx * cfg.slot);
      band.setAttribute('width', cfg.slot);
      band.style.display = 'block';
      tooltip.innerHTML = chartTooltipHTML(cfg.rows[idx], cfg.primary);
      tooltip.style.display = 'block';
      const pad = 14;
      let x = e.clientX + pad;
      if (x + tooltip.offsetWidth > window.innerWidth - 8) x = e.clientX - tooltip.offsetWidth - pad;
      const yPos = Math.max(8, Math.min(window.innerHeight - tooltip.offsetHeight - 8, e.clientY - tooltip.offsetHeight / 2));
      tooltip.style.left = `${x}px`;
      tooltip.style.top = `${yPos}px`;
    });
    svg.addEventListener('mouseleave', () => {
      tooltip.style.display = 'none';
      band.style.display = 'none';
    });
  });
}

function providerDrilldownRows() {
  const rows = state.usageDrilldowns?.providers || [];
  if (!rows.length) return '<p>No provider usage in the last 30 days.</p>';
  return drilldownTable(['Provider', 'Requests', 'Errors', 'Error rate', 'Tokens', 'Cost', 'Avg latency', 'Last used'], rows.map(row => [
    `<span class="mono">${esc(row.provider_id || '(none)')}</span>`,
    compact(row.requests),
    compact(row.errors),
    percent(row.error_rate),
    compact(row.total_tokens),
    money(row.cost_usd),
    `${Math.round(Number(row.avg_latency_ms || 0))} ms`,
    fmt(row.last_used_at)
  ]));
}

function modelDrilldownRows() {
  const rows = state.usageDrilldowns?.models || [];
  if (!rows.length) return '<p>No model usage in the last 30 days.</p>';
  return drilldownTable(['Model', 'Provider', 'Requests', 'Errors', 'Error rate', 'Tokens', 'Cost', 'Avg latency', 'Last used'], rows.map(row => [
    `<span class="mono">${esc(row.model || '(none)')}</span>`,
    `<span class="mono">${esc(row.provider_id || '(none)')}</span>`,
    compact(row.requests),
    compact(row.errors),
    percent(row.error_rate),
    compact(row.total_tokens),
    money(row.cost_usd),
    `${Math.round(Number(row.avg_latency_ms || 0))} ms`,
    fmt(row.last_used_at)
  ]));
}

function drilldownTable(headers, rows) {
  return `<div class="table-scroll">${table(headers, rows)}</div>`;
}

function providerRows() {
  if (!state.providers.length) return '<p>No providers yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>ID</th><th>Name</th><th>Type</th><th>Connection &amp; credentials</th><th>Enabled</th><th>Health</th><th>Failures</th><th>Last check</th><th>Circuit open</th><th>Last error</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.providers.map(p => providerRow(p)).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function providerRow(p) {
  const auth = p.aws_auth_method || 'chain';
  return `
    <tr data-provider-row="${esc(p.id)}">
      <td class="mono">${esc(p.id)}</td>
      <td><input data-provider-field="name" value="${attr(p.name)}" /></td>
      <td><select data-provider-field="type">${option('openai', 'OpenAI-compatible', p.type)}${option('anthropic', 'Anthropic-compatible', p.type)}${option('azure-openai', 'Azure OpenAI', p.type)}${option('azure-anthropic', 'Azure Anthropic (Foundry)', p.type)}${option('bedrock', 'AWS Bedrock', p.type)}</select></td>
      <td class="connection-cell">
        <div class="cell-stack" data-provider-group="api">
          <label class="mini-field"><span>Base URL</span><input data-provider-field="base_url" value="${attr(p.base_url)}" /></label>
          <label class="mini-field"><span>Key env var</span><input data-provider-field="api_key_env" value="${attr((p.api_key_env || '').replace(' (secret set)', ''))}" /></label>
          <label class="mini-field"><span>Direct key</span><input data-provider-field="api_key" type="password" placeholder="leave blank to keep" /></label>
        </div>
        <div class="cell-stack" data-provider-group="azure-openai">
          <label class="mini-field"><span>API version</span><input data-provider-field="azure_api_version" value="${attr(p.azure_api_version)}" placeholder="blank uses 2024-10-21" /></label>
        </div>
        <div class="cell-stack" data-provider-group="bedrock">
          <label class="mini-field"><span>Auth</span><select data-provider-field="aws_auth_method">${option('chain', 'AWS credential chain', auth)}${option('keys', 'Access key & secret', auth)}${option('api_key', 'Bedrock API key', auth)}</select></label>
          <label class="mini-field"><span>Region</span><input data-provider-field="aws_region" value="${attr(p.aws_region)}" placeholder="blank uses AWS_REGION" /></label>
        </div>
        <div class="cell-stack" data-provider-group="bedrock-keys">
          <label class="mini-field"><span>Access key id</span><input data-provider-field="aws_access_key_id" value="${attr(p.aws_access_key_id)}" /></label>
          <label class="mini-field"><span>Secret key</span><input data-provider-field="aws_secret_access_key" type="password" placeholder="${p.has_aws_secret ? 'leave blank to keep' : 'not set'}" /></label>
          <label class="mini-field"><span>Session token</span><input data-provider-field="aws_session_token" type="password" placeholder="${p.has_aws_secret ? 'kept with secret key' : 'optional'}" /></label>
        </div>
        <div class="cell-stack" data-provider-group="bedrock-api-key">
          <label class="mini-field"><span>API key</span><input data-provider-field="bedrock_api_key" type="password" placeholder="${p.has_bedrock_api_key ? 'leave blank to keep' : 'not set'}" /></label>
        </div>
      </td>
      <td><input data-provider-field="enabled" type="checkbox" ${p.enabled ? 'checked' : ''} /></td>
      <td>${statusPill(p.health_status || 'unknown')}</td>
      <td>${Number(p.consecutive_failures || 0)}</td>
      <td>${fmt(p.last_health_check_at)}</td>
      <td>${fmt(p.circuit_open_until)}</td>
      <td class="wrap">${esc(p.last_error || '')}</td>
      <td><div class="actions"><button class="btn" data-save-provider="${esc(p.id)}">Save</button><button class="btn danger" data-delete-provider="${esc(p.id)}">Delete</button></div></td>
    </tr>
  `;
}

function playgroundView() {
  const pg = state.playground;
  const models = state.adminModels.filter(m => m.enabled);
  const providerEnabled = new Set(state.providers.filter(p => p.enabled).map(p => p.id));
  const routes = models.filter(m => providerEnabled.has(m.provider_id));
  if (!routes.length) {
    return adminPanel('Playground', 'chat', '', '<p>No usable model routes. Enable a provider and a model first, then come back to send test messages.</p>');
  }
  if (!pg.route || !routes.some(m => m.route === pg.route)) pg.route = routes[0].route;
  return adminPanel('Playground', 'chat', 'Send test chat messages through a model route to validate provider credentials, routing, and responses. Playground traffic bypasses API keys, budgets, rate limits, and guardrails, is not recorded in usage, and is audit-logged.', `
    <div class="form-grid">
      <label class="form-field"><span>Model route</span><select id="playground-route">${routes.map(m => option(m.route, `${m.route} · ${m.display_name}`, pg.route)).join('')}</select></label>
      <label class="form-field playground-system"><span>System prompt (optional)</span><input id="playground-system" value="${attr(pg.system)}" placeholder="e.g. You are a helpful assistant." /></label>
      <label class="form-field"><span>Max tokens</span><input id="playground-max-tokens" type="number" min="1" max="8192" step="1" value="${attr(pg.maxTokens)}" /></label>
    </div>
    <div class="playground-transcript" id="playground-transcript">
      ${pg.messages.length ? pg.messages.map(playgroundMessageHTML).join('') : '<p class="playground-empty">No messages yet. Pick a model route and send a message to test it.</p>'}
      ${pg.busy ? '<div class="chat-row assistant"><div class="chat-bubble pending">Waiting for response&hellip;</div></div>' : ''}
    </div>
    <div class="playground-composer">
      <textarea id="playground-input" rows="2" placeholder="Type a message. Enter sends, Shift+Enter adds a line.">${esc(pg.draft)}</textarea>
      <div class="playground-actions">
        <button class="btn primary" id="playground-send" ${pg.busy ? 'disabled' : ''}>Send</button>
        <button class="btn" id="playground-clear" ${pg.messages.length || pg.draft ? '' : 'disabled'}>Clear</button>
      </div>
    </div>
  `);
}

function playgroundMessageHTML(m) {
  if (m.role === 'user') {
    return `<div class="chat-row user"><div class="chat-bubble user${m.failed ? ' failed' : ''}">${esc(m.content)}</div></div>`;
  }
  let meta = '';
  if (m.meta) {
    const parts = [`${esc(m.meta.provider_id)} · ${esc(m.meta.upstream_model)}`];
    if (m.meta.status_code) parts.push(`HTTP ${m.meta.status_code}`);
    parts.push(`${Number(m.meta.latency_ms || 0)} ms`);
    if (m.meta.total_tokens) parts.push(`${m.meta.input_tokens} in / ${m.meta.output_tokens} out tokens`);
    meta = `<div class="chat-meta">${parts.join(' · ')}</div>`;
  }
  return `<div class="chat-row assistant"><div class="chat-bubble assistant${m.error ? ' error' : ''}">${esc(m.content)}</div>${meta}</div>`;
}

async function sendPlaygroundMessage() {
  const pg = state.playground;
  const text = (pg.draft || '').trim();
  if (!text || pg.busy || !pg.route) return;
  const userMessage = { role: 'user', content: text };
  pg.messages.push(userMessage);
  pg.draft = '';
  pg.busy = true;
  render();
  try {
    const history = pg.messages.filter(m => !m.error && !m.failed).map(m => ({ role: m.role, content: m.content }));
    const result = await api('/api/admin/playground/chat', { method: 'POST', body: JSON.stringify({
      route: pg.route,
      system: pg.system.trim(),
      max_tokens: Number(pg.maxTokens) || 1024,
      messages: history
    })});
    if (result.ok) {
      pg.messages.push({ role: 'assistant', content: result.content || '(empty response)', meta: result });
    } else {
      userMessage.failed = true;
      pg.messages.push({ role: 'assistant', content: result.error || 'Request failed.', meta: result, error: true });
    }
  } catch (err) {
    userMessage.failed = true;
    pg.messages.push({ role: 'assistant', content: err.message, error: true });
  }
  pg.busy = false;
  render();
}

function modelRows() {
  if (!state.adminModels.length) return '<p>No models yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Route</th><th>Provider</th><th>Model id</th><th>Name</th><th>Input</th><th>Output</th><th>Context</th><th>Fallback routes</th><th>Weighted routes</th><th>Retries</th><th>Timeout ms</th><th>Health routing</th><th>Streaming</th><th>Enabled</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.adminModels.map(m => `
            <tr data-model-row="${esc(m.id)}">
              <td><input data-model-field="route" value="${attr(m.route)}" /></td>
              <td><select data-model-field="provider_id">${state.providers.map(p => option(p.id, p.id, m.provider_id)).join('')}</select></td>
              <td><input data-model-field="model_id" value="${attr(m.model_id)}" /></td>
              <td><input data-model-field="display_name" value="${attr(m.display_name)}" /></td>
              <td><input data-model-field="input_cost_per_million" type="number" min="0" step="0.0001" value="${attr(m.input_cost_per_million)}" /></td>
              <td><input data-model-field="output_cost_per_million" type="number" min="0" step="0.0001" value="${attr(m.output_cost_per_million)}" /></td>
              <td><input data-model-field="context_window" type="number" min="0" step="1" value="${attr(m.context_window)}" /></td>
              <td><textarea data-model-field="fallback_routes" placeholder="route-a&#10;route-b">${attr(m.fallback_routes || '')}</textarea></td>
              <td><textarea data-model-field="weighted_routes" placeholder="route-a 80&#10;route-b 20">${attr(m.weighted_routes || '')}</textarea></td>
              <td><input data-model-field="retry_attempts" type="number" min="0" max="5" step="1" value="${attr(m.retry_attempts || 0)}" /></td>
              <td><input data-model-field="request_timeout_ms" type="number" min="0" step="1000" value="${attr(m.request_timeout_ms || 0)}" /></td>
              <td><input data-model-field="health_routing_enabled" type="checkbox" ${m.health_routing_enabled !== false ? 'checked' : ''} /></td>
              <td><input data-model-field="supports_streaming" type="checkbox" ${m.supports_streaming ? 'checked' : ''} /></td>
              <td><input data-model-field="enabled" type="checkbox" ${m.enabled ? 'checked' : ''} /></td>
              <td><div class="actions"><button class="btn" data-test-model="${esc(m.id)}">Test</button><button class="btn" data-save-model="${esc(m.id)}">Save</button><button class="btn danger" data-delete-model="${esc(m.id)}">Delete</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function userRows() {
  if (!state.users.length) return '<p>No users yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Username</th><th>Email</th><th>Display</th><th>Department</th><th>Role</th><th>Active</th><th>User budget id</th><th>Reset password</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.users.map(u => `
            <tr data-user-row="${esc(u.id)}">
              <td class="mono">${esc(u.username)}</td>
              <td><input data-user-field="email" value="${attr(u.email)}" /></td>
              <td><input data-user-field="display_name" value="${attr(u.display_name)}" /></td>
              <td><input data-user-field="department" value="${attr(u.department)}" /></td>
              <td><select data-user-field="role">${option('user', 'User', u.role)}${option('admin', 'Admin', u.role)}</select></td>
              <td><input data-user-field="is_active" type="checkbox" ${u.is_active ? 'checked' : ''} /></td>
              <td class="mono">${esc(u.id)}</td>
              <td><input data-reset-password type="password" placeholder="new password" /></td>
              <td><div class="actions"><button class="btn" data-save-user="${esc(u.id)}">Save</button><button class="btn" data-reset-user="${esc(u.id)}">Reset</button><button class="btn danger" data-delete-user="${esc(u.id)}">Delete</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function keyGovernanceRows() {
  if (!state.adminKeys.length) return '<p>No API keys yet.</p>';
  return `
    ${state.secret ? `<div class="success">New rotated key: ${esc(state.secret)}</div>` : ''}
    <div class="table-scroll">
      <table>
        <thead><tr><th>Owner</th><th>Department</th><th>Prefix</th><th>Name</th><th>Active</th><th>Monthly budget</th><th>RPM</th><th>TPM</th><th>Model allowlist</th><th>Month spend</th><th>Expires</th><th>Last used</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.adminKeys.map(k => `
            <tr data-key-row="${esc(k.id)}">
              <td class="mono">${esc(k.username || k.user_id)}</td>
              <td>${esc(k.department || '')}</td>
              <td class="mono">${esc(k.prefix)}</td>
              <td><input data-key-field="name" value="${attr(k.name)}" /></td>
              <td><input data-key-field="is_active" type="checkbox" ${k.is_active ? 'checked' : ''} /></td>
              <td><input data-key-field="budget_usd" type="number" min="0" step="0.01" value="${attr(k.budget_usd)}" /></td>
              <td><input data-key-field="rpm_limit" type="number" min="0" step="1" value="${attr(k.rpm_limit)}" /></td>
              <td><input data-key-field="tpm_limit" type="number" min="0" step="1" value="${attr(k.tpm_limit)}" /></td>
              <td><textarea data-key-field="model_allowlist" rows="2" placeholder="provider/model, one per line">${esc(k.model_allowlist || '')}</textarea></td>
              <td>${money(k.monthly_spend_usd || 0)}</td>
              <td><input data-key-field="expires_at" value="${attr(k.expires_at || '')}" placeholder="RFC3339 or blank" /></td>
              <td>${fmt(k.last_used_at)}</td>
              <td><div class="actions"><button class="btn" data-save-key="${esc(k.id)}">Save</button>${k.is_active ? `<button class="btn" data-rotate-admin-key="${esc(k.id)}">Rotate</button>` : ''}<button class="btn danger" data-revoke-admin-key="${esc(k.id)}">Revoke</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function currentMonthKey() {
  return new Date().toISOString().slice(0, 7);
}

async function loadChargeback(month) {
  const cb = state.chargeback;
  cb.loading = true;
  cb.month = month;
  cb.error = '';
  render();
  try {
    cb.report = await api(`/api/admin/chargeback?month=${encodeURIComponent(month)}`);
  } catch (err) {
    cb.error = err.message;
    cb.report = null;
  }
  cb.loading = false;
  render();
}

function chargebackView() {
  const cb = state.chargeback;
  if (!cb.report && !cb.loading && !cb.error) {
    loadChargeback(cb.month || currentMonthKey());
    return '<p>Loading chargeback report&hellip;</p>';
  }
  if (cb.loading) return '<p>Loading chargeback report&hellip;</p>';
  if (cb.error) return `<p class="error">Could not load chargeback report: ${esc(cb.error)}</p><button class="btn" id="chargeback-retry">Retry</button>`;
  const report = cb.report;
  const months = [...new Set([currentMonthKey(), cb.month, ...(report.available_months || [])])].filter(Boolean).sort().reverse();
  const users = report.departments.reduce((sum, d) => sum + d.users.length, 0);
  const allCollapsed = report.departments.length > 0 && report.departments.every(d => cb.collapsed[d.department]);
  return `
    <div class="chargeback-toolbar">
      <label class="form-field"><span>Billing month</span><select id="chargeback-month">${months.map(m => option(m, m, cb.month)).join('')}</select></label>
      <div class="chargeback-actions">
        <button class="btn" id="chargeback-toggle-all">${allCollapsed ? 'Expand all' : 'Collapse all'}</button>
        <button class="btn" id="chargeback-download">${icon('file', 'btn-icon')}Download CSV</button>
      </div>
    </div>
    <div class="metric-strip">
      ${miniMetric('Cost', money(report.cost_usd))}
      ${miniMetric('Requests', compact(report.requests))}
      ${miniMetric('Tokens', compact(report.total_tokens))}
      ${miniMetric('Departments', report.departments.length)}
      ${miniMetric('Active users', users)}
    </div>
    ${chargebackTable(report)}
  `;
}

function chargebackTable(report) {
  if (!report.departments.length) return `<p>No usage recorded for ${esc(report.month)}.</p>`;
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Department / user</th><th>Requests</th><th>Input tokens</th><th>Output tokens</th><th>Total tokens</th><th>Cost</th><th>Budget</th><th>Share</th></tr></thead>
        <tbody>
          ${report.departments.map(dept => `
            <tr class="chargeback-dept" data-chargeback-dept="${attr(dept.department)}" title="Click to ${state.chargeback.collapsed[dept.department] ? 'show' : 'hide'} the user breakdown">
              <td><span class="chargeback-caret">${state.chargeback.collapsed[dept.department] ? '&#9656;' : '&#9662;'}</span>${esc(dept.department || '(no department)')} <span class="muted">· ${dept.users.length} user${dept.users.length === 1 ? '' : 's'}</span></td>
              <td>${compact(dept.requests)}</td>
              <td>${compact(dept.input_tokens)}</td>
              <td>${compact(dept.output_tokens)}</td>
              <td>${compact(dept.total_tokens)}</td>
              <td>${money(dept.cost_usd)}</td>
              <td>${dept.budget_usd > 0 ? `${money(dept.budget_usd)} <span class="muted">(${percent(dept.cost_usd / dept.budget_usd)} used)</span>` : '<span class="muted">—</span>'}</td>
              <td>${percent(report.cost_usd ? dept.cost_usd / report.cost_usd : 0)} <span class="muted">of total</span></td>
            </tr>
            ${state.chargeback.collapsed[dept.department] ? '' : dept.users.map(user => `
              <tr class="chargeback-user">
                <td class="chargeback-user-name">${esc(user.username || user.user_id || '(unattributed)')}</td>
                <td>${compact(user.requests)}</td>
                <td>${compact(user.input_tokens)}</td>
                <td>${compact(user.output_tokens)}</td>
                <td>${compact(user.total_tokens)}</td>
                <td>${money(user.cost_usd)}</td>
                <td></td>
                <td>${percent(dept.cost_usd ? user.cost_usd / dept.cost_usd : 0)} <span class="muted">of dept</span></td>
              </tr>
            `).join('')}
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function budgetBurnDownView() {
  const rows = state.budgetBurnDown || [];
  if (!rows.length) return '<p>No budgets yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Scope</th><th>Spend</th><th>Progress</th><th>Remaining</th><th>Projected month-end</th><th>Daily avg</th><th>Days left</th><th>Status</th></tr></thead>
        <tbody>
          ${rows.map(item => {
            const b = item.budget || {};
            const ratio = Number(item.ratio || 0);
            const projectedRatio = Number(item.projected_ratio || 0);
            return `
              <tr>
                <td><span class="mono">${esc(b.scope_type || '')}</span> ${esc(b.scope_value || '')}</td>
                <td>${money(item.spend_usd)} / ${money(b.limit_usd)}</td>
                <td>${progressBar(ratio, `${percent(ratio)} used`)}</td>
                <td>${money(item.remaining_usd)}</td>
                <td>${money(item.projected_month_end_usd)} <span class="muted">(${percent(projectedRatio)})</span></td>
                <td>${money(item.daily_average_usd)}</td>
                <td>${Number(item.days_remaining || 0)}</td>
                <td>${budgetStatePill(item)}</td>
              </tr>
            `;
          }).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function progressBar(ratio, label) {
  const pct = Math.max(0, Math.min(100, Number(ratio || 0) * 100));
  return `<div class="progress" title="${esc(label)}"><div class="progress-fill" style="width:${pct}%"></div></div><span class="progress-label">${esc(label)}</span>`;
}

function budgetStatePill(item) {
  if (item.blocked) return '<span class="pill off">blocked</span>';
  if (item.warning) return '<span class="pill">warning</span>';
  return '<span class="pill on">ok</span>';
}

function budgetRows() {
  if (!state.budgets.length) return '<p>No records yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Scope</th><th>Scope value</th><th>Limit</th><th>Warn</th><th>Active</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.budgets.map(b => `
            <tr data-budget-row="${esc(b.id)}">
              <td><select data-budget-field="scope_type">${option('department', 'Department', b.scope_type)}${option('user', 'User', b.scope_type)}</select></td>
              <td><input data-budget-field="scope_value" value="${attr(b.scope_value)}" list="budget-values" /></td>
              <td><input data-budget-field="limit_usd" type="number" min="0" step="0.01" value="${attr(b.limit_usd)}" /></td>
              <td><input data-budget-field="warn_pct" type="number" min="1" max="100" step="1" value="${attr(b.warn_pct)}" /></td>
              <td><input data-budget-field="is_active" type="checkbox" ${b.is_active ? 'checked' : ''} /></td>
              <td><div class="actions"><button class="btn" data-save-budget="${esc(b.id)}">Save</button><button class="btn danger" data-delete-budget="${esc(b.id)}">Delete</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function rateLimitRows() {
  if (!state.rateLimits.length) return '<p>No records yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Scope</th><th>Scope value</th><th>RPM</th><th>TPM</th><th>Active</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.rateLimits.map(rl => `
            <tr data-rate-limit-row="${esc(rl.id)}">
              <td><select data-rate-limit-field="scope_type">${option('user', 'User', rl.scope_type)}${option('department', 'Department', rl.scope_type)}${option('provider', 'Provider', rl.scope_type)}${option('model', 'Model', rl.scope_type)}</select></td>
              <td><input data-rate-limit-field="scope_value" value="${attr(rl.scope_value)}" list="rate-limit-values" /></td>
              <td><input data-rate-limit-field="rpm_limit" type="number" min="0" step="1" value="${attr(rl.rpm_limit)}" /></td>
              <td><input data-rate-limit-field="tpm_limit" type="number" min="0" step="1" value="${attr(rl.tpm_limit)}" /></td>
              <td><input data-rate-limit-field="is_active" type="checkbox" ${rl.is_active ? 'checked' : ''} /></td>
              <td><div class="actions"><button class="btn" data-save-rate-limit="${esc(rl.id)}">Save</button><button class="btn danger" data-delete-rate-limit="${esc(rl.id)}">Delete</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function guardrailPolicyView() {
  const p = { ...defaultGuardrailPolicy(), ...(state.guardrailPolicy || {}) };
  const preview = state.guardrailPreview;
  return `
    <div class="guardrail-form">
      <div class="guardrail-toolbar">
        <label class="guardrail-toggle"><input id="guard-enabled" type="checkbox" ${p.enabled ? 'checked' : ''} /> <span>Enabled</span></label>
        <button class="btn primary" id="save-guardrails">${icon('check', 'btn-icon')}Save policy</button>
      </div>
      <div class="guardrail-policy-grid">
        <label class="form-field"><span>Input action</span><select id="guard-input-action">${guardrailActionOptions(p.input_action)}</select></label>
        <label class="form-field"><span>Output action</span><select id="guard-output-action">${guardrailActionOptions(p.output_action)}</select><small class="field-help">Output block rejects streaming requests and blocks non-streaming responses after provider return.</small></label>
        <label class="form-field guardrail-redaction"><span>Redaction text</span><input id="guard-redaction-text" value="${attr(p.redaction_text || '[REDACTED]')}" /></label>
      </div>
      ${guardrailBuiltInPatterns(p)}
      ${guardrailCustomPatterns(p)}
      ${guardrailPreviewView(preview)}
    </div>
    <div class="metric-strip guardrail-summary">
      ${miniMetric('Policy', p.enabled ? 'enabled' : 'disabled')}
      ${miniMetric('Input', p.input_action || 'redact')}
      ${miniMetric('Output', p.output_action || 'redact')}
      ${miniMetric('Custom patterns', (p.custom_patterns || []).filter(row => row.enabled && row.pattern).length)}
      ${miniMetric('Streaming block', p.output_action === 'block' ? 'rejected' : 'allowed')}
    </div>
  `;
}

function guardrailBuiltInPatterns(p) {
  return `
    <div class="guardrail-pattern-section">
      <div class="guardrail-section-heading">
        <div>
          <div class="guardrail-group-title">Built-in patterns</div>
          <p>Email, phone, SSN, credit card, and API key detectors. Info icons show the active regex.</p>
        </div>
      </div>
      <div class="guardrail-pattern-list">
        ${BUILTIN_GUARDRAIL_PATTERNS.map(pattern => `
          <div class="guardrail-pattern-row">
            <label class="check guardrail-pattern-name">
              <input id="${attr(pattern.checkboxId)}" type="checkbox" ${p[pattern.field] ? 'checked' : ''} />
              <span>${esc(pattern.label)}</span>
            </label>
            <span class="pattern-info" tabindex="0" aria-label="${attr(`${pattern.label} regex`)}">
              ${icon('info', 'info-icon')}
              <span class="pattern-card"><strong>${esc(pattern.label)}</strong><code>${esc(pattern.regex)}</code></span>
            </span>
            <span class="pattern-token mono">${esc(pattern.token)}</span>
            <span class="pill">global ${esc(p.input_action || 'redact')}/${esc(p.output_action || 'redact')}</span>
          </div>
        `).join('')}
      </div>
    </div>
  `;
}

function guardrailCustomPatterns(p) {
  const patterns = p.custom_patterns || [];
  return `
    <div class="guardrail-pattern-section">
      <div class="guardrail-section-heading">
        <div>
          <div class="guardrail-group-title">Custom patterns</div>
          <p>Add RE2-compatible regular expressions for organization-specific identifiers, secrets, or data classes.</p>
        </div>
        <button class="btn" id="add-custom-guardrail">${icon('plus', 'btn-icon')}Add pattern</button>
      </div>
      <div class="guardrail-custom-list">
        ${patterns.length ? patterns.map((pattern, index) => guardrailCustomPatternRow(pattern, index)).join('') : '<p>No custom patterns yet.</p>'}
      </div>
    </div>
  `;
}

function guardrailCustomPatternRow(pattern, index) {
  return `
    <div class="guardrail-custom-row" data-guardrail-custom-row="${index}">
      <input data-guardrail-custom-field="id" type="hidden" value="${attr(pattern.id || `custom-${index + 1}`)}" />
      <label class="check guardrail-custom-enabled"><input data-guardrail-custom-field="enabled" type="checkbox" ${pattern.enabled !== false ? 'checked' : ''} /> Enabled</label>
      <label class="form-field guardrail-custom-name"><span>Name</span><input data-guardrail-custom-field="name" value="${attr(pattern.name || '')}" placeholder="Employee ID" /></label>
      <label class="form-field guardrail-custom-pattern"><span>Regex</span><input data-guardrail-custom-field="pattern" value="${attr(pattern.pattern || '')}" placeholder="EMP-[0-9]+" /></label>
      <label class="form-field guardrail-custom-action"><span>Action</span><select data-guardrail-custom-field="action">${option('redact', 'Redact', pattern.action || 'redact')}${option('block', 'Block', pattern.action || 'redact')}</select></label>
      <label class="form-field guardrail-custom-redaction"><span>Replacement</span><input data-guardrail-custom-field="redaction_text" value="${attr(pattern.redaction_text || '')}" placeholder="[REDACTED]" /></label>
      <button class="btn danger guardrail-custom-remove" data-remove-custom-guardrail="${index}">Remove</button>
    </div>
  `;
}

function guardrailPreviewView(preview) {
  const output = preview ? (preview.blocked ? 'Blocked by policy. Matching patterns: ' + (preview.findings || []).join(', ') : preview.output) : '';
  return `
    <div class="guardrail-preview">
      <div class="guardrail-section-heading">
        <div>
          <div class="guardrail-group-title">Test patterns</div>
          <p>Preview redaction or blocking against sample text. Samples are sent only to the local admin API and are not stored.</p>
        </div>
        <div class="actions">
          <select id="guardrail-preview-phase">${option('input', 'Input policy', preview?.phase || 'input')}${option('output', 'Output policy', preview?.phase || 'input')}</select>
          <button class="btn" id="test-guardrails">${icon('check', 'btn-icon')}Preview</button>
        </div>
      </div>
      <textarea id="guardrail-preview-text" rows="3" placeholder="Paste sample text to test...">${esc(state.guardrailPreviewText || '')}</textarea>
      <div class="guardrail-preview-result ${preview?.blocked ? 'blocked' : ''}">
        ${preview ? `
          <div class="guardrail-preview-meta">
            <span class="pill ${preview.blocked ? 'off' : preview.redacted ? 'on' : ''}">${preview.blocked ? 'blocked' : preview.redacted ? 'redacted' : 'no match'}</span>
            <span>${esc((preview.findings || []).join(', ') || 'No findings')}</span>
          </div>
          <pre>${esc(output)}</pre>
        ` : '<span class="muted">Run preview to see what the provider or client would receive.</span>'}
      </div>
    </div>
  `;
}

function guardrailActionOptions(selected) {
  return [option('off', 'Off', selected), option('redact', 'Redact', selected), option('block', 'Block', selected)].join('');
}

function requestLogSearchView() {
  const f = state.requestFilters || {};
  return `
    <div class="form-grid request-filter-grid">
      <label class="form-field"><span>Search</span><input id="request-q" value="${attr(f.q || '')}" placeholder="request id, user, key, provider, model, error" /></label>
      <label class="form-field"><span>Days</span><input id="request-days" type="number" min="1" max="365" step="1" value="${attr(f.days || '7')}" /></label>
      <label class="form-field"><span>Status</span><select id="request-status">${option('any', 'Any', f.status || 'any')}${option('success', 'Success', f.status)}${option('error', 'Error', f.status)}${option('4xx', '4xx', f.status)}${option('5xx', '5xx', f.status)}</select></label>
      <label class="form-field"><span>Protocol</span><select id="request-protocol">${option('', 'Any', f.protocol)}${option('openai', 'OpenAI', f.protocol)}${option('anthropic', 'Anthropic', f.protocol)}${option('bedrock', 'Bedrock', f.protocol)}</select></label>
      <label class="form-field"><span>Streaming</span><select id="request-streaming">${option('', 'Any', f.streaming)}${option('true', 'Streaming', f.streaming)}${option('false', 'Non-streaming', f.streaming)}</select></label>
      <label class="form-field"><span>Provider</span><select id="request-provider">${option('', 'Any', f.provider_id)}${state.providers.map(p => option(p.id, p.id, f.provider_id)).join('')}</select></label>
      <label class="form-field"><span>Model route</span><select id="request-model">${option('', 'Any', f.model)}${state.adminModels.map(m => option(m.route, m.route, f.model)).join('')}</select></label>
      <label class="form-field"><span>Department</span><input id="request-department" value="${attr(f.department || '')}" list="request-departments" placeholder="Department" /></label>
      <datalist id="request-departments">${[...new Set(state.users.map(u => u.department).filter(Boolean))].map(d => `<option value="${attr(d)}"></option>`).join('')}</datalist>
      <button class="btn primary" id="request-apply">${icon('check', 'btn-icon')}Apply</button>
      <button class="btn" id="request-reset">Reset</button>
      <button class="btn" id="request-export">Export CSV</button>
    </div>
  `;
}

function requestLogRows() {
  const result = state.requestLog || { items: [], total: 0, limit: 100, offset: 0 };
  const rows = result.items || [];
  const start = rows.length ? Number(result.offset || 0) + 1 : 0;
  const end = Number(result.offset || 0) + rows.length;
  const total = Number(result.total || 0);
  const pager = `
    <div class="pager">
      <span>${compact(total)} matching requests · showing ${compact(start)}-${compact(end)}</span>
      <div class="actions">
        <button class="btn" id="request-prev" ${Number(result.offset || 0) <= 0 ? 'disabled' : ''}>Previous</button>
        <button class="btn" id="request-next" ${end >= total ? 'disabled' : ''}>Next</button>
      </div>
    </div>
  `;
  if (!rows.length) return `${pager}<p>No request metadata matches the current filters.</p>`;
  return `
    ${pager}
    <div class="table-scroll">
      <table>
        <thead><tr><th>Time</th><th>Request</th><th>User</th><th>Department</th><th>Key</th><th>Provider</th><th>Model</th><th>Protocol</th><th>Endpoint</th><th>Status</th><th>Stream</th><th>Tokens</th><th>Cost</th><th>Latency</th><th>Error</th><th>Client IP</th></tr></thead>
        <tbody>
          ${rows.map(item => `
            <tr>
              <td>${fmt(item.created_at)}</td>
              <td class="mono">${esc(item.request_id)}</td>
              <td class="mono">${esc(item.username || item.user_id || '')}</td>
              <td>${esc(item.department || '')}</td>
              <td><span class="mono">${esc(item.api_key_prefix || item.api_key_id || '')}</span> ${esc(item.api_key_name || '')}</td>
              <td><span class="mono">${esc(item.provider_id || '')}</span> <span class="muted">${esc(item.provider_type || '')}</span></td>
              <td><span class="mono">${esc(item.model_route || '')}</span><br><span class="muted">${esc(item.upstream_model_id || '')}</span></td>
              <td class="mono">${esc(item.protocol || '')}</td>
              <td class="mono">${esc(item.method || '')} ${esc(item.endpoint || '')}</td>
              <td>${statusCodePill(item.status_code, item.error_text)}</td>
              <td>${pill(!!item.streaming)}</td>
              <td>${compact(item.total_tokens || 0)} <span class="muted">${compact(item.input_tokens || 0)}/${compact(item.output_tokens || 0)}</span></td>
              <td>${money(item.cost_usd || 0)}</td>
              <td>${compact(item.latency_ms || 0)} ms</td>
              <td class="wrap">${esc(item.error_text || '')}</td>
              <td class="mono">${esc(item.client_ip || '')}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function auditLogRows() {
  if (!state.auditLogs.length) return '<p>No audit events yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Target</th><th>Details</th><th>IP</th></tr></thead>
        <tbody>
          ${state.auditLogs.map(item => `
            <tr>
              <td>${fmt(item.created_at)}</td>
              <td class="mono">${esc(item.actor_username || item.actor_user_id || '')}</td>
              <td class="mono">${esc(item.action)}</td>
              <td><span class="mono">${esc(item.target_type)}</span> ${esc(item.target_display || item.target_id || '')}</td>
              <td class="wrap">${esc(auditDetails(item.details))}</td>
              <td class="mono">${esc(item.ip_address || '')}</td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function afterRender() {
  wireCharts();
  document.querySelectorAll('[data-theme-id]').forEach((btn) => {
    btn.onclick = () => {
      state.theme = applyTheme(btn.dataset.themeId);
      render();
    };
  });
  document.querySelectorAll('[data-admin-tab]').forEach((btn) => {
    btn.onclick = () => {
      state.adminTab = btn.dataset.adminTab;
      render();
    };
  });
  const create = document.getElementById('create-key');
  if (create) {
    create.onclick = async () => {
      const name = document.getElementById('key-name').value || 'API key';
      const expires_at = document.getElementById('key-expires').value.trim();
      const resp = await api('/api/api-keys', { method: 'POST', body: JSON.stringify({ name, expires_at }) });
      state.secret = resp.key;
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-self-key]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-self-key-row]');
      const body = collectFields(row, 'self-key');
      await api(`/api/api-keys/${encodeURIComponent(btn.dataset.saveSelfKey)}`, { method: 'PATCH', body: JSON.stringify(body) });
      state.notice = 'API key saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-rotate]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Rotate API key ${btn.dataset.rotate}? The old secret will stop working immediately.`)) return;
      const resp = await api(`/api/api-keys/${encodeURIComponent(btn.dataset.rotate)}/rotate`, { method: 'POST', body: '{}' });
      state.secret = resp.key;
      state.notice = 'API key rotated.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-revoke]').forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/api-keys/${btn.dataset.revoke}`, { method: 'DELETE' });
      await refresh();
    };
  });
  const playgroundSend = document.getElementById('playground-send');
  if (playgroundSend) {
    const pg = state.playground;
    const routeSelect = document.getElementById('playground-route');
    const systemInput = document.getElementById('playground-system');
    const maxTokensInput = document.getElementById('playground-max-tokens');
    const input = document.getElementById('playground-input');
    routeSelect.onchange = () => { pg.route = routeSelect.value; };
    systemInput.oninput = () => { pg.system = systemInput.value; };
    maxTokensInput.oninput = () => { pg.maxTokens = maxTokensInput.value; };
    input.oninput = () => { pg.draft = input.value; };
    input.onkeydown = (e) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        sendPlaygroundMessage();
      }
    };
    playgroundSend.onclick = sendPlaygroundMessage;
    document.getElementById('playground-clear').onclick = () => {
      pg.messages = [];
      pg.draft = '';
      render();
    };
    const transcript = document.getElementById('playground-transcript');
    if (transcript) transcript.scrollTop = transcript.scrollHeight;
    if (!pg.busy) input.focus({ preventScroll: true });
  }
  const addProviderForm = document.getElementById('add-provider-form');
  if (addProviderForm) {
    const syncAddProviderForm = () => {
      syncProviderGroups(addProviderForm, val('provider-type'), val('provider-aws-auth') || 'chain');
    };
    document.getElementById('provider-type').onchange = syncAddProviderForm;
    document.getElementById('provider-aws-auth').onchange = syncAddProviderForm;
    syncAddProviderForm();
  }
  document.querySelectorAll('[data-provider-row]').forEach((row) => {
    const typeSelect = row.querySelector('[data-provider-field="type"]');
    const authSelect = row.querySelector('[data-provider-field="aws_auth_method"]');
    const syncRow = () => {
      syncProviderGroups(row, typeSelect?.value, authSelect?.value || 'chain');
    };
    if (typeSelect) typeSelect.onchange = syncRow;
    if (authSelect) authSelect.onchange = syncRow;
    syncRow();
  });
  const createProvider = document.getElementById('create-provider');
  if (createProvider) {
    createProvider.onclick = async () => {
      await api('/api/admin/providers', { method: 'POST', body: JSON.stringify({
        id: val('provider-id'),
        name: val('provider-name'),
        type: val('provider-type'),
        base_url: val('provider-base-url'),
        api_key_env: val('provider-api-key-env'),
        api_key: val('provider-api-key'),
        azure_api_version: val('provider-azure-api-version'),
        aws_region: val('provider-aws-region'),
        aws_auth_method: val('provider-aws-auth') || 'chain',
        aws_access_key_id: val('provider-aws-access-key'),
        aws_secret_access_key: val('provider-aws-secret-key'),
        aws_session_token: val('provider-aws-session-token'),
        bedrock_api_key: val('provider-bedrock-api-key'),
        enabled: checked('provider-enabled')
      })});
      state.notice = 'Provider added.';
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-provider]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-provider-row]');
      const id = btn.dataset.saveProvider;
      const body = collectFields(row, 'provider');
      body.id = id;
      await api(`/api/admin/providers/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) });
      state.notice = 'Provider saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-delete-provider]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Delete provider ${btn.dataset.deleteProvider}? Models under it will also be removed.`)) return;
      await api(`/api/admin/providers/${encodeURIComponent(btn.dataset.deleteProvider)}`, { method: 'DELETE' });
      state.notice = 'Provider deleted.';
      await refresh();
    };
  });
  const createModel = document.getElementById('create-model');
  if (createModel) {
    createModel.onclick = async () => {
      await api('/api/admin/models', { method: 'POST', body: JSON.stringify({
        provider_id: val('model-provider'),
        model_id: val('model-model-id'),
        route: val('model-route'),
        display_name: val('model-display-name'),
        input_cost_per_million: num('model-input-cost'),
        output_cost_per_million: num('model-output-cost'),
        context_window: intNum('model-context'),
        fallback_routes: val('model-fallback-routes'),
        weighted_routes: val('model-weighted-routes'),
        retry_attempts: intNum('model-retry-attempts'),
        request_timeout_ms: intNum('model-timeout-ms'),
        health_routing_enabled: checked('model-health-routing'),
        supports_streaming: checked('model-streaming'),
        enabled: checked('model-enabled')
      })});
      state.notice = 'Model added.';
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-model]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-model-row]');
      const id = btn.dataset.saveModel;
      const body = collectFields(row, 'model');
      body.id = id;
      body.input_cost_per_million = Number(body.input_cost_per_million || 0);
      body.output_cost_per_million = Number(body.output_cost_per_million || 0);
      body.context_window = Number.parseInt(body.context_window || '0', 10);
      body.retry_attempts = Number.parseInt(body.retry_attempts || '0', 10);
      body.request_timeout_ms = Number.parseInt(body.request_timeout_ms || '0', 10);
      await api(`/api/admin/models/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) });
      state.notice = 'Model pricing saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-test-model]').forEach((btn) => {
    btn.onclick = async () => {
      state.notice = 'Testing model...';
      render();
      try {
        const result = await api(`/api/admin/models/${encodeURIComponent(btn.dataset.testModel)}/test`, { method: 'POST', body: '{}' });
        state.notice = `Model test ${result.ok ? 'passed' : 'failed'} in ${result.latency_ms || 0}ms (${result.status_code || 'n/a'}).`;
      } catch (err) {
        state.notice = '';
        state.error = `Model test failed: ${err.message}`;
      }
      render();
    };
  });
  document.querySelectorAll('[data-delete-model]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Delete model ${btn.dataset.deleteModel}?`)) return;
      await api(`/api/admin/models/${encodeURIComponent(btn.dataset.deleteModel)}`, { method: 'DELETE' });
      state.notice = 'Model deleted.';
      await refresh();
    };
  });
  const createUser = document.getElementById('create-user');
  if (createUser) {
    createUser.onclick = async () => {
      await api('/api/admin/users', { method: 'POST', body: JSON.stringify({
        username: val('user-username'),
        password: val('user-password'),
        email: val('user-email'),
        display_name: val('user-display'),
        department: val('user-department'),
        role: val('user-role')
      })});
      state.notice = 'User created.';
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-user]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-user-row]');
      const id = btn.dataset.saveUser;
      const body = collectFields(row, 'user');
      await api(`/api/admin/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) });
      state.notice = 'User saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-reset-user]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-user-row]');
      const password = row.querySelector('[data-reset-password]').value.trim();
      if (!password) {
        state.error = 'Enter a new password before resetting.';
        render();
        return;
      }
      await api(`/api/admin/users/${encodeURIComponent(btn.dataset.resetUser)}/reset-password`, { method: 'POST', body: JSON.stringify({ password }) });
      state.notice = 'Password reset.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-delete-user]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Delete user ${btn.dataset.deleteUser}? API keys will be revoked and usage ledger rows will remain for chargeback.`)) return;
      await api(`/api/admin/users/${encodeURIComponent(btn.dataset.deleteUser)}`, { method: 'DELETE' });
      state.notice = 'User deleted.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-save-key]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-key-row]');
      const body = collectFields(row, 'key');
      body.budget_usd = Number(body.budget_usd || 0);
      body.rpm_limit = Number.parseInt(body.rpm_limit || '0', 10);
      body.tpm_limit = Number.parseInt(body.tpm_limit || '0', 10);
      await api(`/api/admin/api-keys/${encodeURIComponent(btn.dataset.saveKey)}`, { method: 'PATCH', body: JSON.stringify(body) });
      state.notice = 'API key controls saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-rotate-admin-key]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Rotate API key ${btn.dataset.rotateAdminKey}? The old secret will stop working immediately.`)) return;
      const resp = await api(`/api/admin/api-keys/${encodeURIComponent(btn.dataset.rotateAdminKey)}/rotate`, { method: 'POST', body: '{}' });
      state.secret = resp.key;
      state.notice = 'API key rotated.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-revoke-admin-key]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Revoke API key ${btn.dataset.revokeAdminKey}?`)) return;
      await api(`/api/admin/api-keys/${encodeURIComponent(btn.dataset.revokeAdminKey)}`, { method: 'DELETE' });
      state.notice = 'API key revoked.';
      await refresh();
    };
  });
  const createRateLimit = document.getElementById('create-rate-limit');
  if (createRateLimit) {
    createRateLimit.onclick = async () => {
      await api('/api/admin/rate-limits', { method: 'POST', body: JSON.stringify({
        scope_type: val('rate-scope-type'),
        scope_value: val('rate-scope-value'),
        rpm_limit: intNum('rate-rpm'),
        tpm_limit: intNum('rate-tpm')
      })});
      state.notice = 'Rate limit created.';
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-rate-limit]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-rate-limit-row]');
      const body = collectFields(row, 'rate-limit');
      body.rpm_limit = Number.parseInt(body.rpm_limit || '0', 10);
      body.tpm_limit = Number.parseInt(body.tpm_limit || '0', 10);
      await api(`/api/admin/rate-limits/${encodeURIComponent(btn.dataset.saveRateLimit)}`, { method: 'PATCH', body: JSON.stringify(body) });
      state.notice = 'Rate limit saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-delete-rate-limit]').forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/admin/rate-limits/${encodeURIComponent(btn.dataset.deleteRateLimit)}`, { method: 'DELETE' });
      state.notice = 'Rate limit deleted.';
      await refresh();
    };
  });
  const createBudget = document.getElementById('create-budget');
  if (createBudget) {
    createBudget.onclick = async () => {
      await api('/api/admin/budgets', { method: 'POST', body: JSON.stringify({
        scope_type: val('budget-scope-type'),
        scope_value: val('budget-scope-value'),
        limit_usd: num('budget-limit'),
        warn_pct: num('budget-warn') || 90
      })});
      state.notice = 'Budget created.';
      await refresh();
    };
  }
  document.querySelectorAll('[data-save-budget]').forEach((btn) => {
    btn.onclick = async () => {
      const row = btn.closest('[data-budget-row]');
      const body = collectFields(row, 'budget');
      body.limit_usd = Number(body.limit_usd || 0);
      body.warn_pct = Number(body.warn_pct || 90);
      await api(`/api/admin/budgets/${encodeURIComponent(btn.dataset.saveBudget)}`, { method: 'PATCH', body: JSON.stringify(body) });
      state.notice = 'Budget saved.';
      await refresh();
    };
  });
  document.querySelectorAll('[data-delete-budget]').forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/admin/budgets/${encodeURIComponent(btn.dataset.deleteBudget)}`, { method: 'DELETE' });
      state.notice = 'Budget deleted.';
      await refresh();
    };
  });
  const saveGuardrails = document.getElementById('save-guardrails');
  if (saveGuardrails) {
    saveGuardrails.onclick = async () => {
      const body = collectGuardrailPolicyBody();
      state.guardrailPolicy = await api('/api/admin/guardrails', { method: 'PUT', body: JSON.stringify(body) });
      state.guardrailPreview = null;
      state.notice = 'Guardrail policy saved.';
      await refresh();
    };
  }
  const addCustomGuardrail = document.getElementById('add-custom-guardrail');
  if (addCustomGuardrail) {
    addCustomGuardrail.onclick = () => {
      const body = collectGuardrailPolicyBody();
      const nextIndex = (body.custom_patterns || []).length + 1;
      body.custom_patterns = [...(body.custom_patterns || []), {
        id: `custom-${Date.now()}`,
        name: `Custom pattern ${nextIndex}`,
        pattern: '',
        action: 'redact',
        redaction_text: '',
        enabled: true
      }];
      state.guardrailPolicy = { ...(state.guardrailPolicy || defaultGuardrailPolicy()), ...body };
      state.guardrailPreview = null;
      render();
    };
  }
  document.querySelectorAll('[data-remove-custom-guardrail]').forEach((btn) => {
    btn.onclick = () => {
      const body = collectGuardrailPolicyBody();
      const index = Number(btn.dataset.removeCustomGuardrail);
      body.custom_patterns = (body.custom_patterns || []).filter((_, i) => i !== index);
      state.guardrailPolicy = { ...(state.guardrailPolicy || defaultGuardrailPolicy()), ...body };
      state.guardrailPreview = null;
      render();
    };
  });
  const testGuardrails = document.getElementById('test-guardrails');
  if (testGuardrails) {
    testGuardrails.onclick = async () => {
      const body = collectGuardrailPolicyBody();
      const text = rawVal('guardrail-preview-text');
      state.guardrailPreviewText = text;
      state.guardrailPolicy = { ...(state.guardrailPolicy || defaultGuardrailPolicy()), ...body };
      state.guardrailPreview = await api('/api/admin/guardrails/test', {
        method: 'POST',
        body: JSON.stringify({ policy: body, text, phase: val('guardrail-preview-phase') || 'input' })
      });
      render();
    };
  }
  const requestApply = document.getElementById('request-apply');
  if (requestApply) {
    requestApply.onclick = async () => {
      state.requestFilters = {
        q: val('request-q'),
        days: val('request-days') || '7',
        status: val('request-status') || 'any',
        protocol: val('request-protocol'),
        provider_id: val('request-provider'),
        model: val('request-model'),
        department: val('request-department'),
        streaming: val('request-streaming')
      };
      state.requestLog = { ...(state.requestLog || {}), offset: 0, limit: 100 };
      await refresh();
    };
  }
  const requestReset = document.getElementById('request-reset');
  if (requestReset) {
    requestReset.onclick = async () => {
      state.requestFilters = { q: '', days: '7', status: 'any', protocol: '', provider_id: '', model: '', department: '', streaming: '' };
      state.requestLog = { items: [], total: 0, limit: 100, offset: 0 };
      await refresh();
    };
  }
  const requestPrev = document.getElementById('request-prev');
  if (requestPrev) {
    requestPrev.onclick = async () => {
      const current = state.requestLog || { offset: 0, limit: 100 };
      state.requestLog = { ...current, offset: Math.max(0, Number(current.offset || 0) - Number(current.limit || 100)) };
      await refresh();
    };
  }
  const requestNext = document.getElementById('request-next');
  if (requestNext) {
    requestNext.onclick = async () => {
      const current = state.requestLog || { offset: 0, limit: 100 };
      state.requestLog = { ...current, offset: Number(current.offset || 0) + Number(current.limit || 100) };
      await refresh();
    };
  }
  const requestExport = document.getElementById('request-export');
  if (requestExport) {
    requestExport.onclick = async () => {
      const params = requestLogParams(false);
      const url = `/api/admin/request-log/export.csv${params.toString() ? `?${params}` : ''}`;
      const res = await fetch(url, { headers: { Authorization: `Bearer ${state.token}` } });
      if (!res.ok) throw new Error(`Request log export failed: ${res.status}`);
      const blob = await res.blob();
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = objectUrl;
      link.download = `phlox-gw-request-log-${new Date().toISOString().slice(0, 10)}.csv`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(objectUrl);
    };
  }
  const configExport = document.getElementById('config-export');
  if (configExport) {
    configExport.onclick = async () => {
      const res = await fetch('/api/admin/config/export', { headers: { Authorization: `Bearer ${state.token}` } });
      if (!res.ok) throw new Error(`Configuration export failed: ${res.status}`);
      const blob = await res.blob();
      const objectUrl = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = objectUrl;
      link.download = `phlox-gw-admin-config-${new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)}.json`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(objectUrl);
      state.notice = 'Signed configuration export downloaded.';
      render();
    };
  }
  const chargebackMonth = document.getElementById('chargeback-month');
  if (chargebackMonth) {
    chargebackMonth.onchange = () => loadChargeback(chargebackMonth.value);
    document.getElementById('chargeback-download').onclick = async () => {
      const month = state.chargeback.month || currentMonthKey();
      const res = await fetch(`/api/admin/chargeback/export.csv?month=${encodeURIComponent(month)}`, { headers: { Authorization: `Bearer ${state.token}` } });
      if (!res.ok) {
        state.error = `Chargeback export failed: ${res.status}`;
        render();
        return;
      }
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `phlox-gw-chargeback-${month}.csv`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
    };
  }
  const chargebackRetry = document.getElementById('chargeback-retry');
  if (chargebackRetry) {
    chargebackRetry.onclick = () => loadChargeback(state.chargeback.month || currentMonthKey());
  }
  document.querySelectorAll('[data-chargeback-dept]').forEach((row) => {
    row.onclick = () => {
      const dept = row.dataset.chargebackDept;
      state.chargeback.collapsed[dept] = !state.chargeback.collapsed[dept];
      render();
    };
  });
  const chargebackToggleAll = document.getElementById('chargeback-toggle-all');
  if (chargebackToggleAll) {
    chargebackToggleAll.onclick = () => {
      const departments = state.chargeback.report?.departments || [];
      const allCollapsed = departments.length > 0 && departments.every(d => state.chargeback.collapsed[d.department]);
      state.chargeback.collapsed = {};
      if (!allCollapsed) {
        departments.forEach(d => { state.chargeback.collapsed[d.department] = true; });
      }
      render();
    };
  }
  const csvExport = document.getElementById('csv-export');
  if (csvExport) {
    csvExport.onclick = async () => {
      const res = await fetch('/api/admin/usage/export.csv', { headers: { Authorization: `Bearer ${state.token}` } });
      if (!res.ok) throw new Error(`CSV export failed: ${res.status}`);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `phlox-gw-usage-${new Date().toISOString().slice(0, 10)}.csv`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
    };
  }
}

const oldRender = render;
render = function () {
  oldRender();
  afterRender();
};

function icon(name, className = 'icon') {
  const paths = {
    grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
    key: '<path d="M15 7.5a5 5 0 1 0-4.1 4.9L13 14.5V17h2.5v2.5H18V22h3v-3.2l-6-6"/><circle cx="7.5" cy="7.5" r="1.2"/>',
    cpu: '<rect x="7" y="7" width="10" height="10" rx="2"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M1 9h3M1 15h3M20 9h3M20 15h3"/>',
    chart: '<path d="M4 19V5"/><path d="M4 19h16"/><path d="M8 16v-5"/><path d="M12 16V8"/><path d="M16 16v-9"/>',
    network: '<rect x="9" y="2" width="6" height="6" rx="1.5"/><rect x="3" y="16" width="6" height="6" rx="1.5"/><rect x="15" y="16" width="6" height="6" rx="1.5"/><path d="M12 8v4"/><path d="M6 16v-2a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2"/>',
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z"/><path d="M9 12l2 2 4-5"/>',
    server: '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01"/>',
    users: '<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.9"/><path d="M16 3.1a4 4 0 0 1 0 7.8"/>',
    wallet: '<path d="M3 7a3 3 0 0 1 3-3h14v16H6a3 3 0 0 1-3-3V7Z"/><path d="M3 7h17"/><path d="M16 13h4v4h-4a2 2 0 0 1 0-4Z"/>',
    gauge: '<path d="M4 14a8 8 0 1 1 16 0"/><path d="M12 14l4-4"/><path d="M5 20h14"/><path d="M7 14h.01M17 14h.01"/>',
    file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="M8 13h8M8 17h8"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    palette: '<circle cx="13.5" cy="6.5" r=".5"/><circle cx="17.5" cy="10.5" r=".5"/><circle cx="8.5" cy="7.5" r=".5"/><circle cx="6.5" cy="12.5" r=".5"/><path d="M12 2a10 10 0 0 0 0 20h1.5a2.5 2.5 0 0 0 0-5H12a1.5 1.5 0 0 1 0-3h2a8 8 0 0 0 0-16h-2Z"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
    check: '<path d="M20 6 9 17l-5-5"/>',
    chat: '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2Z"/><path d="M8 9h8M8 13h5"/>'
  };
  return `<svg class="${className}" viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${paths[name] || paths.grid}</svg>`;
}

function card(label, value, sub) {
  return `<div class="card"><div class="label">${esc(label)}</div><div class="value">${esc(String(value))}</div><div class="sub">${esc(sub)}</div></div>`;
}

function table(headers, rows) {
  if (!rows.length) return `<p>No records yet.</p>`;
  return `<table><thead><tr>${headers.map(h => `<th>${esc(h)}</th>`).join('')}</tr></thead><tbody>${rows.map(row => `<tr>${row.map(c => `<td>${c}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
}

function usageTable(rows) {
  return table(['Model', 'Department', 'User', 'Requests', 'Tokens', 'Cost'], rows.map(r => [
    `<span class="mono">${esc(r.model)}</span>`, esc(r.department || ''), esc(r.username || ''), r.requests, r.total_tokens, money(r.cost_usd)
  ]));
}

function pill(on) {
  return `<span class="pill ${on ? 'on' : 'off'}">${on ? 'on' : 'off'}</span>`;
}

function statusPill(status) {
  const normalized = String(status || 'unknown').toLowerCase();
  const cls = normalized === 'healthy' ? 'on' : normalized === 'unknown' ? '' : 'off';
  return `<span class="pill ${cls}">${esc(normalized)}</span>`;
}

function statusCodePill(status, errorText) {
  const code = Number(status || 0);
  const hasError = code >= 400 || Boolean(errorText);
  const cls = hasError ? 'off' : code >= 200 && code < 400 ? 'on' : '';
  return `<span class="pill ${cls}">${code || 'n/a'}</span>`;
}

function keyStatusPill(k) {
  if (!k.is_active) return '<span class="pill off">revoked</span>';
  if (k.expires_at && new Date(k.expires_at).getTime() <= Date.now()) return '<span class="pill off">expired</span>';
  return '<span class="pill on">active</span>';
}

function money(v) {
  return `$${Number(v || 0).toFixed(4)}`;
}

function compact(v) {
  return Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(Number(v || 0));
}

function percent(v) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`;
}

function weightedAverage(rows, valueField, weightField) {
  const totalWeight = rows.reduce((sum, row) => sum + Number(row[weightField] || 0), 0);
  if (!totalWeight) return 0;
  const weighted = rows.reduce((sum, row) => sum + (Number(row[valueField] || 0) * Number(row[weightField] || 0)), 0);
  return weighted / totalWeight;
}

function fmt(v) {
  return v ? new Date(v).toLocaleString() : '';
}

function val(id) {
  return document.getElementById(id)?.value?.trim() || '';
}

function rawVal(id) {
  return document.getElementById(id)?.value || '';
}

function checked(id) {
  return Boolean(document.getElementById(id)?.checked);
}

function num(id) {
  return Number(val(id) || 0);
}

function intNum(id) {
  return Number.parseInt(val(id) || '0', 10);
}

function syncProviderGroups(scope, type, auth) {
  const show = (name, on) => scope.querySelectorAll(`[data-provider-group="${name}"]`).forEach((el) => el.classList.toggle('hidden', !on));
  const bedrock = type === 'bedrock';
  show('api', !bedrock);
  show('azure-openai', type === 'azure-openai');
  show('azure-openai-help', type === 'azure-openai');
  show('azure-anthropic-help', type === 'azure-anthropic');
  show('bedrock', bedrock);
  show('bedrock-chain', bedrock && auth === 'chain');
  show('bedrock-keys', bedrock && auth === 'keys');
  show('bedrock-api-key', bedrock && auth === 'api_key');
}

function collectFields(row, prefix) {
  const out = {};
  const datasetKey = prefix.replace(/-([a-z])/g, (_, c) => c.toUpperCase()) + 'Field';
  row.querySelectorAll(`[data-${prefix}-field]`).forEach((el) => {
    const key = el.dataset[datasetKey];
    out[key] = el.type === 'checkbox' ? el.checked : el.value.trim();
  });
  return out;
}

function collectGuardrailPolicyBody() {
  return {
    enabled: checked('guard-enabled'),
    input_action: val('guard-input-action') || 'redact',
    output_action: val('guard-output-action') || 'redact',
    detect_email: checked('guard-detect-email'),
    detect_phone: checked('guard-detect-phone'),
    detect_ssn: checked('guard-detect-ssn'),
    detect_credit_card: checked('guard-detect-credit-card'),
    detect_api_key: checked('guard-detect-api-key'),
    custom_patterns: collectGuardrailCustomPatterns(),
    redaction_text: val('guard-redaction-text') || '[REDACTED]',
    streaming_block_mode: 'reject'
  };
}

function collectGuardrailCustomPatterns() {
  return [...document.querySelectorAll('[data-guardrail-custom-row]')].map((row, index) => {
    const get = (field) => row.querySelector(`[data-guardrail-custom-field="${field}"]`);
    return {
      id: get('id')?.value?.trim() || `custom-${index + 1}`,
      name: get('name')?.value?.trim() || `Custom pattern ${index + 1}`,
      pattern: get('pattern')?.value?.trim() || '',
      action: get('action')?.value || 'redact',
      redaction_text: get('redaction_text')?.value?.trim() || '',
      enabled: Boolean(get('enabled')?.checked)
    };
  }).filter(pattern => pattern.pattern);
}

function option(value, label, selected) {
  return `<option value="${attr(value)}" ${value === selected ? 'selected' : ''}>${esc(label)}</option>`;
}

function attr(value) {
  return esc(value ?? '');
}

function budgetValueOptions() {
  const values = new Set();
  state.users.forEach(u => {
    if (u.department) values.add(u.department);
    values.add(u.id);
  });
  return [...values].map(v => `<option value="${attr(v)}"></option>`).join('');
}

function rateLimitValueOptions() {
  const values = new Set();
  state.users.forEach(u => {
    values.add(u.id);
    if (u.department) values.add(u.department);
  });
  state.providers.forEach(p => values.add(p.id));
  state.adminModels.forEach(m => values.add(m.route));
  return [...values].map(v => `<option value="${attr(v)}"></option>`).join('');
}

function defaultGuardrailPolicy() {
  return {
    id: 'default',
    enabled: false,
    input_action: 'redact',
    output_action: 'redact',
    detect_email: true,
    detect_phone: true,
    detect_ssn: true,
    detect_credit_card: true,
    detect_api_key: true,
    custom_patterns: [],
    redaction_text: '[REDACTED]',
    streaming_block_mode: 'reject'
  };
}

function budgetLabel(b) {
  if (b.scope_type === 'user') {
    const user = state.users.find(u => u.id === b.scope_value);
    return `user: ${esc(user ? `${user.username} (${b.scope_value})` : b.scope_value)}`;
  }
  return `department: ${esc(b.scope_value)}`;
}

function auditDetails(details) {
  if (!details) return '';
  try {
    const parsed = JSON.parse(details);
    return Object.entries(parsed).map(([key, value]) => `${key}: ${value === null ? '' : value}`).join(', ');
  } catch (_) {
    return details;
  }
}

function initialTheme() {
  try {
    return normalizedTheme(localStorage.getItem(THEME_STORAGE_KEY) || DEFAULT_THEME);
  } catch (_) {
    return DEFAULT_THEME;
  }
}

function normalizedTheme(id) {
  return THEMES.some(theme => theme.id === id) ? id : DEFAULT_THEME;
}

function applyTheme(id, persist = true) {
  const theme = normalizedTheme(id);
  document.documentElement.setAttribute('data-theme', theme);
  if (persist) {
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch (_) {}
  }
  return theme;
}

function titleForTab() {
  return { overview: 'Gateway overview', keys: 'API keys', models: 'Model catalog', usage: 'Usage and cost', appearance: 'Appearance', admin: 'Administration' }[state.tab] || 'Gateway';
}

function subtitleForTab() {
  return {
    overview: 'Provider-neutral access with cost and budget controls.',
    keys: 'Mint and revoke user-owned keys for SDK access.',
    models: 'Enabled model routes and administrator-assigned pricing.',
    usage: 'Per-user tokens, request counts, and chargeback cost.',
    appearance: 'Theme selection and local display preferences.',
    admin: 'Users, providers, budgets, and aggregate reporting.'
  }[state.tab] || '';
}

function logout() {
  state.token = '';
  state.user = null;
  localStorage.removeItem('phlox_gw_token');
  render();
}

function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
}

refresh();
