// ══════ State ══════

let view = {
  mode: 'disabled',
  hub: null,
  spoke: null,
  providers: []
};
let models = { model_groups: [], redirect: [] };
let baseline = null;
let lastPersisted = null;
let persistTimer = null;
let persistChain = Promise.resolve();

const HUB_PROTOCOLS = ['openai.chat', 'openai.responses', 'anthropic.messages'];
const PERSIST_DEBOUNCE_MS = 300;

// Draft bar 配置（shared.js 使用）
// baseline = 页面加载时的 draft 快照（视为已生效基线）。
// 合法表单 debounce 写入 /draft/cascade；Apply / 离开页面时 flush。
window.draftBarConfig = {
  hasChanges: () => baseline !== null && JSON.stringify(collectForm()) !== JSON.stringify(baseline),
  onBeforeApply: () => persistDraft({ reportInvalid: true }),
  onApplySuccess: () => {
    loadData();
  }
};

// ══════ Load Data ══════

async function loadData() {
  try {
    const [cascade, draft] = await Promise.all([
      apiFetch('/admin/runtime-config/draft/cascade'),
      apiFetch('/admin/runtime-config/draft')
    ]);
    view = cascade || { mode: 'disabled', providers: [] };
    models.model_groups = draft.model_groups || [];
    models.redirect = draft.redirect || [];
    renderAll();
    baseline = collectForm();
    lastPersisted = JSON.stringify(baseline);
    updateDraftBar();
  } catch (err) {
    showToast(err.message || 'Failed to load cascade configuration.', 'error');
  }
}

// ══════ Render ══════

function renderAll() {
  renderModeChips();
  renderRoleSections();
  renderHubProtocols();
  populatePeerDropdown();
  updateSpokeConnUrl();
  renderPublishedModels();
}

function renderModeChips() {
  document.getElementById('cascadeModeWrap').innerHTML = renderEnumChips({
    id: 'cascadeMode',
    mode: 'single',
    options: [
      { value: 'disabled', label: 'Disabled' },
      { value: 'hub', label: 'Hub' },
      { value: 'spoke', label: 'Spoke' }
    ],
    selected: view.mode
  });
}

function renderRoleSections() {
  document.getElementById('hubSection').style.display = view.mode === 'hub' ? '' : 'none';
  document.getElementById('spokeSection').style.display = view.mode === 'spoke' ? '' : 'none';
  document.getElementById('publishedSection').style.display = view.mode === 'spoke' ? '' : 'none';

  document.getElementById('hubProviderName').value = view.hub ? view.hub.provider_name : '';
  document.getElementById('hubToken').value = view.hub ? (view.hub.token || '') : '';
  document.getElementById('spokeHub').value = view.spoke ? view.spoke.hub : '';
  document.getElementById('spokeToken').value = view.spoke ? (view.spoke.token || '') : '';
}

function renderHubProtocols() {
  document.getElementById('hubProtocols').innerHTML = HUB_PROTOCOLS.map(p =>
    `<span class="badge badge-info">${escapeHtml(p)}</span>`
  ).join('');
}

function populatePeerDropdown() {
  const select = document.getElementById('spokePeer');
  const current = view.spoke ? view.spoke.peer : '';
  const options = ['<option value="">Select provider...</option>']
    .concat(view.providers.map(p =>
      `<option value="${escapeAttr(p)}" ${p === current ? 'selected' : ''}>${escapeHtml(p)}</option>`
    ))
    .join('');
  select.innerHTML = options;
  if (select.dataset.dropdownInit) refreshDropdown(select);
}

function renderPublishedModels() {
  const showInternal = document.getElementById('showInternalModels').checked;
  const rows = [];

  for (const g of models.model_groups) {
    const exposure = g.exposure || 'public';
    if (!showInternal && exposure === 'internal') continue;
    rows.push({
      name: g.name,
      type: 'Model Group',
      exposure,
      ctx: contextLengthOf(g)
    });
  }

  for (const r of models.redirect) {
    const exposure = r.exposure || 'public';
    if (!showInternal && exposure === 'internal') continue;
    rows.push({
      name: r.source,
      type: 'Redirect',
      exposure,
      ctx: r.context_length || null
    });
  }

  rows.sort((a, b) => a.name.localeCompare(b.name));

  const tbody = document.getElementById('publishedTableBody');
  if (rows.length === 0) {
    tbody.innerHTML = '<tr><td colspan="4" style="color:var(--text-muted)">No published models.</td></tr>';
    return;
  }

  tbody.innerHTML = rows.map(r => `
    <tr class="${r.exposure === 'internal' ? 'cascade-internal' : ''}">
      <td><span class="code">${escapeHtml(r.name)}</span></td>
      <td><span class="code-secondary">${escapeHtml(r.type)}</span></td>
      <td>${exposureLabel(r.exposure)}</td>
      <td>${r.ctx ? formatContextLength(r.ctx) : '<span style="color:var(--text-muted)">—</span>'}</td>
    </tr>
  `).join('');
}

function contextLengthOf(group) {
  const md = group.model_metadata;
  if (!md) return null;
  if (md.context_length) return md.context_length;
  if (md.computed_context_length) return md.computed_context_length;
  return null;
}

function formatContextLength(val) {
  return val >= 1000000 ? (val / 1000000).toFixed(0) + 'M' : (val / 1000).toFixed(0) + 'k';
}

function exposureLabel(exposure) {
  switch (exposure) {
    case 'internal':
      return '<span class="vis-label" style="color:var(--text-muted)"><span class="vis-dot internal"></span>internal</span>';
    case 'hidden':
      return '<span class="vis-label" style="color:var(--warning)"><span class="vis-dot hidden"></span>hidden</span>';
    default:
      return '<span class="vis-label" style="color:var(--success)"><span class="vis-dot public"></span>public</span>';
  }
}

// ══════ Role Switching ══════

document.getElementById('cascadeModeWrap').addEventListener('enumchange', (e) => {
  const next = e.detail.value;
  if (!next || next === view.mode) return;

  showConfirm({
    title: 'Change Cascade Role',
    message: `Switching from <strong>${view.mode}</strong> to <strong>${next}</strong> will discard the other role's configuration.`,
    danger: next === 'disabled',
    onConfirm: () => {
      view.mode = next;
      renderAll();
      persistDraft({ reportInvalid: false });
    }
  });

  // Revert the chip selection until the user confirms.
  renderModeChips();
});

// ══════ URL Helpers ══════

function validateHubOrigin(origin) {
  origin = (origin || '').trim();
  if (!origin) return { error: 'Hub origin is required.' };

  let u;
  try {
    u = new URL(origin);
  } catch (e) {
    return { error: 'Invalid URL.' };
  }

  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    return { error: 'Scheme must be http or https (not ws/wss).' };
  }
  if (u.username || u.password) return { error: 'Userinfo is not allowed.' };
  if (u.search) return { error: 'Query is not allowed.' };
  if (u.hash) return { error: 'Fragment is not allowed.' };
  if (!u.host) return { error: 'Host is required.' };

  // new URL normalizes 'https://host' and 'https://host/' to pathname '/', and
  // strips explicit default ports from u.host, so derive the authority from the
  // raw input to reject only a genuine path and preserve the exact host:port.
  const schemeSep = origin.indexOf('://');
  if (schemeSep === -1) return { error: 'Invalid URL.' };
  const authorityStart = schemeSep + 3;
  let authorityEnd = origin.length;
  for (const sep of ['/', '?', '#']) {
    const idx = origin.indexOf(sep, authorityStart);
    if (idx !== -1 && idx < authorityEnd) authorityEnd = idx;
  }
  if (origin.slice(authorityEnd) !== '') return { error: 'Path is not allowed.' };
  const rawAuthority = origin.slice(authorityStart, authorityEnd);

  const scheme = u.protocol === 'https:' ? 'wss' : 'ws';
  return { url: `${scheme}://${rawAuthority}/cascade` };
}

function updateSpokeConnUrl() {
  const origin = document.getElementById('spokeHub').value;
  const result = validateHubOrigin(origin);
  document.getElementById('spokeConnUrl').textContent = result.url || '—';
}

// ══════ Form ══════

function collectForm() {
  return {
    mode: getEnumChipsValue('#cascadeMode') || 'disabled',
    hub: {
      provider_name: (document.getElementById('hubProviderName').value || '').trim(),
      token: (document.getElementById('hubToken').value || '').trim()
    },
    spoke: {
      hub: (document.getElementById('spokeHub').value || '').trim(),
      token: (document.getElementById('spokeToken').value || '').trim(),
      peer: document.getElementById('spokePeer').value || ''
    }
  };
}

function clearErrors() {
  ['hubProviderNameErr', 'hubTokenErr', 'spokeHubErr', 'spokeTokenErr', 'spokePeerErr'].forEach(id => {
    document.getElementById(id).textContent = '';
  });
}

function setError(id, message) {
  document.getElementById(id).textContent = message;
}

function validateForm(form, { silent } = {}) {
  if (!silent) clearErrors();
  const fail = (id, message) => {
    if (!silent) setError(id, message);
    return false;
  };

  if (form.mode === 'hub') {
    if (!form.hub.provider_name) {
      return fail('hubProviderNameErr', 'Provider name is required.');
    }
    if ((view.providers || []).includes(form.hub.provider_name)) {
      return fail('hubProviderNameErr', 'This name already belongs to an existing provider. Choose a different hub name.');
    }
    if (!form.hub.token) {
      return fail('hubTokenErr', 'Shared token is required.');
    }
  } else if (form.mode === 'spoke') {
    const v = validateHubOrigin(form.spoke.hub);
    if (v.error) {
      return fail('spokeHubErr', v.error);
    }
    if (!form.spoke.token) {
      return fail('spokeTokenErr', 'Shared token is required.');
    }
    if (!form.spoke.peer) {
      return fail('spokePeerErr', 'Peer provider is required.');
    }
  }

  return true;
}

function schedulePersist() {
  updateDraftBar();
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = null;
    persistDraft({ reportInvalid: false });
  }, PERSIST_DEBOUNCE_MS);
}

function persistDraft({ reportInvalid } = {}) {
  if (persistTimer) {
    clearTimeout(persistTimer);
    persistTimer = null;
  }
  const run = persistChain.then(() => persistDraftOnce({ reportInvalid }));
  persistChain = run.then(() => {}, () => {});
  return run;
}

async function persistDraftOnce({ reportInvalid } = {}) {
  const form = collectForm();
  try {
    if (reportInvalid) {
      if (!validateForm(form)) return false;
    } else if (!validateForm(form, { silent: true })) {
      return true;
    }

    const snapshot = JSON.stringify(form);
    if (snapshot === lastPersisted) return true;

    const next = await apiFetch('/admin/runtime-config/draft/cascade', {
      method: 'PUT',
      body: snapshot,
      keepalive: true
    });
    lastPersisted = snapshot;
    view = next;
    if (JSON.stringify(collectForm()) === snapshot) {
      renderAll();
    }
    return true;
  } catch (err) {
    showFieldError(err);
    showToast(err.message || 'Failed to save cascade draft.', 'error');
    return false;
  } finally {
    updateDraftBar();
  }
}

function showFieldError(err) {
  const map = {
    'mode': null,
    'hub.provider_name': 'hubProviderNameErr',
    'hub.token': 'hubTokenErr',
    'spoke.hub': 'spokeHubErr',
    'spoke.token': 'spokeTokenErr',
    'spoke.peer': 'spokePeerErr'
  };
  const id = err.field ? map[err.field] : null;
  if (id) setError(id, err.message);
}

// ══════ Init ══════

['hubProviderName', 'hubToken', 'spokeHub', 'spokeToken'].forEach(id => {
  const el = document.getElementById(id);
  if (el) el.addEventListener('input', schedulePersist);
});

const spokePeerEl = document.getElementById('spokePeer');
if (spokePeerEl) spokePeerEl.addEventListener('change', schedulePersist);

window.addEventListener('pagehide', () => persistDraft({ reportInvalid: false }));
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') persistDraft({ reportInvalid: false });
});

loadData();
