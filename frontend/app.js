import {
  GetConfig,
  AddApp,
  AddPaths,
  RemoveApp,
  LaunchApp,
  LaunchAll,
  CaptureRunning,
  SaveConfig,
  SetAutoStart,
  SetAutoRestore,
} from './wailsjs/go/main/App.js';
import { EventsOn, OnFileDrop } from './wailsjs/runtime/runtime.js';

let config = { manual: [], captured: [], auto_start: false, auto_restore: false };
let selection = { zone: null, index: -1 };

const $ = (id) => document.getElementById(id);

const els = {
  summary: $('summary'),
  gridManual: $('grid-manual'),
  gridCaptured: $('grid-captured'),
  emptyManual: $('empty-manual'),
  emptyCaptured: $('empty-captured'),
  countManual: $('count-manual'),
  countCaptured: $('count-captured'),
  status: $('status'),
  toast: $('toast'),
  chkAutoStart: $('chk-autostart'),
  chkAutoRestore: $('chk-autorestore'),
};

let toastTimer = null;
function toast(msg) {
  els.toast.textContent = msg;
  els.toast.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => els.toast.classList.remove('show'), 2600);
}

function setStatus(msg) {
  els.status.textContent = msg;
}

// ---------- 渲染 ----------

function cardHTML(item, zone, index) {
  const selected = selection.zone === zone && selection.index === index;
  const initial = (item.name || '?').replace(/\.exe$/i, '').charAt(0).toUpperCase();
  const icon = item.icon
    ? `<img src="${item.icon}" alt="" draggable="false" />`
    : `<div class="fallback">${initial}</div>`;
  return `
    <div class="card${selected ? ' selected' : ''}" data-zone="${zone}" data-index="${index}" title="${escapeAttr(item.path)}">
      <div class="card-icon">${icon}</div>
      <div class="card-name">${escapeHTML(item.name)}</div>
    </div>`;
}

function escapeHTML(s) {
  return String(s || '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function escapeAttr(s) {
  return escapeHTML(s).replace(/"/g, '&quot;');
}

function render() {
  const manual = config.manual || [];
  const captured = config.captured || [];

  els.gridManual.innerHTML = manual.map((it, i) => cardHTML(it, 'manual', i)).join('');
  els.gridCaptured.innerHTML = captured.map((it, i) => cardHTML(it, 'captured', i)).join('');

  els.emptyManual.classList.toggle('hidden', manual.length > 0);
  els.emptyCaptured.classList.toggle('hidden', captured.length > 0);

  els.countManual.textContent = manual.length;
  els.countCaptured.textContent = captured.length;
  els.summary.textContent = `手动 ${manual.length} 个 · 抓取 ${captured.length} 个`;
}

function renderSelectionOnly() {
  document.querySelectorAll('.card').forEach((el) => {
    const zone = el.dataset.zone;
    const index = Number(el.dataset.index);
    el.classList.toggle('selected', selection.zone === zone && selection.index === index);
  });
}

// ---------- 数据加载 ----------

async function load() {
  try {
    config = await GetConfig();
    render();
    els.chkAutoStart.checked = !!config.auto_start;
    els.chkAutoRestore.checked = !!config.auto_restore;
  } catch (e) {
    toast('读取配置失败：' + e);
  }
}

// ---------- 交互 ----------

function bindGridEvents(container) {
  container.addEventListener('click', (e) => {
    const card = e.target.closest('.card');
    if (!card) return;
    const zone = card.dataset.zone;
    const index = Number(card.dataset.index);
    selection = { zone, index };
    renderSelectionOnly();
    const item = (zone === 'manual' ? config.manual : config.captured)[index];
    setStatus(`已选中：${item ? item.name : ''}`);
  });

  container.addEventListener('dblclick', async (e) => {
    const card = e.target.closest('.card');
    if (!card) return;
    const zone = card.dataset.zone;
    const index = Number(card.dataset.index);
    setStatus('正在启动…');
    try {
      await LaunchApp(zone, index);
      setStatus('已启动');
    } catch (err) {
      toast('启动失败：' + err);
      setStatus('就绪');
    }
  });
}

async function onAdd() {
  try {
    const ok = await AddApp();
    if (ok) {
      await load();
      toast('已添加');
    }
  } catch (e) {
    toast('添加失败：' + e);
  }
}

async function onRemove() {
  if (selection.zone === null || selection.index < 0) {
    toast('请先选中一个软件');
    return;
  }
  const zone = selection.zone;
  const list = zone === 'manual' ? config.manual : config.captured;
  const name = list[selection.index] ? list[selection.index].name : '';
  try {
    await RemoveApp(zone, selection.index);
    selection = { zone: null, index: -1 };
    await load();
    toast(`已删除：${name}`);
  } catch (e) {
    toast('删除失败：' + e);
  }
}

async function onSave() {
  try {
    await SaveConfig();
    toast('已保存');
  } catch (e) {
    toast('保存失败：' + e);
  }
}

async function onCapture() {
  setStatus('正在抓取运行中软件…');
  try {
    const n = await CaptureRunning();
    await load();
    toast(`抓取完成，记录 ${n} 个软件`);
    setStatus('就绪');
  } catch (e) {
    toast('抓取失败：' + e);
    setStatus('就绪');
  }
}

async function onLaunchSelected() {
  if (selection.zone === null || selection.index < 0) {
    toast('请先选中一个软件');
    return;
  }
  try {
    await LaunchApp(selection.zone, selection.index);
    toast('已启动');
  } catch (e) {
    toast('启动失败：' + e);
  }
}

async function onLaunchAll() {
  try {
    const msg = await LaunchAll();
    toast(msg);
  } catch (e) {
    toast('启动失败：' + e);
  }
}

// ---------- 拖放 ----------

// 拖放：由 Wails runtime 提供真实文件路径（比 dataTransfer.files 可靠）。
function setupDragDrop() {
  OnFileDrop(async (x, y, paths) => {
    document.body.classList.remove('dragging');
    if (!paths || paths.length === 0) return;
    try {
      const n = await AddPaths(paths);
      await load();
      toast(n > 0 ? `已添加 ${n} 个软件` : '没有新增（可能已存在）');
    } catch (err) {
      toast('拖放添加失败：' + err);
    }
  }, false);

  // 仅做视觉提示，真正的 drop 由 OnFileDrop 处理
  window.addEventListener('dragover', () => document.body.classList.add('dragging'));
  window.addEventListener('dragleave', (e) => {
    if (e.relatedTarget === null) document.body.classList.remove('dragging');
  });
}

// ---------- 启动 ----------

function init() {
  $('btn-add').addEventListener('click', onAdd);
  $('btn-remove').addEventListener('click', onRemove);
  $('btn-save').addEventListener('click', onSave);
  $('btn-capture').addEventListener('click', onCapture);
  $('btn-launch-sel').addEventListener('click', onLaunchSelected);
  $('btn-launch-all').addEventListener('click', onLaunchAll);

  els.chkAutoStart.addEventListener('change', async (e) => {
    try {
      await SetAutoStart(e.target.checked);
      toast(e.target.checked ? '已开启开机自启' : '已关闭开机自启');
    } catch (err) {
      toast('设置失败：' + err);
      e.target.checked = !e.target.checked;
    }
  });

  els.chkAutoRestore.addEventListener('change', async (e) => {
    try {
      await SetAutoRestore(e.target.checked);
      toast(e.target.checked ? '已开启自动恢复' : '已关闭自动恢复');
    } catch (err) {
      toast('设置失败：' + err);
      e.target.checked = !e.target.checked;
    }
  });

  bindGridEvents(els.gridManual);
  bindGridEvents(els.gridCaptured);
  setupDragDrop();

  EventsOn('launch-result', (msg) => toast(msg));

  load();
}

init();
