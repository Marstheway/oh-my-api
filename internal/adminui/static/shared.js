// shared.js - Admin UI 共享工具函数

/**
 * 封装 fetch，自动处理 401 未授权响应
 * @param {string} path - 请求路径
 * @param {object} opts - fetch 选项
 * @returns {Promise<object>}
 */
async function apiFetch(path, opts = {}) {
  const resp = await fetch(path, {
    ...opts,
    credentials: 'same-origin',
    headers: opts.headers,
  });

  if (resp.status === 401) {
    window.location.href = '/admin/login';
    throw new Error('Unauthorized');
  }

  if (!resp.ok) {
    const err = await resp.json();
    const error = new Error(err.error?.message || 'Request failed');
    error.field = err.error?.field;
    throw error;
  }

  // 204 No Content 无响应体，直接返回 null 避免 JSON 解析失败
  if (resp.status === 204) {
    return null;
  }

  return resp.json();
}

// ══════ Modal ══════

function showModal(title, bodyHtml, footerHtml) {
  closeModal();
  const html = `
    <div class="modal-overlay">
      <div class="modal">
        <div class="modal-header">
          <span class="modal-title">${escapeHtml(title)}</span>
        </div>
        <div class="modal-body">${bodyHtml}</div>
        ${footerHtml ? '<div class="modal-footer">' + footerHtml + '</div>' : ''}
      </div>
    </div>`;

  const container = document.getElementById('modalContainer');
  container.innerHTML = html;

  // 初始化 modal 内的 dropdown
  if (typeof initDropdowns === 'function') {
    initDropdowns();
  }
}

function closeModal() {
  document.getElementById('modalContainer').innerHTML = '';
}

// ══════ Confirm ══════

let confirmEscHandler = null;

function showConfirm({ title, message, danger, onConfirm }) {
  closeConfirm();
  const cls = danger ? 'danger' : 'success';
  const html = `
    <div class="confirm-overlay" onclick="if(event.target===this)closeConfirm()">
      <div class="confirm-dialog ${cls}" onclick="event.stopPropagation()">
        <div class="confirm-title">${escapeHtml(title)}</div>
        <div class="confirm-message">${message}</div>
        <div class="confirm-actions">
          <button class="btn btn-ghost" onclick="closeConfirm()">Cancel</button>
          <button class="btn btn-primary" id="confirmOkBtn">Confirm</button>
        </div>
      </div>
    </div>`;

  document.getElementById('confirmContainer').innerHTML = html;
  document.getElementById('confirmOkBtn').addEventListener('click', () => { closeConfirm(); onConfirm(); });

  confirmEscHandler = (e) => { if (e.key === 'Escape') closeConfirm(); };
  document.addEventListener('keydown', confirmEscHandler);
}

function closeConfirm() {
  document.getElementById('confirmContainer').innerHTML = '';
  if (confirmEscHandler) {
    document.removeEventListener('keydown', confirmEscHandler);
    confirmEscHandler = null;
  }
}

// ══════ Toast ══════

function showToast(message, type) {
  type = type || 'success';
  const container = document.getElementById('toastContainer');
  const toast = document.createElement('div');
  toast.className = 'toast ' + type;
  toast.textContent = (type === 'error' ? '' : '') + message;
  container.appendChild(toast);
  setTimeout(() => {
    toast.style.opacity = '0';
    toast.style.transition = 'opacity 200ms ease';
    setTimeout(() => toast.remove(), 200);
  }, 3000);
}

// ══════ HTML 转义 ══════

function escapeHtml(str) {
  const div = document.createElement('div');
  div.textContent = str;
  return div.innerHTML;
}

// ══════ Mobile Sidebar Toggle ══════

let mobileSidebarInitialized = false;

function initMobileSidebar() {
  if (window.innerWidth > 768 || mobileSidebarInitialized) {
    return;
  }

  const sidebar = document.querySelector('.sidebar');
  if (!sidebar) return;

  // 创建汉堡按钮
  if (!document.querySelector('.mobile-menu-toggle')) {
    const toggle = document.createElement('button');
    toggle.className = 'mobile-menu-toggle';
    toggle.setAttribute('aria-label', 'Toggle navigation menu');
    toggle.innerHTML = `
      <div class="hamburger-icon">
        <span></span><span></span><span></span>
      </div>
    `;
    document.body.appendChild(toggle);

    // 创建遮罩层
    const overlay = document.createElement('div');
    overlay.className = 'sidebar-overlay';
    document.body.appendChild(overlay);

    // 绑定事件
    toggle.addEventListener('click', toggleMobileSidebar);
    overlay.addEventListener('click', closeMobileSidebar);

    // 导航项点击后自动关闭
    document.querySelectorAll('.nav-item').forEach(item => {
      item.addEventListener('click', () => {
        if (window.innerWidth <= 768) {
          closeMobileSidebar();
        }
      });
    });

    mobileSidebarInitialized = true;
  }
}

function toggleMobileSidebar() {
  const sidebar = document.querySelector('.sidebar');
  const toggle = document.querySelector('.mobile-menu-toggle');
  const overlay = document.querySelector('.sidebar-overlay');

  const isOpen = sidebar.classList.contains('open');

  if (isOpen) {
    closeMobileSidebar();
  } else {
    sidebar.classList.add('open');
    toggle.classList.add('active');
    overlay.classList.add('active');
    document.body.style.overflow = 'hidden';
  }
}

function closeMobileSidebar() {
  const sidebar = document.querySelector('.sidebar');
  const toggle = document.querySelector('.mobile-menu-toggle');
  const overlay = document.querySelector('.sidebar-overlay');

  if (sidebar) sidebar.classList.remove('open');
  if (toggle) toggle.classList.remove('active');
  if (overlay) overlay.classList.remove('active');
  document.body.style.overflow = '';
}

// 页面加载时初始化
document.addEventListener('DOMContentLoaded', () => {
  initMobileSidebar();
});

// 窗口 resize 时处理
window.addEventListener('resize', () => {
  if (window.innerWidth <= 768) {
    initMobileSidebar();
  } else {
    closeMobileSidebar();
  }
});

// ══════ Lucide Icons Auto-Init ══════

(function initLucideIcons() {
  if (typeof lucide === 'undefined') return;

  lucide.createIcons();

  let pending = false;
  const observer = new MutationObserver(() => {
    if (pending) return;
    pending = true;
    requestAnimationFrame(() => {
      pending = false;
      // 二次确认：只处理未初始化的图标，已处理的图标有 .lucide class
      if (document.querySelector('[data-lucide]:not(.lucide)')) {
        lucide.createIcons();
      }
    });
  });

  observer.observe(document.body, { childList: true, subtree: true });
})();

// ══════ Draft Bar ══════
// 各页面需在脚本开头设置 window.draftBarConfig：
//   hasChanges: () => boolean          — 判断是否有 pending changes
//   onApplySuccess: () => void         — apply 成功后更新 original state + 刷新 UI

/**
 * 更新 draft bar 状态（dot 动画、文案、Apply 按钮 disabled）
 */
function updateDraftBar() {
  const bar = document.getElementById('draftBar');
  const label = document.getElementById('draftBarLabel');
  const btn = document.getElementById('applyBtn');
  const cfg = window.draftBarConfig;

  const hasChanges = cfg && cfg.hasChanges ? cfg.hasChanges() : false;

  bar.classList.toggle('has-changes', hasChanges);
  label.textContent = hasChanges ? 'Pending changes — not yet applied to runtime' : 'No pending changes';
  btn.disabled = !hasChanges;
}

/**
 * 应用当前 draft 配置到 runtime
 */
async function applyDraft() {
  const cfg = window.draftBarConfig || {};
  try {
    await apiFetch('/admin/runtime-config/apply', { method: 'POST' });
    showToast('Configuration applied successfully.');
    if (cfg.onApplySuccess) {
      cfg.onApplySuccess();
    }
    updateDraftBar();
  } catch (err) {
    showToast(err.message || 'Apply failed.', 'error');
  }
}
