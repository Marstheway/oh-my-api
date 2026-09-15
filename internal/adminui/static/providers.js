// ══════ State ══════

let providers = [];
let originalProviders = [];
let editProviderName = null;

// Draft bar 配置（shared.js 使用）
window.draftBarConfig = {
  hasChanges: () => JSON.stringify(originalProviders) !== JSON.stringify(providers),
  onApplySuccess: () => {
    originalProviders = JSON.parse(JSON.stringify(providers));
  }
};

// ══════ Shared enums (Enum Chips) ══════

// Admin UI 可选协议。后端 codec/handler 仍保留 ollama.* 运行时逻辑，但 Admin 不再提供新建入口。
// ollama.chat / ollama.embed：停止更新，逐步淘汰 —— 请改用 Ollama 的 OpenAI 兼容层：
//   openai.chat / openai.embeddings，endpoint 指向 http://host:11434/v1
const VALID_PROTOCOLS = [
  'openai.chat',
  'anthropic.messages',
  'openai.responses',
  'openai.embeddings'
  // 'ollama.chat',  // stopped updating, phased out
  // 'ollama.embed', // stopped updating, phased out
];

// 已有配置里若仍带有已淘汰协议，渲染 chips 时并入 options，避免打开编辑后误丢。
function protocolChipOptions(selected) {
  const opts = VALID_PROTOCOLS.slice();
  for (const p of selected || []) {
    if (p && !opts.includes(p)) opts.push(p);
  }
  return opts;
}

// ══════ Load Data ══════

async function loadProviders() {
  try {
    const data = await apiFetch('/admin/runtime-config/draft/providers');
    providers = data || [];
    // 首次加载时保存原始状态
    if (originalProviders.length === 0 || providers.length === 0) {
      originalProviders = JSON.parse(JSON.stringify(providers));
    }
    renderProviders();
    updateDraftBar();
  } catch (err) {
    showToast('Failed to load providers', 'error');
  }
}

// ══════ Render ══════

function renderProviders() {
  const grid = document.getElementById('providerGrid');
  const empty = document.getElementById('emptyState');

  // 防御性处理：providers 为 null/undefined 时视为空数组
  const list = (providers || []).slice().sort((a, b) => a.name.localeCompare(b.name));

  if (list.length === 0) {
    grid.style.display = 'none';
    empty.style.display = 'flex';
    return;
  }

  grid.style.display = 'grid';
  empty.style.display = 'none';

  grid.innerHTML = list.map(p => {
    // Endpoint info
    let endpointInfo = '';
    if (p.endpoints && p.endpoints.length > 1) {
      endpointInfo = `${p.endpoints.length} endpoints`;
    } else if (p.endpoints && p.endpoints.length === 1) {
      endpointInfo = escapeHtml(p.endpoints[0].url || '');
    }

    // Protocol badges
    const protocolBadges = (p.protocols || []).map(proto => {
      const badgeClass = getProtocolBadgeClass(proto);
      const label = getProtocolLabel(proto);
      return `<span class="badge ${badgeClass}">${escapeHtml(label)}</span>`;
    }).join('');

    if (isCascadeProvider(p)) {
      endpointInfo = 'cascade session';
    }

    // Advanced config badges
    const advancedBadges = [];
    if (p.rate_limit && p.rate_limit.qpm > 0) {
      advancedBadges.push(`<span class="badge badge-neutral">${p.rate_limit.qpm} qpm</span>`);
    }
    if (p.remote_bridge && p.remote_bridge.enabled) {
      advancedBadges.push('<span class="badge badge-info">OAuth bridge</span>');
    }
    if (isCascadeProvider(p)) {
      advancedBadges.push('<span class="badge badge-info">cascade</span>');
    }

    return `
      <div class="provider-card" onclick="openProviderModal('${escapeAttr(p.name)}')">
        <div class="card-header">
          <div class="card-title">${escapeHtml(p.name)}</div>
          <div class="card-actions" onclick="event.stopPropagation()">
            <button class="btn-icon" onclick="openProviderModal('${escapeAttr(p.name)}')" title="${isCascadeProvider(p) ? 'View' : 'Edit'}">
              <i data-lucide="pencil" style="width:11px;height:11px"></i>
            </button>
            <button class="btn-icon" onclick="deleteProvider('${escapeAttr(p.name)}')" title="Delete" style="color:var(--danger);opacity:0.6">
              <i data-lucide="trash-2" style="width:11px;height:11px"></i>
            </button>
          </div>
        </div>

        <div class="card-info-row">
          <span class="label">Endpoint:</span>
          <span>${endpointInfo}</span>
        </div>

        <div class="card-meta">
          ${protocolBadges}
          ${advancedBadges.join('')}
        </div>
      </div>
    `;
  }).join('');
}

function getProtocolBadgeClass(protocol) {
  if (protocol.includes('openai.chat') || protocol === 'openai.embeddings') return 'protocol-badge-openai';
  if (protocol.includes('anthropic.messages')) return 'protocol-badge-anthropic';
  if (protocol.includes('openai.responses')) return 'protocol-badge-responses';
  // legacy ollama.* still rendered for existing configs (phased out in Admin UI)
  if (protocol.includes('ollama')) return 'protocol-badge-ollama';
  return 'badge-neutral';
}

function isCascadeProvider(p) {
  return !!(p && p.cascade && p.cascade.enabled);
}

function getProtocolLabel(protocol) {
  if (protocol.includes('openai.chat')) return 'openai.chat';
  if (protocol === 'openai.embeddings') return 'openai.embeddings';
  if (protocol.includes('anthropic.messages')) return 'anthropic.messages';
  if (protocol.includes('openai.responses')) return 'openai.responses';
  // legacy labels (not offered for new selection)
  if (protocol.includes('ollama.chat')) return 'ollama.chat';
  if (protocol.includes('ollama.embed')) return 'ollama.embed';
  return protocol;
}

// ══════ Modal ══════

function openProviderModal(name) {
  editProviderName = name || null;
  const prov = name ? providers.find(p => p.name === name) : null;
  if (isCascadeProvider(prov)) {
    const body = `
      <div class="form-group">
        <p class="form-hint" style="margin:0">This provider is a cascade hub session. It is managed from the Cascade page.</p>
      </div>
      <div class="form-group">
        <label class="form-label">Provider Name</label>
        <input class="form-input" type="text" value="${escapeAttr(prov.name)}" disabled>
      </div>
      <div class="form-group">
        <label class="form-label">Protocols</label>
        <div class="chips">${(prov.protocols || []).map(p => `<span class="badge ${getProtocolBadgeClass(p)}">${escapeHtml(getProtocolLabel(p))}</span>`).join('')}</div>
      </div>
    `;
    const footer = `
      <button class="btn btn-ghost" onclick="closeModal()">Close</button>
      <button class="btn btn-primary" onclick="window.location.href='/admin/cascade'">Open Cascade Settings</button>
    `;
    showModal('Cascade Provider', body, footer);
    return;
  }

  // Merge endpoints array (backend always returns endpoints array now)
  let endpoints = [];
  if (prov && prov.endpoints && prov.endpoints.length > 0) {
    endpoints = prov.endpoints;
  }

  // Default: at least one endpoint row
  if (endpoints.length === 0) {
    endpoints = [{ url: '', protocols: [] }];
  }

  const hasBridge = prov && prov.remote_bridge && prov.remote_bridge.enabled;
  const hasAdvanced = hasBridge;
  const advancedBadges = [];
  if (hasBridge) advancedBadges.push('oauth');

  const body = `
    <!-- 1. Identity -->
    <div class="form-section-label">Identity</div>
    <div class="form-group">
      <label class="form-label">Provider Name <span class="required">*</span></label>
      <input class="form-input" id="pvName" type="text" value="${escapeAttr(prov ? prov.name : '')}" placeholder="e.g. openai">
      <span class="form-error" id="pvNameErr"></span>
    </div>

    <div class="form-row">
      <div class="form-group" style="flex: 1;">
        <label class="form-label">API Key</label>
        <input class="form-input" id="pvApiKey" type="text" value="${escapeAttr(prov ? prov.api_key : '')}" placeholder="sk-...">
        <span class="form-hint">Leave empty for no-auth providers</span>
      </div>
      <div class="form-group" style="flex: 0 0 120px;">
        <label class="form-label">Rate Limit (QPM)</label>
        <input class="form-input" id="pvQpm" type="number" value="${prov && prov.rate_limit ? prov.rate_limit.qpm : ''}" min="1" placeholder="No limit">
        <span class="form-hint">Requests per minute</span>
      </div>
    </div>

    <!-- 2. Connection -->
    <div class="form-section">
      <div class="form-section-label">Connection</div>
      <div class="form-group">
        <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
          <label class="form-label" style="margin:0">Endpoints <span class="required">*</span></label>
          <button class="btn btn-ghost btn-sm" onclick="addEndpointRow()">+ Add</button>
        </div>
        <div class="table-editor" id="endpointsEditor">
          <div class="table-editor-header">
            <span>Endpoint URL</span>
            <span class="w-protocols">Protocols</span>
            <span class="w-auto"></span>
          </div>
          ${endpoints.map((ep, i) => `
            <div class="table-editor-row" data-index="${i}">
              <input class="form-input" type="text" value="${escapeAttr(ep.url)}" placeholder="https://api.example.com/v1" data-field="url">
              ${renderEnumChips({
                mode: 'multi',
                className: 'endpoint-protocol-chips',
                options: protocolChipOptions(ep.protocols || []),
                selected: ep.protocols || [],
                attrs: { 'data-field': 'protocols' }
              })}
              <button class="btn-icon w-auto" onclick="removeEndpointRow(this)" title="Remove" ${endpoints.length <= 1 ? 'disabled style="opacity:0.3;pointer-events:none"' : ''}>
                <i data-lucide="minus" style="width:11px;height:11px"></i>
              </button>
            </div>
          `).join('')}
        </div>
        <span class="form-hint">Reachable capability. Multi-protocol providers should use one endpoint per protocol.</span>
      </div>
    </div>

    <!-- 3. Advanced -->
    <div class="form-section">
      <button class="collapse-toggle ${hasAdvanced ? '' : 'collapsed'}" onclick="this.classList.toggle('collapsed')">
        <svg class="collapse-icon" width="12" height="12" viewBox="0 0 12 12" fill="none">
          <path d="M2 4l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
        <span>Advanced</span>
        ${advancedBadges.map(b => `<span class="section-badge">${escapeHtml(b)}</span>`).join('')}
      </button>
      <div class="collapse-content">
        <div class="form-group">
          <label class="form-label">
            <input type="checkbox" id="pvRemoteBridgeEnabled" style="margin-right:6px;vertical-align:middle"
              ${hasBridge ? 'checked' : ''}
              onchange="document.getElementById('remoteBridgeFields').style.display = this.checked ? 'flex' : 'none'">
            Enable OAuth Bridge
          </label>
          <span class="form-hint">Route via an OAuth bridge instead of calling upstream directly</span>
        </div>
        <div id="remoteBridgeFields" style="display:${hasBridge ? 'flex' : 'none'};flex-direction:column;gap:12px;">
          <div class="form-group">
            <label class="form-label">
              <input type="checkbox" id="pvRemoteBridgeLocal" style="margin-right:6px;vertical-align:middle"
                ${prov && prov.remote_bridge && prov.remote_bridge.local ? 'checked' : ''}
                onchange="document.getElementById('remoteBridgeTokenField').style.display = this.checked ? 'none' : ''">
              Local OAuth Bridge
            </label>
            <span class="form-hint">Loopback bridge; skip bridge token auth</span>
          </div>
          <div class="form-row">
            <div class="form-group" style="flex: 0 0 180px;">
              <label class="form-label">OAuth Provider</label>
              <select class="form-select" id="pvRemoteBridgeProvider" data-dropdown>
                <option value="xai-oauth" ${!prov?.remote_bridge?.provider || prov.remote_bridge.provider === 'xai-oauth' ? 'selected' : ''}>xai-oauth</option>
              </select>
            </div>
            <div class="form-group" id="remoteBridgeTokenField" style="flex: 1;display:${prov && prov.remote_bridge && prov.remote_bridge.local ? 'none' : ''};">
              <label class="form-label">Bridge Token</label>
              <input class="form-input" id="pvRemoteBridgeToken" type="text" value="${escapeAttr(prov?.remote_bridge?.token || '')}" placeholder="bridge-auth-token">
            </div>
          </div>
        </div>
      </div>
    </div>
  `;

  const footer = `
    <button class="btn btn-ghost" onclick="closeModal()">Cancel</button>
    <button class="btn btn-primary" onclick="saveProvider()">${name ? 'Save Changes' : 'Create Provider'}</button>
  `;

  showModal(name ? 'Edit Provider' : 'Create Provider', body, footer);
}

// ══════ Endpoints Table ══════

function addEndpointRow() {
  const editor = document.getElementById('endpointsEditor');
  const row = document.createElement('div');
  row.className = 'table-editor-row';
  row.innerHTML = `
    <input class="form-input" type="text" placeholder="https://api.example.com/v1" data-field="url">
    ${renderEnumChips({
      mode: 'multi',
      className: 'endpoint-protocol-chips',
      options: protocolChipOptions([]),
      selected: [],
      attrs: { 'data-field': 'protocols' }
    })}
    <button class="btn-icon w-auto" onclick="removeEndpointRow(this)" title="Remove">
      <i data-lucide="minus" style="width:11px;height:11px"></i>
    </button>
  `;
  editor.appendChild(row);
  lucide.createIcons();
  row.querySelector('[data-field="url"]').focus();
  updateEndpointRemoveButtons();
}

function removeEndpointRow(btn) {
  btn.closest('.table-editor-row').remove();
  updateEndpointRemoveButtons();
}

function updateEndpointRemoveButtons() {
  const rows = document.querySelectorAll('#endpointsEditor .table-editor-row');
  rows.forEach(row => {
    const btn = row.querySelector('.btn-icon');
    if (rows.length <= 1) {
      btn.disabled = true;
      btn.style.opacity = '0.3';
      btn.style.pointerEvents = 'none';
    } else {
      btn.disabled = false;
      btn.style.opacity = '1';
      btn.style.pointerEvents = 'auto';
    }
  });
}

function getEndpointsValues() {
  const rows = document.querySelectorAll('#endpointsEditor .table-editor-row');
  return Array.from(rows).map(r => {
    const url = r.querySelector('[data-field="url"]').value.trim();
    const protocols = getEnumChipsValues(r.querySelector('[data-field="protocols"]'));
    return { url, protocols };
  }).filter(ep => ep.url);
}

// ══════ Save Provider ══════

async function saveProvider() {
  const name = document.getElementById('pvName').value.trim();
  const apiKey = document.getElementById('pvApiKey').value.trim();
  const qpm = parseInt(document.getElementById('pvQpm').value) || 0;

  const endpoints = getEndpointsValues();

  // Remote bridge
  const remoteBridgeEnabled = document.getElementById('pvRemoteBridgeEnabled').checked;
  const remoteBridgeLocal = document.getElementById('pvRemoteBridgeLocal').checked;
  const remote_bridge = remoteBridgeEnabled ? {
    enabled: true,
    provider: document.getElementById('pvRemoteBridgeProvider').value.trim(),
    token: remoteBridgeLocal ? '' : document.getElementById('pvRemoteBridgeToken').value.trim(),
    local: remoteBridgeLocal
  } : undefined;

  document.getElementById('pvNameErr').textContent = '';
  if (!name) { document.getElementById('pvNameErr').textContent = 'Provider name is required.'; return; }
  if (endpoints.length === 0) { showToast('At least one endpoint is required.', 'error'); return; }

  // Validate each endpoint has at least one protocol
  for (const ep of endpoints) {
    if (!ep.protocols || ep.protocols.length === 0) {
      showToast('Each endpoint must have at least one protocol.', 'error');
      return;
    }
  }

  const body = {
    name,
    api_key: apiKey,
    rate_limit: qpm > 0 ? { qpm } : undefined,
    endpoints,
    remote_bridge
  };

  try {
    if (editProviderName) {
      const result = await apiFetch(`/admin/runtime-config/draft/providers/${encodeURIComponent(editProviderName)}`, { method: 'PUT', body: JSON.stringify(body) });
      // Update local state
      const idx = providers.findIndex(p => p.name === editProviderName);
      if (idx !== -1) {
        providers[idx] = result;
      }
      showToast(`"${name}" updated.`);
    } else {
      const result = await apiFetch('/admin/runtime-config/draft/providers', { method: 'POST', body: JSON.stringify(body) });
      providers.push(result);
      showToast(`"${name}" created.`);
    }
    closeModal();
    renderProviders();
    updateDraftBar();
  } catch (err) {
    if (err.field) {
      document.getElementById('pvNameErr').textContent = err.message;
    }
    showToast(err.message || 'An error occurred.', 'error');
  }
}

// ══════ Delete Provider ══════

function deleteProvider(name) {
  showConfirm({
    title: 'Delete Provider',
    message: `This will remove provider <strong>${escapeHtml(name)}</strong>. Model groups referencing it will break.`,
    danger: true,
    onConfirm: async () => {
      try {
        await apiFetch(`/admin/runtime-config/draft/providers/${encodeURIComponent(name)}`, { method: 'DELETE' });
        providers = providers.filter(p => p.name !== name);
        renderProviders();
        updateDraftBar();
        showToast(`"${name}" deleted.`);
      } catch (err) {
        showToast(err.message || 'Delete failed.', 'error');
      }
    }
  });
}


// ══════ Init ══════

loadProviders();
