// rules.js - Admin Rules 配置页
// 整表读写 /admin/runtime-config/draft/rules；每次增删改排立即 PUT 到 draft，Apply 才落盘。
// 合同：单条至多一个 match 字段（空 = GLOBAL）；action 至少一类、可多类并存；
// effort 与 effort_mode 同条互斥。
// Action 编辑器采用 table-editor：每行一类 action，按需添加，便于后续扩展。

// ══════ State ══════

let rules = [];
// Apply 成功时的快照（顺序敏感）。null 表示尚未初始化；仅由首次加载和 Apply 成功更新，
// 避免空 draft 场景下重载把快照重置为当前值而丢失 hasChanges。
let originalRules = null;
let editingIndex = null; // 编辑中的规则下标；null = 新建
let effortPrevByRow = {}; // rowId -> effort chips 上一次选中值，用于 none 独占互斥判断
let actionRowSeq = 0; // 生成 action 行稳定 id，避免增删后 id 冲突

// Draft bar 配置（shared.js 使用）
window.draftBarConfig = {
  hasChanges: () => originalRules !== null && JSON.stringify(rules) !== JSON.stringify(originalRules),
  onApplySuccess: () => {
    originalRules = JSON.parse(JSON.stringify(rules));
  }
};

// ══════ Enums ══════

const MATCH_FIELDS = [
  { value: '', label: 'GLOBAL (all requests)' },
  { value: 'client_model', label: 'client_model' },
  { value: 'key', label: 'key' },
  { value: 'upstream_model', label: 'upstream_model' }
];
const MATCH_OPS = ['equals', 'startWith', 'include'];
const MATCH_VALUE_PLACEHOLDERS = {
  client_model: 'e.g. gpt-4',
  key: 'key name',
  upstream_model: 'e.g. openai/gpt-4'
};
const EFFORT_OPTIONS = ['low', 'medium', 'high', 'xhigh', 'max', 'none'];

// 规则可选出站协议（新建默认选项）；draft 中已有的其它合法协议在编辑时并入 options，
// 对齐 providers.js 的 protocolChipOptions 思路，避免打开编辑后误丢。
const RULE_PROTOCOL_OPTIONS = ['openai.chat', 'anthropic.messages', 'openai.responses'];

// Action 类型注册表：type 即 JSON 字段名。新增 action 只在此加一行；
// 摘要 / 回填 / 收集都扫这张表，不要再按字段名平铺 if-else。
const ACTION_DEFS = [
  {
    type: 'protocol',
    label: 'protocol',
    kind: 'enum-single',
    options: (selected) => protocolChipOptions(selected || ''),
    hint: 'Force the outbound protocol.'
  },
  {
    type: 'effort',
    label: 'effort',
    kind: 'enum-multi',
    options: () => EFFORT_OPTIONS,
    exclusiveWith: ['effort_mode'],
    hint: 'Allowed effort levels; none is exclusive.'
  },
  {
    type: 'effort_mode',
    label: 'effort_mode',
    kind: 'enum-single',
    options: () => ['strip'],
    exclusiveWith: ['effort'],
    hint: 'strip removes effort parameters from the request.'
  },
  {
    type: 'temperature_mode',
    label: 'temperature_mode',
    kind: 'enum-single',
    options: () => ['strip'],
    hint: 'strip removes the temperature parameter from the request.'
  },
  {
    type: 'thinking',
    label: 'thinking',
    kind: 'enum-single',
    options: () => ['on', 'off'],
    hint: 'Force thinking on/off.'
  },
  {
    type: 'max_tokens',
    label: 'max_tokens',
    kind: 'number',
    min: 1,
    placeholder: 'e.g. 2000',
    hint: 'Floor clamp: request max_tokens is raised to this value when unset or lower; larger values are kept.',
    invalidMessage: 'max_tokens must be a positive integer.'
  },
  {
    type: 'qpm',
    label: 'qpm',
    kind: 'number',
    min: 1,
    placeholder: 'e.g. 120',
    hint: 'Per-model QPM. Requires an upstream_model match.',
    invalidMessage: 'qpm must be a positive integer.'
  },
  {
    type: 'retries',
    label: 'retries',
    kind: 'number',
    min: 0,
    max: 2,
    placeholder: '0-2 extra attempts',
    hint: 'Extra same-leaf attempts after a transient 5xx/transport failure (max 2). 0 overrides a broader rule.',
    invalidMessage: 'retries must be 0, 1, or 2.'
  },
  {
    type: 'enable_time_range',
    label: 'enable_time_range',
    kind: 'time-ranges',
    hint: 'Allowed daily windows [start, end). Cross-midnight OK. Multiple = OR.'
  },
  {
    type: 'disable_time_range',
    label: 'disable_time_range',
    kind: 'time-ranges',
    hint: 'Blocked daily ranges [start, end). Cross-midnight OK. Multiple = OR.'
  }
];

function protocolChipOptions(selected) {
  const opts = RULE_PROTOCOL_OPTIONS.slice();
  if (selected && !opts.includes(selected)) opts.push(selected);
  return opts;
}

function actionDef(type) {
  return ACTION_DEFS.find((d) => d.type === type) || null;
}

function actionValuePresent(value) {
  if (value === undefined || value === null || value === '') return false;
  if (Array.isArray(value)) return value.length > 0;
  return true;
}

function cloneActionValue(value) {
  return Array.isArray(value) ? value.slice() : value;
}

function formatActionValue(value, escape) {
  const enc = (s) => (escape ? escapeHtml(String(s)) : String(s));
  if (Array.isArray(value)) return `[${value.map((item) => enc(item)).join(', ')}]`;
  return enc(value);
}

function eachPresentAction(action, fn) {
  const a = action || {};
  for (const def of ACTION_DEFS) {
    const value = a[def.type];
    if (!actionValuePresent(value)) continue;
    fn(def, value);
  }
}

// ══════ Load ══════

async function loadRules() {
  try {
    const data = await apiFetch('/admin/runtime-config/draft/rules');
    rules = (data && data.rules) || [];
    if (originalRules === null) {
      originalRules = JSON.parse(JSON.stringify(rules));
    }
    renderRules();
    updateDraftBar();
  } catch (err) {
    showToast(err.message || 'Failed to load rules.', 'error');
  }
}

// ══════ Render ══════

function renderRules() {
  const tbody = document.getElementById('rulesTableBody');
  const empty = document.getElementById('rulesEmpty');
  if (!rules || rules.length === 0) {
    tbody.innerHTML = '';
    empty.style.display = 'flex';
    initRulesSortable();
    return;
  }
  empty.style.display = 'none';
  tbody.innerHTML = rules.map((rule, i) => `
    <tr data-index="${i}">
      <td class="drag-cell">
        <span class="drag-handle" title="Drag to reorder">
          <i data-lucide="grip-vertical" style="width:14px;height:14px"></i>
        </span>
      </td>
      <td><span class="row-index">${i + 1}</span></td>
      <td>${matchSummary(rule.match)}</td>
      <td>${actionSummary(rule.action)}</td>
      <td>
        <div class="row-actions" style="justify-content:flex-end">
          <button class="btn-icon" onclick="openRuleModal(${i})" title="Edit">
            <i data-lucide="pencil" style="width:12px;height:12px"></i>
          </button>
          <button class="btn-icon" onclick="deleteRule(${i})" title="Delete" style="color:var(--danger);opacity:0.6">
            <i data-lucide="trash-2" style="width:12px;height:12px"></i>
          </button>
        </div>
      </td>
    </tr>
  `).join('');
  initRulesSortable();
}

function matchSummary(match) {
  const m = match || {};
  const chips = [];
  if (m.client_model) chips.push(condChip('client_model', m.client_model));
  if (m.key) chips.push(condChip('key', m.key));
  if (m.upstream_model) chips.push(condChip('upstream_model', m.upstream_model));
  if (chips.length === 0) return '<span class="badge badge-warning">GLOBAL</span>';
  return chips.join('');
}

function condChip(field, cond) {
  return `<span class="cond-chip"><span class="cond-field">${field}</span><span class="cond-op">${escapeHtml(cond.op)}</span><span class="cond-value">"${escapeHtml(cond.value)}"</span></span>`;
}

function actionSummary(action) {
  const chips = [];
  eachPresentAction(action, (def, value) => {
    chips.push(`<span class="cond-chip"><span class="cond-field">${escapeHtml(def.type)}</span><span class="cond-value">${formatActionValue(value, true)}</span></span>`);
  });
  return chips.length ? chips.join('') : '<span class="chip-muted">—</span>';
}

// ══════ Sortable 拖拽改序 ══════

let rulesSortable = null;

function initRulesSortable() {
  if (rulesSortable) {
    rulesSortable.destroy();
    rulesSortable = null;
  }
  const tbody = document.getElementById('rulesTableBody');
  if (!tbody || typeof Sortable === 'undefined') return;
  rulesSortable = new Sortable(tbody, {
    animation: 150,
    handle: '.drag-handle',
    ghostClass: 'sortable-ghost',
    chosenClass: 'sortable-chosen',
    onEnd: onRulesSorted
  });
}

async function onRulesSorted() {
  const tbody = document.getElementById('rulesTableBody');
  const order = Array.from(tbody.querySelectorAll('tr[data-index]')).map(tr => Number(tr.dataset.index));
  const next = order.map(i => rules[i]);
  // 顺序未变或 DOM 与状态不一致时不发 PUT；PUT 失败时重载以恢复 DOM 顺序
  if (next.length !== rules.length || JSON.stringify(next) === JSON.stringify(rules)) return;
  if (!(await putRules({ rules: next }))) {
    await loadRules();
  }
}

// ══════ PUT 整表 ══════

async function putRules(body) {
  try {
    const data = await apiFetch('/admin/runtime-config/draft/rules', {
      method: 'PUT',
      body: JSON.stringify(body)
    });
    rules = (data && data.rules) || [];
    renderRules();
    updateDraftBar();
    return true;
  } catch (err) {
    showToast(err.message || 'Failed to save rules.', 'error');
    return false;
  }
}

// ══════ Modal ══════

function opSelectOptions(selectedOp) {
  return MATCH_OPS.map(op =>
    `<option value="${op}" ${op === selectedOp ? 'selected' : ''}>${op}</option>`
  ).join('');
}

function fieldSelectOptions(selectedField) {
  return MATCH_FIELDS.map(f =>
    `<option value="${f.value}" ${f.value === selectedField ? 'selected' : ''}>${f.label}</option>`
  ).join('');
}

function openRuleModal(index) {
  editingIndex = (index === undefined || index === null) ? null : index;
  const rule = editingIndex !== null ? rules[editingIndex] : null;
  const match = (rule && rule.match) || {};
  const action = (rule && rule.action) || {};

  // 至多一个 match 字段；取首个存在的字段回填。
  const matchField = match.client_model ? 'client_model' : (match.key ? 'key' : (match.upstream_model ? 'upstream_model' : ''));
  const cond = matchField ? match[matchField] : null;
  const isGlobal = !matchField;
  const actionRows = actionRowsFromAction(action);

  const body = `
    <div class="form-section-label">Match</div>
    <div class="form-hint">One condition per rule; choose GLOBAL to match every request.</div>
    <div class="form-group">
      <div class="form-row">
        <select class="form-select" id="ruleMatchField" data-dropdown style="flex:0 0 180px">
          ${fieldSelectOptions(matchField)}
        </select>
        <div id="ruleMatchOpWrap" style="flex:0 0 120px;display:${isGlobal ? 'none' : ''}">
          <select class="form-select" id="ruleMatchOp" data-dropdown style="width:100%">
            ${opSelectOptions(cond ? cond.op : 'equals')}
          </select>
        </div>
        <input class="form-input" id="ruleMatchValue" type="text" style="display:${isGlobal ? 'none' : ''}" value="${escapeAttr(cond ? cond.value : '')}" placeholder="${escapeAttr(MATCH_VALUE_PLACEHOLDERS[matchField] || 'condition value')}">
      </div>
    </div>

    <div class="form-section-label">Action</div>
    <div class="form-hint">Add one row per action type. Same type across rules: later matching rule overrides earlier ones. effort and effort_mode are mutually exclusive.</div>
    <div class="form-group">
      <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
        <label class="form-label" style="margin:0">Actions <span class="required">*</span></label>
        <button type="button" class="btn btn-ghost btn-sm" id="ruleActionAddBtn" onclick="addActionRow()">+ Add</button>
      </div>
      <div class="table-editor" id="ruleActionEditor">
        <div class="table-editor-header">
          <span class="w-action-type">Type</span>
          <span class="w-action-value">Value</span>
          <span class="w-auto"></span>
        </div>
        ${actionRows.map((row) => renderActionRow(row)).join('')}
      </div>
      <span class="form-hint" id="ruleActionHint">Pick a type, then fill its value. Unused types stay out of the way until added.</span>
    </div>
  `;

  const footer = `
    <button class="btn btn-ghost" onclick="closeModal()">Cancel</button>
    <button class="btn btn-primary" onclick="saveRule()">${editingIndex !== null ? 'Save Changes' : 'Create Rule'}</button>
  `;

  showModal(editingIndex !== null ? 'Edit Rule' : 'Create Rule', body, footer);
  bindRuleModalEvents();
  refreshActionTypeOptions();
  updateActionRemoveButtons();
  updateActionAddButton();
  updateActionHint();
}

// ══════ Action table ══════

function nextActionRowId() {
  actionRowSeq += 1;
  return String(actionRowSeq);
}

function actionRowsFromAction(action) {
  const rows = [];
  eachPresentAction(action, (def, value) => {
    rows.push({ id: nextActionRowId(), type: def.type, value: cloneActionValue(value) });
  });
  if (rows.length === 0) {
    rows.push({ id: nextActionRowId(), type: '', value: null });
  }
  return rows;
}

function usedActionTypes(exceptRowId) {
  return Array.from(document.querySelectorAll('#ruleActionEditor .table-editor-row'))
    .filter((row) => row.dataset.rowId !== String(exceptRowId))
    .map((row) => row.dataset.actionType || '')
    .filter(Boolean);
}

function blockedActionTypes(usedTypes) {
  const blocked = new Set(usedTypes);
  for (const type of usedTypes) {
    const def = actionDef(type);
    (def && def.exclusiveWith || []).forEach((t) => blocked.add(t));
  }
  return blocked;
}

function actionTypeOptionsHtml(selectedType, rowId) {
  const used = usedActionTypes(rowId);
  const blocked = blockedActionTypes(used);
  const options = ['<option value="">Select type…</option>'];
  for (const def of ACTION_DEFS) {
    const taken = blocked.has(def.type) && def.type !== selectedType;
    options.push(
      `<option value="${escapeAttr(def.type)}" ${def.type === selectedType ? 'selected' : ''} ${taken ? 'disabled' : ''}>${escapeHtml(def.label)}</option>`
    );
  }
  return options.join('');
}

function renderActionValueEditor(type, value, rowId) {
  const def = actionDef(type);
  if (!def) {
    return '<span class="form-hint action-value-placeholder">Select an action type</span>';
  }
  if (def.kind === 'enum-single') {
    const selected = typeof value === 'string' ? value : '';
    return renderEnumChips({
      id: `ruleActionValue_${rowId}`,
      mode: 'single',
      allowEmpty: true,
      options: def.options(selected),
      selected
    });
  }
  if (def.kind === 'enum-multi') {
    const selected = Array.isArray(value) ? value : [];
    return renderEnumChips({
      id: `ruleActionValue_${rowId}`,
      mode: 'multi',
      options: def.options(selected),
      selected
    });
  }
  if (def.kind === 'number') {
    const raw = (value !== undefined && value !== null && value !== '') ? String(value) : '';
    const min = def.min !== undefined ? def.min : 1;
    const maxAttr = def.max !== undefined ? ` max="${def.max}"` : '';
    return `<input class="form-input" data-field="value" type="number" min="${min}"${maxAttr} value="${escapeAttr(raw)}" placeholder="${escapeAttr(def.placeholder || '')}">`;
  }
  if (def.kind === 'time-ranges') {
    const ranges = Array.isArray(value) ? value : [];
    return `
      <div class="tags-input-area" data-field="time-ranges" id="ruleActionRanges_${rowId}" onclick="document.getElementById('ruleActionRangesInput_${rowId}').focus()">
        ${ranges.map((range) => `
          <span class="tag">${escapeHtml(range)}<span class="tag-remove" onclick="this.parentElement.remove()">&times;</span></span>
        `).join('')}
        <input class="tag-input" id="ruleActionRangesInput_${rowId}" placeholder="HH:MM-HH:MM">
      </div>
    `;
  }
  return '';
}

function renderActionRow(row) {
  const type = row.type || '';
  return `
    <div class="table-editor-row" data-row-id="${escapeAttr(row.id)}" data-action-type="${escapeAttr(type)}">
      <select class="form-select w-action-type" data-field="type" data-dropdown>
        ${actionTypeOptionsHtml(type, row.id)}
      </select>
      <div class="action-value-cell w-action-value" data-field="value-wrap">
        ${renderActionValueEditor(type, row.value, row.id)}
      </div>
      <button type="button" class="btn-icon w-auto" onclick="removeActionRow(this)" title="Remove">
        <i data-lucide="minus" style="width:11px;height:11px"></i>
      </button>
    </div>
  `;
}

function addActionRow() {
  const editor = document.getElementById('ruleActionEditor');
  if (!editor) return;
  const used = usedActionTypes();
  const blocked = blockedActionTypes(used);
  const nextType = (ACTION_DEFS.find((d) => !blocked.has(d.type)) || { type: '' }).type;
  if (!nextType && ACTION_DEFS.every((d) => blocked.has(d.type))) {
    showToast('All action types are already added (or blocked by exclusivity).', 'warning');
    return;
  }
  const row = { id: nextActionRowId(), type: nextType, value: null };
  editor.insertAdjacentHTML('beforeend', renderActionRow(row));
  if (typeof initDropdowns === 'function') initDropdowns();
  bindActionRowEvents(editor.querySelector(`.table-editor-row[data-row-id="${row.id}"]`));
  refreshActionTypeOptions();
  updateActionRemoveButtons();
  updateActionAddButton();
  updateActionHint();
  if (typeof lucide !== 'undefined') lucide.createIcons();
}

function removeActionRow(btn) {
  const row = btn.closest('.table-editor-row');
  if (!row) return;
  const editor = document.getElementById('ruleActionEditor');
  const rows = editor ? editor.querySelectorAll('.table-editor-row') : [];
  if (rows.length <= 1) {
    // 保留一行空壳，避免表格塌成空状态。
    row.dataset.actionType = '';
    const typeSelect = row.querySelector('[data-field="type"]');
    if (typeSelect) typeSelect.value = '';
    const wrap = row.querySelector('[data-field="value-wrap"]');
    if (wrap) wrap.innerHTML = renderActionValueEditor('', null, row.dataset.rowId);
    refreshActionTypeOptions();
    updateActionRemoveButtons();
    updateActionAddButton();
    updateActionHint();
    return;
  }
  delete effortPrevByRow[row.dataset.rowId];
  row.remove();
  refreshActionTypeOptions();
  updateActionRemoveButtons();
  updateActionAddButton();
  updateActionHint();
}

function updateActionRemoveButtons() {
  const rows = document.querySelectorAll('#ruleActionEditor .table-editor-row');
  rows.forEach((row) => {
    const btn = row.querySelector('.btn-icon');
    if (!btn) return;
    const onlyEmpty = rows.length === 1 && !(row.dataset.actionType || '');
    if (rows.length <= 1 && onlyEmpty) {
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

function updateActionAddButton() {
  const btn = document.getElementById('ruleActionAddBtn');
  if (!btn) return;
  const used = usedActionTypes();
  const blocked = blockedActionTypes(used);
  const canAdd = ACTION_DEFS.some((d) => !blocked.has(d.type));
  btn.disabled = !canAdd;
  btn.style.opacity = canAdd ? '1' : '0.4';
}

function updateActionHint() {
  const hint = document.getElementById('ruleActionHint');
  if (!hint) return;
  const selected = usedActionTypes();
  if (selected.length === 0) {
    hint.textContent = 'Pick a type, then fill its value. Unused types stay out of the way until added.';
    return;
  }
  const hints = selected.map((type) => {
    const def = actionDef(type);
    return def ? `${def.label}: ${def.hint}` : '';
  }).filter(Boolean);
  hint.textContent = hints.join(' · ');
}

function refreshActionTypeOptions() {
  document.querySelectorAll('#ruleActionEditor .table-editor-row').forEach((row) => {
    const select = row.querySelector('[data-field="type"]');
    if (!select) return;
    const current = row.dataset.actionType || select.value || '';
    select.innerHTML = actionTypeOptionsHtml(current, row.dataset.rowId);
    select.value = current;
    if (typeof refreshDropdown === 'function') refreshDropdown(select);
  });
}

function onActionTypeChange(select) {
  const row = select.closest('.table-editor-row');
  if (!row) return;
  const nextType = select.value || '';
  const used = usedActionTypes(row.dataset.rowId);
  const blocked = blockedActionTypes(used);
  if (nextType && blocked.has(nextType)) {
    showToast(`${nextType} conflicts with another action on this rule.`, 'error');
    select.value = row.dataset.actionType || '';
    return;
  }
  row.dataset.actionType = nextType;
  const wrap = row.querySelector('[data-field="value-wrap"]');
  if (wrap) {
    wrap.innerHTML = renderActionValueEditor(nextType, null, row.dataset.rowId);
    bindActionRowEvents(row);
  }
  refreshActionTypeOptions();
  updateActionAddButton();
  updateActionHint();
}

function onEffortChange(e) {
  const el = e.target;
  const row = el.closest('.table-editor-row');
  const rowId = row ? row.dataset.rowId : '';
  const prev = effortPrevByRow[rowId] || [];
  const values = e.detail.values;
  if (values.includes('none')) {
    const clickedNone = !prev.includes('none');
    el.querySelectorAll('.enum-chip.active').forEach((c) => {
      const isNone = c.dataset.value === 'none';
      if ((clickedNone && !isNone) || (!clickedNone && isNone)) {
        c.classList.remove('active');
      }
    });
  }
  effortPrevByRow[rowId] = getEnumChipsValues(el);
}

function setupTimeRangeTagInput(inputId, areaId) {
  const input = document.getElementById(inputId);
  if (!input || input.dataset.bound === '1') return;
  input.dataset.bound = '1';
  input.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    e.preventDefault();
    const value = input.value.trim();
    if (!value) return;
    if (!validateRuleTimeRange(value)) {
      showToast('Invalid time range, expected HH:MM-HH:MM', 'error');
      return;
    }
    const area = document.getElementById(areaId);
    const tag = document.createElement('span');
    tag.className = 'tag';
    tag.innerHTML = `${escapeHtml(value)}<span class="tag-remove" onclick="this.parentElement.remove()">&times;</span>`;
    area.insertBefore(tag, input);
    input.value = '';
  });
}

function bindActionRowEvents(row) {
  if (!row) return;
  const typeSelect = row.querySelector('[data-field="type"]');
  if (typeSelect && typeSelect.dataset.bound !== '1') {
    typeSelect.dataset.bound = '1';
    typeSelect.addEventListener('change', () => onActionTypeChange(typeSelect));
  }
  const rowId = row.dataset.rowId;
  const type = row.dataset.actionType || '';
  if (type === 'effort') {
    const effortEl = document.getElementById(`ruleActionValue_${rowId}`);
    if (effortEl && effortEl.dataset.effortBound !== '1') {
      effortEl.dataset.effortBound = '1';
      effortEl.addEventListener('enumchange', onEffortChange);
      effortPrevByRow[rowId] = getEnumChipsValues(effortEl);
    }
  }
  if (type === 'enable_time_range' || type === 'disable_time_range') {
    setupTimeRangeTagInput(`ruleActionRangesInput_${rowId}`, `ruleActionRanges_${rowId}`);
  }
}

function bindRuleModalEvents() {
  effortPrevByRow = {};
  const fieldEl = document.getElementById('ruleMatchField');
  if (fieldEl) fieldEl.addEventListener('change', onMatchFieldChange);
  document.querySelectorAll('#ruleActionEditor .table-editor-row').forEach(bindActionRowEvents);
}

// ══════ Match 字段切换 ══════

function onMatchFieldChange() {
  const field = document.getElementById('ruleMatchField').value;
  const isGlobal = !field;
  document.getElementById('ruleMatchOpWrap').style.display = isGlobal ? 'none' : '';
  const valueInput = document.getElementById('ruleMatchValue');
  valueInput.style.display = isGlobal ? 'none' : '';
  valueInput.placeholder = MATCH_VALUE_PLACEHOLDERS[field] || 'condition value';
}

// ══════ Time-range tag inputs（HH:MM-HH:MM，与后端 ParseTimeRange 同级严格） ══════

function validRuleClock(h, m) {
  if (h < 0 || h > 24 || m < 0 || m > 59) return false;
  if (h === 24 && m !== 0) return false;
  return true;
}

function validateRuleTimeRange(range) {
  const match = range.match(/^(\d{2}):(\d{2})-(\d{2}):(\d{2})$/);
  if (!match) return false;
  const h1 = parseInt(match[1], 10);
  const m1 = parseInt(match[2], 10);
  const h2 = parseInt(match[3], 10);
  const m2 = parseInt(match[4], 10);
  if (!validRuleClock(h1, m1) || !validRuleClock(h2, m2)) return false;
  return !(h1 === h2 && m1 === m2);
}

function getTimeRangeTagValues(areaId) {
  return Array.from(document.querySelectorAll(`#${areaId} .tag`))
    .map(t => t.textContent.replace(/\×$/, '').trim())
    .filter(Boolean);
}

// ══════ Save ══════

// 组装 match：GLOBAL 提交空对象；非 GLOBAL 且 value trim 空 → 返回 null（调用方拦截），
// 绝不提交 {op, value:""} 这种永不命中条件。
function collectMatch() {
  const field = document.getElementById('ruleMatchField').value;
  if (!field) return {};
  const value = document.getElementById('ruleMatchValue').value.trim();
  if (!value) return null;
  const match = {};
  match[field] = { op: document.getElementById('ruleMatchOp').value, value };
  return match;
}

function readActionRowValue(row) {
  const type = row.dataset.actionType || '';
  const def = actionDef(type);
  if (!def) return { type: '', value: null, empty: true };

  if (def.kind === 'enum-single') {
    const v = getEnumChipsValue(`#ruleActionValue_${row.dataset.rowId}`);
    return { type, value: v || null, empty: !v };
  }
  if (def.kind === 'enum-multi') {
    const v = getEnumChipsValues(`#ruleActionValue_${row.dataset.rowId}`);
    return { type, value: v, empty: !v.length };
  }
  if (def.kind === 'number') {
    const raw = row.querySelector('[data-field="value"]');
    if (!raw || raw.value === '') return { type, value: null, empty: true };
    const n = parseInt(raw.value, 10);
    const min = def.min !== undefined ? def.min : 1;
    const max = def.max !== undefined ? def.max : Infinity;
    if (!Number.isFinite(n) || n < min || n > max) return { type, value: null, empty: true, invalid: true };
    return { type, value: n, empty: false };
  }
  if (def.kind === 'time-ranges') {
    const v = getTimeRangeTagValues(`ruleActionRanges_${row.dataset.rowId}`);
    return { type, value: v, empty: !v.length };
  }
  return { type, value: null, empty: true };
}

// 组装 action：遍历 action table 行；全空则返回空对象。
function collectAction() {
  const action = {};
  for (const row of document.querySelectorAll('#ruleActionEditor .table-editor-row')) {
    const parsed = readActionRowValue(row);
    if (!parsed.type || parsed.empty) continue;
    action[parsed.type] = parsed.value;
  }
  return action;
}

async function saveRule() {
  const match = collectMatch();

  // 非 GLOBAL 且 match 值为空：UI 拦截，不发 PUT
  if (match === null) {
    showToast('Match value is required for a non-GLOBAL rule.', 'error');
    return;
  }

  // 行级非法 number（填了但 <=0）先拦截
  for (const row of document.querySelectorAll('#ruleActionEditor .table-editor-row')) {
    const parsed = readActionRowValue(row);
    if (parsed.invalid) {
      const def = actionDef(parsed.type);
      showToast((def && def.invalidMessage) || `${parsed.type} value is invalid.`, 'error');
      return;
    }
    if (parsed.type && parsed.empty) {
      showToast(`Empty value for action type ${parsed.type}.`, 'error');
      return;
    }
  }

  const action = collectAction();

  // 空 action 在 UI 拦截：不发 PUT
  if (Object.keys(action).length === 0) {
    showToast('Empty action: add at least one action row with a value.', 'error');
    return;
  }

  // effort 与 effort_mode 同条互斥：前端拦截，不发 PUT（后端校验兜底）
  if (action.effort && action.effort_mode) {
    showToast('effort and effort_mode cannot be set on the same rule; clear one of them.', 'error');
    return;
  }

  // qpm 只允许 upstream_model match：前端拦截，不发 PUT（后端校验兜底）
  if (action.qpm !== undefined && !(match.upstream_model)) {
    showToast('qpm action requires an upstream_model match (rate limits protect the upstream identity).', 'error');
    return;
  }

  // 空 match 允许保存，但强提示为全局规则
  if (Object.keys(match).length === 0) {
    showToast('This rule has no match conditions — it will apply to ALL requests (GLOBAL). Make sure this is intended.', 'warning');
  }

  const next = rules.slice();
  if (editingIndex !== null) {
    next[editingIndex] = { match, action };
  } else {
    next.push({ match, action });
  }

  if (await putRules({ rules: next })) {
    closeModal();
  }
}

// ══════ Delete ══════

function ruleSummaryText(rule) {
  const m = rule.match || {};
  let condText = 'GLOBAL';
  if (m.client_model) condText = `client_model ${m.client_model.op} "${m.client_model.value}"`;
  else if (m.key) condText = `key ${m.key.op} "${m.key.value}"`;
  else if (m.upstream_model) condText = `upstream_model ${m.upstream_model.op} "${m.upstream_model.value}"`;
  const parts = [];
  eachPresentAction(rule.action, (def, value) => {
    parts.push(`${def.type} ${formatActionValue(value, false)}`);
  });
  return `${condText} → ${parts.length ? parts.join(', ') : '(empty action)'}`;
}

async function deleteRule(index) {
  const rule = rules[index];
  if (!rule) return;
  showConfirm({
    title: 'Delete Rule',
    message: `This will remove <strong>${escapeHtml(ruleSummaryText(rule))}</strong>.`,
    danger: true,
    onConfirm: async () => {
      const next = rules.filter((_, i) => i !== index);
      if (await putRules({ rules: next })) {
        showToast('Rule deleted.');
      }
    }
  });
}

// ══════ Init ══════

loadRules();
