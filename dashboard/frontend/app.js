const state = {
  view: 'display',
  robots: [],
  releases: [],
  selected: null,
  authenticated: false,
  passwordChangeRequired: false,
  adminUser: 'admin',
  stream: null,
  streamMode: '',
  reconnectTimer: null,
  reconnectAttempt: 0,
  latestEventAt: 0,
  toastTimer: null,
  history: [],
  historyRobot: null,
  historyLoading: false,
  publicHistory: [],
  publicHistoryRobot: null,
  publicHistoryLoading: false,
  publicHistoryMode: 'host',
  publicHistoryMotor: '',
  publicHistoryMetric: 'torque_nm',
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
  $('#change-password-button').addEventListener('click', () => showPasswordGate(false));
  $('#password-change-form').addEventListener('submit', changePassword);
  $('#cancel-password-change').addEventListener('click', cancelPasswordChange);
  $('#refresh-button').addEventListener('click', reconnectStream);
  $('#robot-search').addEventListener('input', renderRobotList);
  $('#remark-button').addEventListener('click', openRemark);
  $('#remark-form').addEventListener('submit', saveRemark);
  $('#update-button').addEventListener('click', openUpdate);
  $('#update-form').addEventListener('submit', assignUpdate);
  $('#clear-update-button').addEventListener('click', clearUpdate);
  $('#release-form').addEventListener('submit', uploadRelease);
  $('#delete-robot-button').addEventListener('click', openDeleteRobot);
  $('#delete-robot-form').addEventListener('submit', deleteRobot);
  $('#history-range').addEventListener('change', loadHistory);
  $('#public-history-range').addEventListener('change', () => loadPublicHistory(selectedPublicRobot()));
  $('#public-metric-select').addEventListener('change', (event) => { state.publicHistoryMetric = event.target.value; drawPublicHistory(state.publicHistory); });
  $('#public-motor-select').addEventListener('change', (event) => { state.publicHistoryMotor = event.target.value; drawPublicHistory(state.publicHistory); });
  $$('[data-public-mode]').forEach((button) => button.addEventListener('click', () => setPublicHistoryMode(button.dataset.publicMode)));
  window.addEventListener('resize', () => { drawHistoryChart(state.history); drawPublicHistory(state.publicHistory); });
  $$('.dialog-close').forEach((button) => button.addEventListener('click', () => button.closest('dialog').close()));
}

async function bootDashboard() {
  showLoginGate();
  try {
    const session = await api('/api/v1/session');
    state.authenticated = Boolean(session.authenticated);
    state.passwordChangeRequired = Boolean(session.password_change_required);
    state.adminUser = session.username || 'admin';
    $('#username').value = state.adminUser;
  } catch (error) {
    $('#login-error').textContent = error.message;
  }
  if (state.authenticated && state.passwordChangeRequired) showPasswordGate(true);
  else if (state.authenticated) enterDashboard();
  else focusPassword();
}

function showLoginGate() {
  $('#login-gate').classList.remove('hidden');
  $('#password-change-gate').classList.add('hidden');
  $('#app-view').classList.add('hidden');
}

function showPasswordGate(forced = true) {
  state.passwordChangeRequired = forced;
  $('#login-gate').classList.add('hidden');
  $('#password-change-gate').classList.remove('hidden');
  $('#app-view').classList.add('hidden');
  $('#password-change-copy').textContent = forced ? '继续前必须替换初始密码。' : '修改后，其他管理会话将立即失效。';
  $('#cancel-password-change').classList.toggle('hidden', forced);
  $('#password-change-error').textContent = '';
  $('#password-change-form').reset();
  window.setTimeout(() => $('#current-password').focus(), 0);
}

function showApp() {
  $('#login-gate').classList.add('hidden');
  $('#password-change-gate').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  $('#public-view').classList.toggle('hidden', state.view !== 'display');
  $('#settings-view').classList.toggle('hidden', state.view !== 'settings');
  $('#logout-button').classList.toggle('hidden', !state.authenticated);
  $('#change-password-button').classList.toggle('hidden', !state.authenticated || state.view !== 'settings');
}

function enterDashboard() {
  state.view = 'settings';
  state.passwordChangeRequired = false;
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
    if (response.status === 403 && message === 'password change required') {
      state.passwordChangeRequired = true;
      closeStream(false);
      showPasswordGate(true);
      throw new Error('继续前必须修改管理密码');
    }
    throw new Error(message);
  }
  return response.status === 204 ? null : response.json();
}

async function login(event) {
  event.preventDefault();
  $('#login-error').textContent = '';
  try {
    const session = await api('/api/v1/session', { method: 'POST', body: JSON.stringify({ username: $('#username').value, password: $('#password').value }) });
    const password = $('#password').value;
    $('#password').value = '';
    state.authenticated = true;
    state.passwordChangeRequired = Boolean(session.password_change_required);
    if (state.passwordChangeRequired) {
      showPasswordGate(true);
      $('#current-password').value = password;
      $('#new-password').focus();
    } else enterDashboard();
  } catch (error) {
    $('#login-error').textContent = error.message;
  }
}

async function changePassword(event) {
  event.preventDefault();
  const current = $('#current-password').value;
  const next = $('#new-password').value;
  if (next !== $('#confirm-password').value) {
    $('#password-change-error').textContent = '两次输入的新密码不一致';
    return;
  }
  try {
    await api('/api/v1/admin/password', { method: 'POST', body: JSON.stringify({ current_password: current, new_password: next }) }, true);
    state.passwordChangeRequired = false;
    $('#password-change-form').reset();
    enterDashboard();
    toast('管理密码已更新');
  } catch (error) {
    $('#password-change-error').textContent = error.message;
  }
}

function cancelPasswordChange() {
  if (state.passwordChangeRequired) return;
  showApp();
}

async function logout() {
  try { await api('/api/v1/session', { method: 'DELETE' }); } catch {}
  state.authenticated = false;
  state.passwordChangeRequired = false;
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
  if (event.type === 'removed') {
    const key = mode === 'admin' ? event.uuid : event.id;
    state.robots = state.robots.filter((robot) => robotKey(robot, mode) !== key);
  }
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
  if (mode === 'admin' && state.selected && state.historyRobot !== state.selected && !state.historyLoading) loadHistory();
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
  $$('#robot-list .robot-item').forEach((button) => button.addEventListener('click', () => {
    if (state.selected !== button.dataset.key) { state.publicHistory = []; state.publicHistoryRobot = null; }
    state.selected = button.dataset.key; render();
  }));
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
  const hasTelemetry = Boolean(summary.has_telemetry);
  setMetric('cpu', hasTelemetry ? summary.cpu_percent : NaN, hasTelemetry ? `${fixed(summary.cpu_percent)}%` : '--', hasTelemetry ? `负载 ${fixed(summary.load_1)}` : '等待新数据');
  setMetric('memory', hasTelemetry ? summary.memory_percent : NaN, hasTelemetry ? `${fixed(summary.memory_percent)}%` : '--', hasTelemetry ? '内存占用' : '等待新数据');
  setMetric('disk', hasTelemetry ? summary.disk_percent : NaN, hasTelemetry ? `${fixed(summary.disk_percent)}%` : '--', hasTelemetry ? '根目录占用' : '等待新数据');
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
    ['电池状态', battery?.online ? powerStatusLabel(battery.power_supply_status) : '未启用']
  ]);
  renderPublicHistoryControls(robot);
  if (state.publicHistoryRobot !== robot.id && !state.publicHistoryLoading) loadPublicHistory(robot);
  else drawPublicHistory(state.publicHistory);
}

function selectedPublicRobot() { return state.robots.find((robot) => robot.id === state.selected); }

function renderPublicHistoryControls() {
  const motors = new Map();
  state.publicHistory.forEach((point) => (point.motors || []).forEach((motor) => motors.set(motor.id, motor.label || motor.id)));
  const motorSelect = $('#public-motor-select');
  const current = state.publicHistoryMotor;
  motorSelect.innerHTML = [...motors.entries()].map(([id, label]) => `<option value="${escapeHTML(id)}">${escapeHTML(label)}</option>`).join('');
  if (motors.size && (!current || !motors.has(current))) state.publicHistoryMotor = motors.keys().next().value;
  motorSelect.value = state.publicHistoryMotor;
  motorSelect.classList.toggle('hidden', state.publicHistoryMode !== 'single' || !motors.size);
  $('#public-metric-select').classList.toggle('hidden', state.publicHistoryMode !== 'motors' || !motors.size);
  $$('[data-public-mode]').forEach((button) => button.classList.toggle('active', button.dataset.publicMode === state.publicHistoryMode));
}

function setPublicHistoryMode(mode) {
  state.publicHistoryMode = mode;
  renderPublicHistoryControls();
  drawPublicHistory(state.publicHistory);
}

async function loadPublicHistory(robot) {
  if (!robot || state.publicHistoryLoading) return;
  state.publicHistoryLoading = true;
  $('#public-history-empty').textContent = '正在读取历史采样';
  $('#public-history-empty').classList.remove('hidden');
  try {
    const hours = Number($('#public-history-range').value) || 24;
    const data = await api(`/api/v1/robots/${encodeURIComponent(robot.id)}/history?hours=${hours}`);
    if (state.selected !== robot.id) return;
    state.publicHistory = data.points || [];
    state.publicHistoryRobot = robot.id;
    renderPublicHistoryControls();
    drawPublicHistory(state.publicHistory);
  } catch (error) {
    $('#public-history-empty').textContent = error.message;
  } finally {
    state.publicHistoryLoading = false;
    const selected = selectedPublicRobot();
    if (selected && selected.id !== robot.id) window.queueMicrotask(() => loadPublicHistory(selected));
  }
}

function renderSettings() {
  $('#settings-count').textContent = state.robots.length;
  $('#settings-robot-list').innerHTML = state.robots.map((robot) => `
    <button class="settings-robot-item ${robot.uuid === state.selected ? 'active' : ''}" data-key="${escapeHTML(robot.uuid)}" type="button">
      <span><strong>${escapeHTML(robot.code)}</strong><small>${escapeHTML(robot.hostname || robot.model || '-')}</small></span><span class="status-label ${isAdminOnline(robot) ? 'online' : ''}">${isAdminOnline(robot) ? '在线' : '离线'}</span>
    </button>`).join('') || '<div class="empty-line">暂无机器人记录</div>';
  $$('#settings-robot-list .settings-robot-item').forEach((button) => button.addEventListener('click', () => selectAdminRobot(button.dataset.key)));
  const robot = state.robots.find((item) => item.uuid === state.selected);
  $('#settings-robot-empty').classList.toggle('hidden', Boolean(robot));
  $('#settings-robot-panel').classList.toggle('hidden', !robot);
  $('#history-panel').classList.toggle('hidden', !robot);
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
  if (state.historyRobot === robot.uuid) drawHistoryChart(state.history);
}

function selectAdminRobot(uuid) {
  state.selected = uuid;
  state.history = [];
  state.historyRobot = null;
  render();
  loadHistory();
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

function openDeleteRobot() {
  const robot = selectedAdminRobot(); if (!robot) return;
  $('#delete-robot-title').textContent = `删除 ${robot.code}`;
  $('#delete-robot-dialog').showModal();
}

async function deleteRobot(event) {
  event.preventDefault();
  const robot = selectedAdminRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${encodeURIComponent(robot.uuid)}`, { method: 'DELETE' }, true);
    $('#delete-robot-dialog').close();
    state.robots = state.robots.filter((item) => item.uuid !== robot.uuid);
    state.selected = state.robots[0]?.uuid || null;
    state.history = [];
    state.historyRobot = null;
    render();
    if (state.selected) loadHistory();
    toast('设备及其历史已删除');
  } catch (error) { toast(error.message, true); }
}

async function loadHistory() {
  const robot = selectedAdminRobot();
  if (!robot || state.historyLoading) return;
  state.historyLoading = true;
  $('#history-empty').textContent = '正在读取历史采样';
  $('#history-empty').classList.remove('hidden');
  try {
    const hours = Number($('#history-range').value) || 24;
    const data = await api(`/api/v1/admin/robots/${encodeURIComponent(robot.uuid)}/history?hours=${hours}`, {}, true);
    if (state.selected !== robot.uuid) return;
    state.history = data.points || [];
    state.historyRobot = robot.uuid;
    drawHistoryChart(state.history);
  } catch (error) {
    $('#history-empty').textContent = error.message;
    toast(error.message, true);
  } finally {
    state.historyLoading = false;
  }
}

function drawHistoryChart(points) {
  const canvas = $('#history-chart');
  if (!canvas || $('#history-panel').classList.contains('hidden')) return;
  const available = points.filter((point) => [point.cpu_percent, point.memory_percent, point.battery_soc_percent].some((value) => Number.isFinite(Number(value))));
  $('#history-empty').classList.toggle('hidden', available.length > 1);
  if (available.length <= 1) {
    $('#history-empty').textContent = '等待形成历史采样';
    $('#history-meta').textContent = available.length ? '已有 1 个采样点' : '暂无历史采样';
    return;
  }
  const bounds = canvas.parentElement.getBoundingClientRect();
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  const width = Math.max(300, Math.floor(bounds.width));
  const height = Math.max(190, Math.floor(bounds.height));
  canvas.width = Math.floor(width * ratio);
  canvas.height = Math.floor(height * ratio);
  const context = canvas.getContext('2d');
  context.scale(ratio, ratio);
  context.clearRect(0, 0, width, height);
  const padding = { top: 15, right: 16, bottom: 28, left: 38 };
  const chartWidth = width - padding.left - padding.right;
  const chartHeight = height - padding.top - padding.bottom;
  context.strokeStyle = '#dfe6e8';
  context.fillStyle = '#73818a';
  context.font = '11px system-ui';
  context.lineWidth = 1;
  for (let value = 0; value <= 100; value += 25) {
    const y = padding.top + chartHeight * (1 - value / 100);
    context.beginPath(); context.moveTo(padding.left, y); context.lineTo(width - padding.right, y); context.stroke();
    context.fillText(`${value}%`, 4, y + 4);
  }
  const series = [
    ['cpu_percent', '#2376bc'], ['memory_percent', '#65758b'], ['battery_soc_percent', '#0d8d65']
  ];
  series.forEach(([field, color]) => {
    context.strokeStyle = color; context.lineWidth = 2; context.lineJoin = 'round'; context.lineCap = 'round'; context.beginPath();
    let started = false;
    available.forEach((point, index) => {
      const value = Number(point[field]);
      if (!Number.isFinite(value)) { started = false; return; }
      const x = padding.left + chartWidth * (index / (available.length - 1));
      const y = padding.top + chartHeight * (1 - Math.max(0, Math.min(100, value)) / 100);
      if (!started) { context.moveTo(x, y); started = true; } else context.lineTo(x, y);
    });
    context.stroke();
  });
  const first = new Date(available[0].at);
  const last = new Date(available[available.length - 1].at);
  context.fillStyle = '#73818a';
  context.fillText(first.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }), padding.left, height - 7);
  const lastLabel = last.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  context.fillText(lastLabel, width - padding.right - context.measureText(lastLabel).width, height - 7);
  $('#history-meta').textContent = `${available.length} 个采样点 · ${relativeTime(available[available.length - 1].at)}更新`;
}

function drawPublicHistory(points) {
  const canvas = $('#public-history-chart');
  if (!canvas || !selectedPublicRobot()) return;
  const mode = state.publicHistoryMode;
  const available = points.filter((point) => mode === 'host' ? Number.isFinite(Number(point.cpu_percent)) || Number.isFinite(Number(point.memory_percent)) : (point.motors || []).length);
  const empty = $('#public-history-empty');
  empty.classList.toggle('hidden', available.length > 1);
  if (available.length <= 1) {
    empty.textContent = available.length ? '已有 1 个采样点' : '暂无历史采样';
    $('#public-history-meta').textContent = available.length ? '已有 1 个采样点' : '暂无历史采样';
    $('#public-chart-legend').innerHTML = '';
    return;
  }
  const bounds = canvas.parentElement.getBoundingClientRect();
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  const width = Math.max(300, Math.floor(bounds.width));
  const height = Math.max(190, Math.floor(bounds.height));
  canvas.width = Math.floor(width * ratio); canvas.height = Math.floor(height * ratio);
  const context = canvas.getContext('2d'); context.setTransform(ratio, 0, 0, ratio, 0, 0); context.clearRect(0, 0, width, height);
  const padding = { top: 17, right: 16, bottom: 28, left: 43 }; const chartWidth = width - padding.left - padding.right; const chartHeight = height - padding.top - padding.bottom;
  context.strokeStyle = '#dfe6e8'; context.fillStyle = '#73818a'; context.font = '11px system-ui'; context.lineWidth = 1;
  for (let value = 0; value <= 100; value += 25) { const y = padding.top + chartHeight * (1 - value / 100); context.beginPath(); context.moveTo(padding.left, y); context.lineTo(width - padding.right, y); context.stroke(); const axisLabel = mode === 'host' ? `${value}%` : value === 100 ? '高' : value === 0 ? '低' : ''; if (axisLabel) context.fillText(axisLabel, 5, y + 4); }
  let series = [];
  if (mode === 'host') series = [['cpu_percent', 'CPU', '#2376bc'], ['memory_percent', '内存', '#65758b'], ['battery_soc_percent', '电池', '#0d8d65']];
  else if (mode === 'motors') {
    const ids = [...new Map(points.flatMap((point) => (point.motors || []).map((motor) => [motor.id, motor.label || motor.id]))).entries()];
    series = ids.map(([id, label], index) => [`motor:${id}`, label, `hsl(${(index * 37) % 360} 58% 42%)`]);
  } else series = [['position_rad', '位置', '#8a5db8'], ['velocity_rad_per_sec', '速度', '#2376bc'], ['torque_nm', '转矩', '#c17a20']];
  $('#public-chart-legend').innerHTML = series.map((item, index) => {
    const range = mode === 'host' ? '' : formatSeriesRange(points, item[0]);
    return `<span><i class="series-swatch series-color-${index % 32}"></i>${escapeHTML(item[1])}${range ? `<small>${escapeHTML(range)}</small>` : ''}</span>`;
  }).join('');
  series.forEach(([field, label, color]) => {
    context.strokeStyle = color; context.lineWidth = 2; context.lineJoin = 'round'; context.lineCap = 'round'; context.beginPath(); let started = false;
    available.forEach((point, index) => {
      let value;
      if (field.startsWith('motor:')) value = Number((point.motors || []).find((motor) => motor.id === field.slice(6))?.[state.publicHistoryMetric]);
      else if (mode === 'single') value = Number((point.motors || []).find((motor) => motor.id === state.publicHistoryMotor)?.[field]);
      else value = Number(point[field]);
      if (!Number.isFinite(value)) { started = false; return; }
      const x = padding.left + chartWidth * (index / (available.length - 1)); const normalized = mode === 'host' ? value : normalizeSeriesValue(points, field, value); const y = padding.top + chartHeight * (1 - Math.max(0, Math.min(100, normalized)) / 100);
      if (!started) { context.moveTo(x, y); started = true; } else context.lineTo(x, y);
    }); context.stroke();
  });
  const first = new Date(available[0].at); const last = new Date(available[available.length - 1].at); context.fillStyle = '#73818a';
  const firstLabel = first.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }); const lastLabel = last.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  context.fillText(firstLabel, padding.left, height - 7); context.fillText(lastLabel, width - padding.right - context.measureText(lastLabel).width, height - 7); $('#public-history-meta').textContent = `${available.length} 个采样点 · ${relativeTime(available[available.length - 1].at)}更新`;
}

function normalizeSeriesValue(points, field, value) {
  const values = points.flatMap((point) => {
    if (field.startsWith('motor:')) return Number((point.motors || []).find((motor) => motor.id === field.slice(6))?.[state.publicHistoryMetric]);
    return Number((point.motors || []).find((motor) => motor.id === state.publicHistoryMotor)?.[field]);
  }).filter(Number.isFinite);
  if (!values.length) return 50;
  const min = Math.min(...values); const max = Math.max(...values);
  return max === min ? 50 : ((value - min) / (max - min)) * 100;
}

function formatSeriesRange(points, field) {
  const values = points.flatMap((point) => {
    if (field.startsWith('motor:')) return Number((point.motors || []).find((motor) => motor.id === field.slice(6))?.[state.publicHistoryMetric]);
    return Number((point.motors || []).find((motor) => motor.id === state.publicHistoryMotor)?.[field]);
  }).filter(Number.isFinite);
  if (!values.length) return '';
  const metric = field.startsWith('motor:') ? state.publicHistoryMetric : field;
  const unit = { torque_nm: 'N·m', velocity_rad_per_sec: 'rad/s', position_rad: 'rad' }[metric] || '';
  return `${Math.min(...values).toFixed(1)}–${Math.max(...values).toFixed(1)} ${unit}`;
}

function selectedAdminRobot() { return state.robots.find((robot) => robot.uuid === state.selected); }
function robotKey(robot, mode) { return mode === 'admin' ? robot.uuid : robot.id; }
function isPublicOnline(robot) { return Boolean(robot.online) && Date.now() - Date.parse(robot.last_seen) <= 12000; }
function isAdminOnline(robot) { return Date.now() - Date.parse(robot.last_seen) <= 12000; }

function setMetric(name, value, display, sub) {
  const safe = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Number(value))) : 0;
  $(`#${name}-value`).textContent = display;
  $(`#${name}-sub`).textContent = sub;
  const meter = $(`#${name}-meter`);
  meter.className = `level-${Math.round(safe / 5) * 5}`;
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
function powerStatusLabel(value) { return ({ charging: '充电中', discharging: '放电中', not_charging: '未充电', full: '已充满' })[value] || value || '在线'; }
function bytes(value) { const number = Number(value); if (!Number.isFinite(number) || number <= 0) return '-'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let size = number; let index = 0; while (size >= 1024 && index < units.length - 1) { size /= 1024; index += 1; } return `${size.toFixed(index ? 1 : 0)} ${units[index]}`; }
function escapeHTML(value) { return String(value ?? '').replace(/[&<>'"]/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[character])); }
function toast(message, error = false) { const element = $('#toast'); element.textContent = message; element.className = `toast show${error ? ' error' : ''}`; clearTimeout(state.toastTimer); state.toastTimer = setTimeout(() => { element.className = 'toast'; }, 2600); }
