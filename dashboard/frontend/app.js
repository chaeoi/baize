const state = {
  view: 'display',
  robots: [],
  releases: [],
  selected: null,
  authenticated: false,
  adminUser: 'admin',
  stream: null,
  streamMode: '',
  reconnectTimer: null,
  reconnectAttempt: 0,
  latestEventAt: 0,
  toastTimer: null,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const dashboardPath = window.location.pathname === '/dashboard' || window.location.pathname === '/dashboard/';

document.addEventListener('DOMContentLoaded', boot);

async function boot() {
  lucide.createIcons();
  bindEvents();
  updateClock();
  window.setInterval(updateClock, 1000);
  if (dashboardPath) {
    await bootDashboard();
    return;
  }
  state.view = 'display';
  showApp();
  openStream('public');
}

function bindEvents() {
  $('#login-form').addEventListener('submit', login);
  $('#logout-button').addEventListener('click', logout);
  $('#refresh-button').addEventListener('click', reconnectStream);
  $('#robot-search').addEventListener('input', renderRobotList);
  $('#remark-button').addEventListener('click', openRemark);
  $('#remark-form').addEventListener('submit', saveRemark);
  $('#update-button').addEventListener('click', openUpdate);
  $('#update-form').addEventListener('submit', assignUpdate);
  $('#clear-update-button').addEventListener('click', clearUpdate);
  $('#release-form').addEventListener('submit', uploadRelease);
  $$('.dialog-close').forEach((button) => button.addEventListener('click', () => button.closest('dialog').close()));
}

async function bootDashboard() {
  showLoginGate();
  try {
    const session = await api('/api/v1/session');
    state.authenticated = Boolean(session.authenticated);
    state.adminUser = session.username || 'admin';
    $('#username').value = state.adminUser;
  } catch (error) {
    $('#login-error').textContent = error.message;
  }
  if (state.authenticated) enterDashboard();
  else focusPassword();
}

function showLoginGate() {
  $('#login-gate').classList.remove('hidden');
  $('#app-view').classList.add('hidden');
}

function showApp() {
  $('#login-gate').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  $('#public-view').classList.toggle('hidden', state.view !== 'display');
  $('#settings-view').classList.toggle('hidden', state.view !== 'settings');
  $('#logout-button').classList.toggle('hidden', !state.authenticated);
}

function enterDashboard() {
  state.view = 'settings';
  history.replaceState({}, '', '/dashboard');
  $('#settings-session').textContent = state.adminUser;
  showApp();
  openStream('admin');
  loadReleases();
}

async function api(path, options = {}, requiresLogin = false) {
  const headers = options.body instanceof FormData ? {} : { 'Content-Type': 'application/json' };
  const response = await fetch(path, { credentials: 'same-origin', ...options, headers: { ...headers, ...options.headers } });
  if (response.status === 401 && requiresLogin) {
    state.authenticated = false;
    closeStream();
    showLoginGate();
    focusPassword();
    throw new Error('登录已失效');
  }
  if (!response.ok) {
    let message = response.statusText;
    try { message = (await response.json()).error || message; } catch {}
    throw new Error(message);
  }
  return response.status === 204 ? null : response.json();
}

async function login(event) {
  event.preventDefault();
  $('#login-error').textContent = '';
  try {
    await api('/api/v1/session', { method: 'POST', body: JSON.stringify({ username: $('#username').value, password: $('#password').value }) });
    $('#password').value = '';
    state.authenticated = true;
    enterDashboard();
  } catch (error) {
    $('#login-error').textContent = error.message;
  }
}

async function logout() {
  try { await api('/api/v1/session', { method: 'DELETE' }); } catch {}
  state.authenticated = false;
  closeStream();
  window.location.assign('/');
}

function focusPassword() { window.setTimeout(() => $('#password').focus(), 0); }

function openStream(mode) {
  closeStream(false);
  state.streamMode = mode;
  setConnection('reconnecting', mode === 'admin' ? '后台通道连接中' : '实时通道连接中');
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
  const path = mode === 'admin' ? '/api/v1/admin/ws/robots' : '/api/v1/ws/robots';
  const socket = new WebSocket(`${scheme}://${window.location.host}${path}`);
  state.stream = socket;
  socket.addEventListener('open', () => {
    if (state.stream !== socket) return;
    state.reconnectAttempt = 0;
    setConnection('live', mode === 'admin' ? '后台实时' : '实时连接');
  });
  socket.addEventListener('message', (event) => {
    if (state.stream !== socket) return;
    try { receiveEvent(JSON.parse(event.data), mode); } catch { setConnection('error', '数据格式错误'); }
  });
  socket.addEventListener('error', () => {
    if (state.stream === socket) setConnection('error', '实时通道异常');
  });
  socket.addEventListener('close', () => {
    if (state.stream !== socket) return;
    state.stream = null;
    setConnection('reconnecting', '实时通道重连中');
    scheduleReconnect();
  });
}

function closeStream(schedule = true) {
  if (state.reconnectTimer) {
    clearTimeout(state.reconnectTimer);
    state.reconnectTimer = null;
  }
  const socket = state.stream;
  state.stream = null;
  if (socket) {
    socket.onclose = null;
    socket.close();
  }
  if (schedule && state.streamMode) scheduleReconnect();
}

function reconnectStream() {
  state.reconnectAttempt = 0;
  openStream(state.view === 'settings' ? 'admin' : 'public');
}

function scheduleReconnect() {
  if (state.reconnectTimer || !state.streamMode) return;
  const delay = Math.min(1000 * (2 ** Math.min(state.reconnectAttempt, 4)), 15000);
  state.reconnectAttempt += 1;
  state.reconnectTimer = setTimeout(() => {
    state.reconnectTimer = null;
    openStream(state.streamMode);
  }, delay);
}

function receiveEvent(event, mode) {
  state.latestEventAt = Date.now();
  if (event.type === 'snapshot') state.robots = event.robots || [];
  if (event.type === 'robot' && event.robot) {
    const key = mode === 'admin' ? event.robot.uuid : event.robot.id;
    const index = state.robots.findIndex((robot) => (mode === 'admin' ? robot.uuid : robot.id) === key);
    if (index === -1) state.robots.push(event.robot);
    else state.robots[index] = event.robot;
  }
  if (event.server_time) {
    const serverTime = Date.parse(event.server_time);
    if (Number.isFinite(serverTime)) $('#fleet-latency').textContent = `${Math.max(0, Date.now() - serverTime)} ms`;
  }
  const eventDate = event.robot?.collected_at || event.server_time;
  $('#last-event').textContent = eventDate ? `最新 ${relativeTime(eventDate)}` : '实时数据';
  if (!state.selected || !state.robots.some((robot) => robotKey(robot, mode) === state.selected)) state.selected = state.robots[0] ? robotKey(state.robots[0], mode) : null;
  render();
}

function setConnection(type, label) {
  const stateEl = $('#connection-state');
  stateEl.className = `connection-state ${type}`;
  stateEl.lastElementChild.textContent = label;
}

function render() {
  if (state.view === 'settings') renderSettings();
  else renderPublic();
  lucide.createIcons();
}

function renderPublic() {
  const robots = state.robots;
  const online = robots.filter(isPublicOnline).length;
  const alerts = robots.reduce((total, robot) => total + (robot.summary?.diagnostic_count || 0), 0);
  $('#fleet-total').textContent = robots.length;
  $('#fleet-online').textContent = online;
  $('#fleet-alerts').textContent = alerts;
  $('#fleet-health').textContent = robots.length ? `${Math.round((online / robots.length) * 100)}% 正常运行` : '等待上报';
  $('#directory-count').textContent = robots.length;
  renderRobotList();
  const robot = robots.find((item) => item.id === state.selected);
  $('#empty-state').classList.toggle('hidden', Boolean(robot));
  $('#robot-detail').classList.toggle('hidden', !robot);
  if (robot) renderPublicDetail(robot);
}

function renderRobotList() {
  const query = ($('#robot-search')?.value || '').trim().toLowerCase();
  const robots = state.robots.filter((robot) => [robot.code, robot.model, robot.remark].some((value) => (value || '').toLowerCase().includes(query)));
  $('#robot-list').innerHTML = robots.map((robot) => `
    <button class="robot-item ${robot.id === state.selected ? 'active' : ''}" data-key="${escapeHTML(robot.id)}" type="button">
      <span class="dot ${isPublicOnline(robot) ? 'online' : ''}"></span>
      <span><strong>${escapeHTML(robot.code)}</strong><small>${escapeHTML(robot.remark || robot.model || '未命名设备')}</small></span>
      <time>${relativeTime(robot.last_seen)}</time>
    </button>`).join('') || '<div class="empty-line">没有匹配设备</div>';
  $$('#robot-list .robot-item').forEach((button) => button.addEventListener('click', () => { state.selected = button.dataset.key; render(); }));
}

function renderPublicDetail(robot) {
  const summary = robot.summary || {};
  const battery = summary.battery;
  $('#robot-code').textContent = robot.code || '-';
  $('#robot-model').textContent = robot.model || '未知型号';
  $('#robot-remark').textContent = robot.remark || '未设置备注';
  const online = isPublicOnline(robot);
  $('#robot-status').textContent = online ? '在线运行' : '离线';
  $('#robot-status').classList.toggle('online', online);
  $('#detail-beacon').classList.toggle('online', online);
  $('#detail-updated-text').textContent = `采集于 ${formatDate(robot.collected_at)} · ${relativeTime(robot.last_seen)}收到`;
  setMetric('cpu', summary.cpu_percent, `${fixed(summary.cpu_percent)}%`, `负载 ${fixed(summary.load_1)}`);
  setMetric('memory', summary.memory_percent, `${fixed(summary.memory_percent)}%`, '内存占用');
  setMetric('disk', summary.disk_percent, `${fixed(summary.disk_percent)}%`, '根目录占用');
  setMetric('battery', battery?.online ? battery.soc_percent : NaN, battery?.online ? `${fixed(battery.soc_percent)}%` : '--', battery ? `${fixed(battery.voltage)} V · ${fixed(battery.current)} A` : '未启用');
  $('#system-facts').innerHTML = facts([
    ['负载', fixed(summary.load_1)], ['运行时长', duration(summary.uptime_seconds)], ['采集时间', formatDate(robot.collected_at)], ['状态', online ? '在线运行' : '离线']
  ]);
  const maxTemp = summary.temperature_max;
  const minTemp = summary.temperature_min;
  $('#thermal-summary').innerHTML = maxTemp === undefined ? '<div class="empty-line">无温度数据</div>' : `<div class="thermal-reading ${maxTemp >= 80 ? 'hot' : ''}"><span>最高温度</span><strong>${fixed(maxTemp)} °C</strong></div><div class="thermal-reading"><span>最低温度</span><strong>${fixed(minTemp)} °C</strong></div><div class="thermal-note">设备上报的热传感器摘要</div>`;
  $('#component-facts').innerHTML = facts([
    ['GPU', summary.gpu ? `${fixed(summary.gpu.utilization_percent)}% · ${fixed(summary.gpu.temperature_celsius)} °C` : '无数据'],
    ['电机', `${summary.motor_count || 0} 个 · ${summary.motor_topic_online ? '有数据' : '无数据'}`],
    ['诊断', summary.diagnostic_count ? `${summary.diagnostic_count} 项异常` : '正常'],
    ['电池状态', battery?.online ? (battery.power_supply_status || '在线') : '未启用']
  ]);
}

function renderSettings() {
  $('#settings-count').textContent = state.robots.length;
  $('#settings-robot-list').innerHTML = state.robots.map((robot) => `
    <button class="settings-robot-item ${robot.uuid === state.selected ? 'active' : ''}" data-key="${escapeHTML(robot.uuid)}" type="button">
      <span><strong>${escapeHTML(robot.code)}</strong><small>${escapeHTML(robot.hostname || robot.model || '-')}</small></span><span class="status-label ${isAdminOnline(robot) ? 'online' : ''}">${isAdminOnline(robot) ? '在线' : '离线'}</span>
    </button>`).join('') || '<div class="empty-line">暂无机器人记录</div>';
  $$('#settings-robot-list .settings-robot-item').forEach((button) => button.addEventListener('click', () => { state.selected = button.dataset.key; render(); }));
  const robot = state.robots.find((item) => item.uuid === state.selected);
  $('#settings-robot-empty').classList.toggle('hidden', Boolean(robot));
  $('#settings-robot-panel').classList.toggle('hidden', !robot);
  if (!robot) return;
  const online = isAdminOnline(robot);
  $('#settings-robot-code').textContent = robot.code;
  $('#settings-robot-status').textContent = online ? '在线' : '离线';
  $('#settings-robot-status').classList.toggle('online', online);
  const telemetry = robot.telemetry || {};
  const motor = telemetry.motors || {};
  const bms = telemetry.bms || {};
  $('#settings-identity-facts').innerHTML = facts([
    ['UUID', robot.uuid], ['型号', robot.model], ['主机名', robot.hostname], ['平台', `${robot.os}/${robot.arch}`], ['Agent 版本', robot.agent_version], ['最后上报', formatDate(robot.last_seen)]
  ]);
  $('#settings-config-facts').innerHTML = facts([
    ['系统采集', telemetry.system ? '已启用' : '未上报'], ['CPU 核心', telemetry.system?.cpu_cores || '-'], ['磁盘路径', (telemetry.system?.disks || []).map((disk) => disk.path).join(', ') || '-'],
    ['电机 Topic', motor.topic || '-'], ['电机来源', motor.source || '-'], ['BMS 协议', bms.protocol || '-'], ['BMS 接口', bms.interface || '-'], ['目标版本', robot.desired_version || '跟随发布']
  ]);
  $('#update-button').disabled = !state.releases.some((release) => release.os === robot.os && release.arch === robot.arch);
  $('#settings-session').textContent = state.adminUser;
  renderReleases();
}

function openRemark() {
  const robot = selectedAdminRobot(); if (!robot) return;
  $('#remark-dialog-title').textContent = robot.code;
  $('#remark-input').value = robot.remark || '';
  $('#remark-dialog').showModal();
}

async function saveRemark(event) {
  event.preventDefault();
  const robot = selectedAdminRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${encodeURIComponent(robot.uuid)}/remark`, { method: 'PATCH', body: JSON.stringify({ remark: $('#remark-input').value }) }, true);
    $('#remark-dialog').close(); toast('备注已更新');
  } catch (error) { toast(error.message, true); }
}

function openUpdate() {
  const robot = selectedAdminRobot(); if (!robot) return;
  const versions = state.releases.filter((release) => release.os === robot.os && release.arch === robot.arch);
  $('#update-dialog-title').textContent = `${robot.code} · 指定版本`;
  $('#update-version').innerHTML = versions.map((release) => `<option value="${escapeHTML(release.version)}">${escapeHTML(release.version)} · ${bytes(release.size)}</option>`).join('');
  $('#update-dialog').showModal();
}

async function assignUpdate(event) {
  event.preventDefault();
  const robot = selectedAdminRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${encodeURIComponent(robot.uuid)}/update`, { method: 'POST', body: JSON.stringify({ version: $('#update-version').value }) }, true);
    $('#update-dialog').close(); toast('更新指令已下发');
  } catch (error) { toast(error.message, true); }
}

async function clearUpdate() {
  const robot = selectedAdminRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${encodeURIComponent(robot.uuid)}/update`, { method: 'DELETE' }, true);
    $('#update-dialog').close(); toast('已恢复跟随发布');
  } catch (error) { toast(error.message, true); }
}

async function loadReleases() {
  try {
    const data = await api('/api/v1/admin/releases', {}, true);
    state.releases = data.releases || [];
    render();
  } catch (error) { if (error.message !== '登录已失效') toast(error.message, true); }
}

async function uploadRelease(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  try {
    await api('/api/v1/admin/releases', { method: 'POST', body: form }, true);
    event.currentTarget.reset(); await loadReleases(); toast('Agent 发布已上传');
  } catch (error) { toast(error.message, true); }
}

function renderReleases() {
  if (!$('#release-list')) return;
  $('#release-list').innerHTML = state.releases.map((release) => `<div class="release-row"><strong>${escapeHTML(release.version)}</strong><span>${escapeHTML(release.os)}/${escapeHTML(release.arch)}</span><code>${escapeHTML(release.sha256.slice(0, 12))} · ${bytes(release.size)}</code><button class="icon-button" data-release="${escapeHTML(release.id)}" type="button" title="删除发布" aria-label="删除发布"><i data-lucide="trash-2"></i></button></div>`).join('') || '<div class="empty-line">暂无发布文件</div>';
  $$('#release-list [data-release]').forEach((button) => button.addEventListener('click', () => deleteRelease(button.dataset.release)));
  lucide.createIcons();
}

async function deleteRelease(id) {
  try { await api(`/api/v1/admin/releases/${encodeURIComponent(id)}`, { method: 'DELETE' }, true); await loadReleases(); toast('发布文件已删除'); } catch (error) { toast(error.message, true); }
}

function selectedAdminRobot() { return state.robots.find((robot) => robot.uuid === state.selected); }
function robotKey(robot, mode) { return mode === 'admin' ? robot.uuid : robot.id; }
function isPublicOnline(robot) { return Boolean(robot.online) && Date.now() - Date.parse(robot.last_seen) <= 12000; }
function isAdminOnline(robot) { return Date.now() - Date.parse(robot.last_seen) <= 12000; }

function setMetric(name, value, display, sub) {
  const safe = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Number(value))) : 0;
  $(`#${name}-value`).textContent = display;
  $(`#${name}-sub`).textContent = sub;
  $(`#${name}-meter`).style.width = `${safe}%`;
}

function facts(items) { return items.map(([label, value]) => `<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value === undefined || value === null || value === '' ? '-' : String(value))}</dd></div>`).join(''); }
function updateClock() {
  $('#server-clock').textContent = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  if (state.view === 'display' && state.robots.length) renderPublic();
}
function formatDate(value) { const date = new Date(value); return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('zh-CN', { hour12: false }); }
function relativeTime(value) { const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000)); if (!Number.isFinite(seconds)) return '-'; if (seconds < 2) return '刚刚'; if (seconds < 60) return `${seconds} 秒前`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes} 分钟前`; return `${Math.floor(minutes / 60)} 小时前`; }
function duration(value) { if (!Number.isFinite(Number(value))) return '-'; const seconds = Math.max(0, Math.floor(Number(value))); const days = Math.floor(seconds / 86400); const hours = Math.floor((seconds % 86400) / 3600); const minutes = Math.floor((seconds % 3600) / 60); return days ? `${days} 天 ${hours} 小时` : `${hours} 小时 ${minutes} 分`; }
function fixed(value, digits = 1) { return Number.isFinite(Number(value)) ? Number(value).toFixed(digits) : '-'; }
function bytes(value) { const number = Number(value); if (!Number.isFinite(number) || number <= 0) return '-'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let size = number; let index = 0; while (size >= 1024 && index < units.length - 1) { size /= 1024; index += 1; } return `${size.toFixed(index ? 1 : 0)} ${units[index]}`; }
function escapeHTML(value) { return String(value ?? '').replace(/[&<>'"]/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[character])); }
function toast(message, error = false) { const element = $('#toast'); element.textContent = message; element.className = `toast show${error ? ' error' : ''}`; clearTimeout(state.toastTimer); state.toastTimer = setTimeout(() => { element.className = 'toast'; }, 2600); }
