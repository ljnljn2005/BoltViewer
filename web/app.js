'use strict';

const $ = id => document.getElementById(id);
const state = { info: null, path: null, labels: [], roots: [], nodes: new Map(), offset: 0, limit: 100, matched: 0, query: '', entries: [], value: null, key: null, tab: 'text', epoch: 0, listRequest: 0, valueRequest: 0, busy: false };
const typeLabels = { text: 'TEXT', json: 'JSON', binary: 'BINARY', bucket: 'BUCKET' };
let searchTimer, toastTimer;
const pathID = path => JSON.stringify(path);
const icon = name => {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.classList.add('icon');
  svg.setAttribute('aria-hidden', 'true');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', '#i-' + name);
  svg.append(use);
  return svg;
};
const size = n => {
  if (n < 1024) return n + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toLocaleString('en', { maximumFractionDigits: 1 }) + ' ' + units[i];
};
function apiURL(route, params = {}) {
  const url = new URL('/api/' + route, location.origin);
  for (const [key, val] of Object.entries(params)) url.searchParams.set(key, typeof val === 'object' ? JSON.stringify(val) : val);
  return url.pathname + url.search;
}
async function api(route, params = {}, options = {}) {
  const res = await fetch(apiURL(route, params), options);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || '请求失败');
  return body;
}
function toast(message) {
  clearTimeout(toastTimer);
  $('toast').textContent = message;
  $('toast').hidden = false;
  toastTimer = setTimeout(() => { $('toast').hidden = true; }, 4200);
}
function setStatus(message, busy = false) {
  $('status').textContent = message;
  document.body.classList.toggle('busy', busy);
}
function showError(id, message) { $(id).textContent = message || ''; $(id).hidden = !message; }
function emptyList(title, message) {
  $('list-empty').hidden = false;
  $('list-empty').querySelector('h2').textContent = title;
  $('list-empty').querySelector('p').textContent = message;
}
function clearDetail(message) {
  state.value = null;
  state.key = null;
  state.valueRequest++;
  $('detail').hidden = true;
  $('detail-empty').hidden = false;
  $('value-format').hidden = true;
  $('detail-empty').querySelector('h2').textContent = message || '看看里面有什么';
  $('detail-empty').querySelector('p').textContent = message ? '请稍候' : '选择一条记录，查看文本、JSON 或原始字节。';
}

async function loadDatabase(preserve = false) {
  const saved = preserve && state.path ? { path: [...state.path], labels: [...state.labels], query: state.query } : null;
  const epoch = ++state.epoch;
  clearTimeout(searchTimer);
  state.listRequest++;
  state.path = null;
  state.query = '';
  state.offset = 0;
  state.nodes.clear();
  $('search').value = '';
  $('search').disabled = true;
  $('export').disabled = true;
  $('refresh').disabled = true;
  $('entries').replaceChildren();
  clearDetail();
  showError('list-error', '');
  $('value-count').textContent = $('child-count').textContent = $('sequence').textContent = '—';
  $('prev-page').disabled = $('next-page').disabled = true;
  $('page-label').textContent = '尚未选择 bucket';
  $('match-count').textContent = '0 条记录';
  $('breadcrumbs').textContent = 'DATABASE EXPLORER';
  $('bucket-title').textContent = '浏览你的数据';
  $('bucket-description').textContent = '选择一个 bucket，查看其中的键值。';
  emptyList('读取数据库…', '正在加载 bucket 结构。');
  setStatus('正在读取数据库…', true);
  try {
    const info = await api('info');
    if (epoch !== state.epoch) return;
    state.info = info;
    $('file-name').textContent = info.open ? info.name : '未打开数据库';
    $('file-name').title = info.open ? info.name : '';
    $('db-size').textContent = info.open ? size(info.size) : '—';
    $('page-size').textContent = info.open ? size(info.pageSize) : '—';
    $('db-path').textContent = info.open ? info.path : '本地运行 · 数据留在你的电脑';
    $('db-path').title = info.open ? info.path : '';
    $('root-count').textContent = info.buckets || 0;
    $('upload-limit').textContent = '上传上限 ' + size(info.maxUpload) + ' · 文件仅在本地处理';
    document.title = info.open ? info.name + ' · BoltDB Viewer' : 'BoltDB Viewer · 本地数据库查看器';
    if (!info.open) {
      $('tree').replaceChildren();
      const note = document.createElement('p');
      note.className = 'sidebar-empty'; note.textContent = '打开数据库后，bucket 会显示在这里。'; $('tree').append(note);
      emptyList('打开一个数据库', '点击右上角“打开数据库”，选择文件或输入本地路径。');
      setStatus('就绪 · 请选择数据库');
      return;
    }
    const { items } = await api('buckets');
    if (epoch !== state.epoch) return;
    state.roots = items.map(item => createNode(item, [], []));
    renderTree();
    if (!items.length) {
      emptyList('这是一个空数据库', '数据库中还没有 bucket。');
      setStatus('已连接 · 只读');
      return;
    }
    if (saved) {
      // Restore only paths that still exist after a refresh.
      let nodes = state.roots;
      let target;
      for (const name of saved.path) {
        target = nodes.find(node => node.item.name === name);
        if (!target) break;
        nodes = await loadChildren(target);
        if (epoch !== state.epoch) return;
        target.expanded = nodes.length > 0;
      }
      if (target && pathID(target.path) === pathID(saved.path)) {
        state.query = saved.query; $('search').value = saved.query;
        await selectBucket(target.path, target.labels, true);
        return;
      }
    }
    await selectBucket(state.roots[0].path, state.roots[0].labels);
  } catch (err) {
    if (epoch !== state.epoch) return;
    showError('list-error', err.message);
    emptyList('无法加载数据库', '请刷新重试，或打开另一个数据库。');
    setStatus('读取失败');
  } finally {
    if (epoch === state.epoch) $('refresh').disabled = false;
  }
}

function createNode(item, parentPath, parentLabels) {
  const node = { item, path: [...parentPath, item.name], labels: [...parentLabels, item.display], children: null, expanded: false, loading: false, error: '' };
  state.nodes.set(pathID(node.path), node);
  return node;
}
async function loadChildren(node) {
  if (node.children !== null) return node.children;
  if (node.pending) return node.pending;
  const epoch = state.epoch;
  node.loading = true; node.error = '';
  node.pending = api('buckets', { path: node.path }).then(({ items }) => {
    if (epoch !== state.epoch) return [];
    node.children = items.map(item => createNode(item, node.path, node.labels));
    return node.children;
  }).catch(err => { if (epoch === state.epoch) node.error = err.message; throw err; }).finally(() => {
    node.loading = false; node.pending = null;
    if (epoch === state.epoch) renderTree();
  });
  return node.pending;
}
function renderTree() {
  const fragment = document.createDocumentFragment();
  function append(node, depth) {
    const row = document.createElement('div'); row.className = 'tree-row';
    if (state.path && pathID(node.path) === pathID(state.path)) row.classList.add('selected');
    if (node.expanded) row.classList.add('expanded');
    row.style.paddingLeft = Math.min(depth * 14, 100) + 'px';
    const toggle = document.createElement('button'); toggle.className = 'tree-toggle';
    toggle.setAttribute('aria-label', (node.expanded ? '收起 ' : '展开 ') + node.item.display);
    toggle.setAttribute('aria-expanded', String(node.expanded));
    if (node.children?.length === 0) toggle.disabled = true;
    else toggle.append(icon('chevron'));
    toggle.addEventListener('click', async () => {
      if (node.expanded) { node.expanded = false; renderTree(); return; }
      node.expanded = true; renderTree();
      try { await loadChildren(node); } catch (err) { toast(err.message); }
    });
    const select = document.createElement('button'); select.className = 'tree-select';
    select.title = node.item.display;
    if (row.classList.contains('selected')) select.setAttribute('aria-current', 'true');
    const label = document.createElement('span'); label.textContent = node.item.display;
    select.append(icon('folder'), label);
    select.addEventListener('click', () => { void selectBucket(node.path, node.labels); });
    row.append(toggle, select); fragment.append(row);
    if (node.expanded) {
      if (node.children) node.children.forEach(child => append(child, depth + 1));
      else if (node.loading || node.error) {
        const note = document.createElement('div'); note.className = 'tree-children-note';
        note.textContent = node.error || '加载中…'; fragment.append(note);
      }
    }
  }
  state.roots.forEach(node => append(node, 0));
  $('tree').replaceChildren(fragment);
}
async function selectBucket(path, labels, preserveQuery = false) {
  clearTimeout(searchTimer);
  const changed = !state.path || pathID(path) !== pathID(state.path);
  state.path = [...path]; state.labels = [...labels]; state.offset = 0;
  if (changed && !preserveQuery) { state.query = ''; $('search').value = ''; }
  $('search').disabled = false; $('export').disabled = false;
  $('bucket-title').textContent = labels[labels.length - 1];
  $('bucket-description').textContent = '按原始键的字节顺序排列 · 只读浏览';
  const crumbs = document.createDocumentFragment();
  const root = document.createElement('span'); root.textContent = state.info.name; crumbs.append(root);
  labels.forEach((label, i) => {
    const button = document.createElement('button'); button.textContent = label; button.title = label;
    button.addEventListener('click', () => { void selectBucket(path.slice(0, i + 1), labels.slice(0, i + 1)); });
    crumbs.append(icon('chevron'), button);
  });
  $('breadcrumbs').replaceChildren(crumbs);
  renderTree();
  void loadChildren(state.nodes.get(pathID(path)) || createNode({ name: path.at(-1), display: labels.at(-1) }, path.slice(0, -1), labels.slice(0, -1))).catch(err => toast(err.message));
  await loadEntries();
}
async function loadEntries() {
  const request = ++state.listRequest;
  const epoch = state.epoch;
  const path = [...state.path];
  clearDetail();
  $('entries').replaceChildren();
  $('prev-page').disabled = $('next-page').disabled = true;
  $('page-label').textContent = '正在读取…';
  showError('list-error', '');
  emptyList('正在读取…', '加载当前 bucket 的键值记录。');
  setStatus('读取键值记录…', true);
  try {
    const data = await api('entries', { path, q: state.query, offset: state.offset, limit: state.limit });
    if (request !== state.listRequest || epoch !== state.epoch) return;
    state.entries = data.items; state.matched = data.matched;
    $('value-count').textContent = data.values.toLocaleString();
    $('child-count').textContent = data.buckets.toLocaleString();
    $('sequence').textContent = data.sequence;
    $('sequence').title = data.sequence;
    $('match-count').textContent = state.query ? data.matched.toLocaleString() + ' / ' + data.total.toLocaleString() + ' 条' : data.total.toLocaleString() + ' 条记录';
    renderEntries();
    $('list-empty').hidden = data.items.length > 0;
    if (!data.items.length) emptyList(state.query ? '没有匹配的键' : '这个 bucket 是空的', state.query ? '试试其他键名，或输入键的十六进制片段。' : '请选择左侧其他 bucket。');
    $('page-label').textContent = data.matched ? (state.offset + 1) + '–' + Math.min(state.offset + state.limit, data.matched) + ' / ' + data.matched.toLocaleString() : '0 条记录';
    $('prev-page').disabled = state.offset === 0;
    $('next-page').disabled = state.offset + state.limit >= data.matched;
    setStatus('已连接 · 只读 · ' + size(state.info.size));
    const first = data.items.find(item => item.kind === 'value');
    if (first) void selectValue(first);
  } catch (err) {
    if (request !== state.listRequest || epoch !== state.epoch) return;
    state.entries = [];
    showError('list-error', err.message);
    emptyList('无法读取 bucket', '请刷新数据库后重试。');
    $('page-label').textContent = '读取失败';
    setStatus('读取失败');
  }
}
function renderEntries() {
  const fragment = document.createDocumentFragment();
  state.entries.forEach(item => {
    const row = document.createElement('tr'); row.tabIndex = 0;
    row.setAttribute('aria-label', (item.kind === 'bucket' ? '打开 bucket ' : '查看记录 ') + item.display);
    if (item.key === state.key) row.className = 'selected';
    const keyCell = document.createElement('td');
    const key = document.createElement('div'); key.className = 'entry-key';
    if (item.kind === 'bucket') key.append(icon('folder'));
    const name = document.createElement('span'); name.textContent = item.display; name.title = item.display; key.append(name);
    const preview = document.createElement('div'); preview.className = 'entry-preview'; preview.textContent = item.preview || (item.kind === 'value' ? '空值' : '');
    keyCell.append(key, preview);
    const typeCell = document.createElement('td'); const badge = document.createElement('span'); badge.className = 'badge type-' + item.format; badge.textContent = typeLabels[item.format]; typeCell.append(badge);
    const sizeCell = document.createElement('td'); sizeCell.textContent = item.kind === 'bucket' ? '—' : size(item.size);
    row.append(keyCell, typeCell, sizeCell);
    const activate = () => {
      if (item.kind === 'bucket') void selectBucket([...state.path, item.key], [...state.labels, item.display]);
      else void selectValue(item);
    };
    row.addEventListener('click', activate);
    row.addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); activate(); } });
    fragment.append(row);
  });
  $('entries').replaceChildren(fragment);
}
async function selectValue(item) {
  clearDetail('正在读取值…');
  state.key = item.key;
  const request = ++state.valueRequest;
  const epoch = state.epoch;
  renderEntries();
  try {
    const val = await api('value', { path: state.path, key: item.key });
    if (request !== state.valueRequest || epoch !== state.epoch) return;
    state.value = val;
    state.tab = val.format === 'binary' ? 'hex' : val.format === 'json' && !val.truncated ? 'json' : 'text';
    $('detail-empty').hidden = true; $('detail').hidden = false;
    $('selected-key').textContent = val.keyDisplay;
    $('key-size').textContent = val.keySize + ' 字节 · ' + (val.keyDisplay.startsWith('0x') ? '二进制键以 HEX 显示' : 'UTF-8');
    $('value-size').textContent = val.size.toLocaleString() + ' 字节';
    $('value-format').className = 'badge type-' + val.format;
    $('value-format').textContent = typeLabels[val.format]; $('value-format').hidden = false;
    renderValue();
  } catch (err) {
    if (request !== state.valueRequest || epoch !== state.epoch) return;
    clearDetail();
    $('detail-empty').querySelector('h2').textContent = '读取值失败';
    $('detail-empty').querySelector('p').textContent = err.message;
    toast(err.message);
  }
}
function decodeBytes(base64) { return Uint8Array.from(atob(base64), c => c.charCodeAt(0)); }
function formatHex(bytes) {
  const lines = [];
  for (let i = 0; i < bytes.length; i += 8) {
    const chunk = bytes.subarray(i, i + 8);
    const hex = Array.from(chunk, b => b.toString(16).padStart(2, '0')).join(' ').padEnd(23, ' ');
    const ascii = Array.from(chunk, b => b >= 32 && b < 127 ? String.fromCharCode(b) : '.').join('');
    lines.push(i.toString(16).padStart(8, '0') + '  ' + hex + '  ' + ascii);
  }
  return lines.join('\n');
}
// Format valid JSON without converting numeric literals to JavaScript Numbers.
function prettyJSON(text) {
  let result = '', depth = 0, inString = false, escaped = false, previous = '';
  const compact = text.trim();
  for (let i = 0; i < compact.length; i++) {
    const c = compact[i];
    if (inString) {
      result += c;
      if (escaped) escaped = false;
      else if (c === '\\') escaped = true;
      else if (c === '"') inString = false;
      previous = c;
      continue;
    }
    if (/\s/.test(c)) continue;
    if (c === '"') { inString = true; result += c; }
    else if (c === '{' || c === '[') {
      result += c; depth++;
      const next = compact.slice(i + 1).trimStart()[0];
      if (next !== '}' && next !== ']') result += '\n' + '  '.repeat(Math.min(depth, 32));
    } else if (c === '}' || c === ']') {
      depth--;
      if (previous !== '{' && previous !== '[') result += '\n' + '  '.repeat(Math.min(depth, 32));
      result += c;
    } else if (c === ',') result += ',\n' + '  '.repeat(Math.min(depth, 32));
    else if (c === ':') result += ': ';
    else result += c;
    previous = c;
  }
  return result;
}
function formattedValue(clip = false) {
  const val = state.value;
  if (state.tab === 'base64') return clip ? val.data.slice(0, 65536) : val.data;
  if (state.tab === 'hex') {
    const bytes = decodeBytes(val.data);
    return formatHex(clip ? bytes.subarray(0, 65536) : bytes);
  }
  if (state.tab === 'json') {
    const text = prettyJSON(val.text); return clip ? text.slice(0, 65536) : text;
  }
  const text = val.format === 'binary' ? new TextDecoder().decode(decodeBytes(val.data)) : val.text;
  return clip ? text.slice(0, 65536) : text;
}
function renderValue() {
  if (!state.value) return;
  const val = state.value;
  document.querySelectorAll('[data-tab]').forEach(button => {
    button.disabled = button.dataset.tab === 'json' && (val.format !== 'json' || val.truncated);
    const active = button.dataset.tab === state.tab;
    button.setAttribute('aria-selected', String(active)); button.tabIndex = active ? 0 : -1;
  });
  const content = formattedValue(true);
  $('value-content').textContent = content || (val.size === 0 ? '（空值 · 0 字节）' : '');
  $('value-content').classList.toggle('hex-view', state.tab === 'hex');
  $('value-content').scrollTop = 0;
  const notes = [];
  if (val.truncated) notes.push('服务端预览仅包含前 1 MiB');
  if (state.tab === 'hex' ? decodeBytes(val.data).length > 65536 : formattedValue().length > 65536) notes.push('界面仅显示前 64 KiB 内容');
  if (state.tab === 'text' && val.format === 'binary') notes.push('二进制值使用 UTF-8 替换字符预览');
  if (notes.length && (val.truncated || val.size > 65536)) notes.push('下载原始值获取完整数据');
  $('value-note').textContent = notes.join('；'); $('value-note').hidden = !notes.length;
}
async function copy(text) {
  try { await navigator.clipboard.writeText(text); toast('已复制到剪贴板'); }
  catch { toast('复制失败，请在内容中手动选择并复制'); }
}
function download(route, params) {
  const a = document.createElement('a'); a.href = apiURL(route, params); a.download = ''; document.body.append(a); a.click(); a.remove();
}

function showOpenDialog() {
  if (state.busy) return;
  showError('open-error', ''); $('open-progress').textContent = '原始数据库不会被修改';
  $('open-dialog').showModal();
}
function setOpenBusy(busy) {
  state.busy = busy;
  ['path-input', 'path-submit', 'file-input', 'close-dialog'].forEach(id => { $(id).disabled = busy; });
  $('drop-zone').setAttribute('aria-disabled', String(busy));
  $('path-submit').textContent = busy ? '正在打开…' : '打开文件';
}
async function openDatabase(options, route) {
  if (state.busy) return;
  showError('open-error', ''); setOpenBusy(true);
  $('open-progress').textContent = route === 'upload' ? '正在上传本地副本…' : '正在打开数据库…';
  try {
    await api(route, {}, { method: 'POST', ...options });
    $('open-dialog').close();
    await loadDatabase();
    toast('数据库已打开 · 只读模式');
  } catch (err) {
    showError('open-error', err.message); $('open-progress').textContent = '原始数据库不会被修改';
  } finally { setOpenBusy(false); $('file-input').value = ''; }
}
function uploadFile(file) {
  if (!file || state.busy) return;
  if (state.info && file.size > state.info.maxUpload) { showError('open-error', '文件超过上传上限 ' + size(state.info.maxUpload) + '，请使用本地路径打开。'); return; }
  const body = new FormData(); body.append('file', file);
  void openDatabase({ body }, 'upload');
}

$('open-db').addEventListener('click', showOpenDialog);
$('close-dialog').addEventListener('click', () => $('open-dialog').close());
$('open-dialog').addEventListener('cancel', e => { if (state.busy) e.preventDefault(); });
$('path-form').addEventListener('submit', e => {
  e.preventDefault(); const path = $('path-input').value.trim();
  if (!path) { showError('open-error', '请输入数据库文件的本地路径。'); $('path-input').focus(); return; }
  void openDatabase({ headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path }) }, 'open');
});
$('file-input').addEventListener('change', e => uploadFile(e.target.files[0]));
$('drop-zone').addEventListener('keydown', e => { if (!state.busy && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); $('file-input').click(); } });
$('drop-zone').addEventListener('dragover', e => { e.preventDefault(); if (!state.busy) $('drop-zone').classList.add('drag-over'); });
$('drop-zone').addEventListener('dragleave', () => $('drop-zone').classList.remove('drag-over'));
$('drop-zone').addEventListener('drop', e => { e.preventDefault(); $('drop-zone').classList.remove('drag-over'); uploadFile(e.dataTransfer.files[0]); });
$('refresh').addEventListener('click', () => { void loadDatabase(true); });
$('search').addEventListener('input', () => {
  clearTimeout(searchTimer);
  state.query = $('search').value; state.offset = 0;
  // Invalidate pending results before the debounce to avoid rendering stale search results.
  state.listRequest++; clearDetail();
  searchTimer = setTimeout(() => { if (state.path) void loadEntries(); }, 220);
});
$('prev-page').addEventListener('click', () => { state.offset = Math.max(0, state.offset - state.limit); void loadEntries(); });
$('next-page').addEventListener('click', () => { state.offset += state.limit; void loadEntries(); });
$('export').addEventListener('click', () => { if (state.path) { download('export', { path: state.path }); toast('正在导出当前 bucket，包含所有子 bucket'); } });
$('copy-key').addEventListener('click', () => { if (state.value) void copy(state.value.keyDisplay); });
$('copy-value').addEventListener('click', () => {
  if (!state.value) return;
  if (state.value.truncated) { toast('该值超过预览上限，请下载原始值获取完整内容'); return; }
  void copy(formattedValue());
});
$('download-value').addEventListener('click', () => { if (state.value) download('value', { path: state.path, key: state.key, download: 1 }); });
document.querySelectorAll('[data-tab]').forEach(button => {
  button.addEventListener('click', () => { state.tab = button.dataset.tab; renderValue(); });
  button.addEventListener('keydown', e => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    const tabs = [...document.querySelectorAll('[data-tab]')].filter(tab => !tab.disabled);
    let i = tabs.indexOf(button);
    if (e.key === 'Home') i = 0; else if (e.key === 'End') i = tabs.length - 1; else i = (i + (e.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    e.preventDefault(); tabs[i].click(); tabs[i].focus();
  });
});
document.addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); if (!state.busy && !$('open-dialog').open) $('search').focus(); }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'o') { e.preventDefault(); if (!$('open-dialog').open) showOpenDialog(); }
});
void loadDatabase();
