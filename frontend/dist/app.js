const state = {
  token: localStorage.getItem('phlox_gw_token') || '',
  user: null,
  tab: 'overview',
  health: null,
  models: [],
  providers: [],
  keys: [],
  usage: null,
  adminUsage: null,
  users: [],
  budgets: [],
  adminKeys: [],
  adminModels: [],
  auditLogs: [],
  secret: '',
  error: '',
  notice: ''
};

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

async function refresh() {
  state.error = '';
  try {
    state.health = await api('/api/health', { headers: {} });
    if (state.token) {
      state.user = await api('/api/auth/me');
      const base = [api('/api/models'), api('/api/api-keys'), api('/api/usage')];
      const [models, keys, usage] = await Promise.all(base);
      state.models = models || [];
      state.keys = keys || [];
      state.usage = usage;
      if (state.user.role === 'admin') {
        const [providers, users, budgets, adminUsage, adminModels, adminKeys, auditLogs] = await Promise.all([
          api('/api/admin/providers'),
          api('/api/admin/users'),
          api('/api/admin/budgets'),
          api('/api/admin/usage/summary'),
          api('/api/admin/models'),
          api('/api/admin/api-keys'),
          api('/api/admin/audit-log?limit=100')
        ]);
        state.providers = providers || [];
        state.users = users || [];
        state.budgets = budgets || [];
        state.adminUsage = adminUsage;
        state.adminModels = adminModels || [];
        state.adminKeys = adminKeys || [];
        state.auditLogs = auditLogs || [];
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
        <div class="mark">P</div>
        <div><h1>Phlox-GW</h1><p>Enterprise LLM gateway</p></div>
      </div>
      <p>Sign in with the local admin account to configure models, keys, budgets, and usage reporting.</p>
      <div class="field"><label>Username</label><input id="username" autocomplete="username" value="admin" /></div>
      <div class="field"><label>Password</label><input id="password" type="password" autocomplete="current-password" value="admin" /></div>
      <div class="error" id="login-error"></div>
      <button class="btn primary" id="login-btn">Sign in</button>
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
}

function shell(content) {
  const tabs = [
    ['overview', 'Overview'],
    ['keys', 'API Keys'],
    ['models', 'Models'],
    ['usage', 'Usage'],
    ['admin', 'Admin']
  ];
  app.innerHTML = `
    <div class="app">
      <aside class="sidebar">
        <div class="brand">
          <div class="mark">P</div>
          <div><h1>Phlox-GW</h1><p>LLM gateway</p></div>
        </div>
        <nav class="nav">
          ${tabs.map(([id, label]) => `<button data-tab="${id}" class="${state.tab === id ? 'active' : ''}">${label}</button>`).join('')}
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

function render() {
  if (!state.token || !state.user) {
    loginView();
    return;
  }
  if (state.tab === 'keys') return shell(keysView());
  if (state.tab === 'models') return shell(modelsView());
  if (state.tab === 'usage') return shell(usageView());
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
        <button class="btn primary" id="create-key">Create</button>
      </div>
      <div class="success" id="new-secret">${state.secret ? `New key: ${state.secret}` : ''}</div>
    </section>
    <section class="panel">
      <h3>Your API keys</h3>
      ${table(['Name', 'Prefix', 'Active', 'Last used', ''], state.keys.map(k => [
        esc(k.name), `<span class="mono">${esc(k.prefix)}</span>`, pill(k.is_active), fmt(k.last_used_at), k.is_active ? `<button class="btn danger" data-revoke="${k.id}">Revoke</button>` : ''
      ]))}
    </section>
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

function adminView() {
  if (state.user.role !== 'admin') return `<section class="panel">Admin role required.</section>`;
  const usage = state.adminUsage || {};
  return `
    <section class="grid">
      ${card('Users', state.users.length, 'Local accounts')}
      ${card('Providers', state.providers.length, 'Configured providers')}
      ${card('Budgets', state.budgets.length, 'Monthly limits')}
      ${card('API keys', state.adminKeys.length, 'User-owned credentials')}
      ${card('Audit events', state.auditLogs.length, 'Recent admin activity')}
      ${card('Total spend', money(usage.cost_usd || 0), `${usage.requests || 0} requests`)}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Add provider</h3><span>OpenAI-compatible covers Ollama, vLLM, LM Studio, OpenRouter, and LiteLLM.</span></div>
      <div class="form-grid">
        <input id="provider-id" placeholder="provider id, e.g. local-vllm" />
        <input id="provider-name" placeholder="Display name" />
        <select id="provider-type"><option value="openai">OpenAI-compatible</option><option value="anthropic">Anthropic-compatible</option><option value="bedrock">AWS Bedrock</option></select>
        <input id="provider-base-url" placeholder="Base URL, e.g. http://localhost:8000/v1" />
        <input id="provider-api-key-env" placeholder="API key env var, e.g. OPENAI_API_KEY" />
        <input id="provider-api-key" placeholder="Direct API key (optional)" type="password" />
        <input id="provider-aws-region" placeholder="AWS region for Bedrock" />
        <label class="check"><input id="provider-enabled" type="checkbox" checked /> Enabled</label>
        <button class="btn primary" id="create-provider">Add provider</button>
      </div>
    </section>
    <section class="panel">
      <h3>Providers</h3>
      ${providerRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Add model</h3><span>Route defaults to provider/model. Prices are USD per 1M tokens.</span></div>
      <div class="form-grid">
        <select id="model-provider">${state.providers.map(p => `<option value="${esc(p.id)}">${esc(p.id)} · ${esc(p.name)}</option>`).join('')}</select>
        <input id="model-model-id" placeholder="Upstream model id" />
        <input id="model-route" placeholder="Route id (optional)" />
        <input id="model-display-name" placeholder="Display name" />
        <input id="model-input-cost" placeholder="Input $ / 1M" type="number" min="0" step="0.0001" value="0" />
        <input id="model-output-cost" placeholder="Output $ / 1M" type="number" min="0" step="0.0001" value="0" />
        <input id="model-context" placeholder="Context window" type="number" min="0" step="1" value="0" />
        <label class="check"><input id="model-streaming" type="checkbox" checked /> Streaming</label>
        <label class="check"><input id="model-enabled" type="checkbox" checked /> Enabled</label>
        <button class="btn primary" id="create-model">Add model</button>
      </div>
    </section>
    <section class="panel">
      <h3>Models and pricing</h3>
      ${modelRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Add user</h3><span>Local users can mint their own API keys after signing in.</span></div>
      <div class="form-grid">
        <input id="user-username" placeholder="Username" />
        <input id="user-password" placeholder="Temporary password" type="password" />
        <input id="user-email" placeholder="Email" />
        <input id="user-display" placeholder="Display name" />
        <input id="user-department" placeholder="Department" />
        <select id="user-role"><option value="user">User</option><option value="admin">Admin</option></select>
        <button class="btn primary" id="create-user">Create user</button>
      </div>
    </section>
    <section class="panel">
      <h3>Users</h3>
      ${userRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>API key governance</h3><span>Empty allowlist means all enabled models. Limits of 0 are unlimited.</span></div>
      ${keyGovernanceRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Add budget</h3><span>User budgets use the user id shown above; department budgets use the department name.</span></div>
      <div class="form-grid">
        <select id="budget-scope-type"><option value="department">Department</option><option value="user">User</option></select>
        <input id="budget-scope-value" placeholder="Department name or user id" list="budget-values" />
        <datalist id="budget-values">${budgetValueOptions()}</datalist>
        <input id="budget-limit" placeholder="Monthly limit USD" type="number" min="0" step="0.01" />
        <input id="budget-warn" placeholder="Warn %" type="number" min="1" max="100" step="1" value="90" />
        <button class="btn primary" id="create-budget">Create budget</button>
      </div>
    </section>
    <section class="panel">
      <h3>Budgets</h3>
      ${budgetRows()}
    </section>
    <section class="panel">
      <div class="section-head"><h3>Audit log</h3><span>Recent local auth, admin, and API key lifecycle events.</span></div>
      ${auditLogRows()}
    </section>
  `;
}

function providerRows() {
  if (!state.providers.length) return '<p>No providers yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>ID</th><th>Name</th><th>Type</th><th>Base URL</th><th>Key env</th><th>Direct key</th><th>AWS region</th><th>Enabled</th><th>Health</th><th>Failures</th><th>Last check</th><th>Circuit open</th><th>Last error</th><th>Actions</th></tr></thead>
        <tbody>
          ${state.providers.map(p => `
            <tr data-provider-row="${esc(p.id)}">
              <td class="mono">${esc(p.id)}</td>
              <td><input data-provider-field="name" value="${attr(p.name)}" /></td>
              <td><select data-provider-field="type">${option('openai', 'OpenAI-compatible', p.type)}${option('anthropic', 'Anthropic-compatible', p.type)}${option('bedrock', 'AWS Bedrock', p.type)}</select></td>
              <td><input data-provider-field="base_url" value="${attr(p.base_url)}" /></td>
              <td><input data-provider-field="api_key_env" value="${attr((p.api_key_env || '').replace(' (secret set)', ''))}" /></td>
              <td><input data-provider-field="api_key" type="password" placeholder="leave blank to keep" /></td>
              <td><input data-provider-field="aws_region" value="${attr(p.aws_region)}" /></td>
              <td><input data-provider-field="enabled" type="checkbox" ${p.enabled ? 'checked' : ''} /></td>
              <td>${statusPill(p.health_status || 'unknown')}</td>
              <td>${Number(p.consecutive_failures || 0)}</td>
              <td>${fmt(p.last_health_check_at)}</td>
              <td>${fmt(p.circuit_open_until)}</td>
              <td class="wrap">${esc(p.last_error || '')}</td>
              <td><div class="actions"><button class="btn" data-save-provider="${esc(p.id)}">Save</button><button class="btn danger" data-delete-provider="${esc(p.id)}">Delete</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
}

function modelRows() {
  if (!state.adminModels.length) return '<p>No models yet.</p>';
  return `
    <div class="table-scroll">
      <table>
        <thead><tr><th>Route</th><th>Provider</th><th>Model id</th><th>Name</th><th>Input</th><th>Output</th><th>Context</th><th>Streaming</th><th>Enabled</th><th>Actions</th></tr></thead>
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
              <td><div class="actions"><button class="btn" data-save-key="${esc(k.id)}">Save</button><button class="btn danger" data-revoke-admin-key="${esc(k.id)}">Revoke</button></div></td>
            </tr>
          `).join('')}
        </tbody>
      </table>
    </div>
  `;
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
  const create = document.getElementById('create-key');
  if (create) {
    create.onclick = async () => {
      const name = document.getElementById('key-name').value || 'API key';
      const resp = await api('/api/api-keys', { method: 'POST', body: JSON.stringify({ name }) });
      state.secret = resp.key;
      await refresh();
    };
  }
  document.querySelectorAll('[data-revoke]').forEach((btn) => {
    btn.onclick = async () => {
      await api(`/api/api-keys/${btn.dataset.revoke}`, { method: 'DELETE' });
      await refresh();
    };
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
        aws_region: val('provider-aws-region'),
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
  document.querySelectorAll('[data-revoke-admin-key]').forEach((btn) => {
    btn.onclick = async () => {
      if (!confirm(`Revoke API key ${btn.dataset.revokeAdminKey}?`)) return;
      await api(`/api/admin/api-keys/${encodeURIComponent(btn.dataset.revokeAdminKey)}`, { method: 'DELETE' });
      state.notice = 'API key revoked.';
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

function money(v) {
  return `$${Number(v || 0).toFixed(4)}`;
}

function fmt(v) {
  return v ? new Date(v).toLocaleString() : '';
}

function val(id) {
  return document.getElementById(id)?.value?.trim() || '';
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

function collectFields(row, prefix) {
  const out = {};
  row.querySelectorAll(`[data-${prefix}-field]`).forEach((el) => {
    const key = el.dataset[`${prefix}Field`];
    out[key] = el.type === 'checkbox' ? el.checked : el.value.trim();
  });
  return out;
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

function titleForTab() {
  return { overview: 'Gateway overview', keys: 'API keys', models: 'Model catalog', usage: 'Usage and cost', admin: 'Administration' }[state.tab] || 'Gateway';
}

function subtitleForTab() {
  return {
    overview: 'Provider-neutral access with cost and budget controls.',
    keys: 'Mint and revoke user-owned keys for SDK access.',
    models: 'Enabled model routes and administrator-assigned pricing.',
    usage: 'Per-user tokens, request counts, and chargeback cost.',
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
