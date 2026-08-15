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
  publicHistoryDrawKey: '',
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
  state.selected = publicRobotIDFromLocation();
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
  $('#public-metric-select').addEventListener('change', (event) => { state.publicHistoryMetric = event.target.value; state.publicHistoryDrawKey = ''; drawPublicHistory(state.publicHistory); });
  $('#public-motor-select').addEventListener('change', (event) => { state.publicHistoryMotor = event.target.value; state.publicHistoryDrawKey = ''; drawPublicHistory(state.publicHistory); });
  $$('[data-public-mode]').forEach((button) => button.addEventListener('click', () => setPublicHistoryMode(button.dataset.publicMode)));
  $('#back-to-fleet').addEventListener('click', (event) => { event.preventDefault(); showFleet(); });
  window.addEventListener('popstate', syncPublicRoute);
  window.addEventListener('resize', () => {
    drawHistoryChart(state.history);
    state.publicHistoryDrawKey = '';
    drawPublicHistory(state.publicHistory);
  });
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
  $('#dashboard-link').classList.toggle('hidden', state.view === 'settings');
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
  if (event.server_time && $('#fleet-latency')) {
    const serverTime = Date.parse(event.server_time);
    if (Number.isFinite(serverTime)) $('#fleet-latency').textContent = `${Math.max(0, Date.now() - serverTime)} ms`;
  }
  const eventDate = event.robot?.collected_at || event.server_time;
  $('#last-event').textContent = eventDate ? `最新 ${relativeTime(eventDate)}` : '实时数据';
  if (mode === 'admin') {
    if (!state.selected || !state.robots.some((robot) => robotKey(robot, mode) === state.selected)) state.selected = state.robots[0] ? robotKey(state.robots[0], mode) : null;
  } else {
    const routeID = publicRobotIDFromLocation();
    state.selected = routeID || null;
    if (event.type === 'removed' && routeID === event.id) showFleet(true);
  }
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
  const robot = robots.find((item) => item.id === state.selected);
  if (state.selected && !robot && robots.length) {
    showFleet(true);
    return;
  }
  const detailOpen = Boolean(robot);
  $('#fleet-page').classList.toggle('hidden', detailOpen);
  $('#robot-page').classList.toggle('hidden', !detailOpen);
  if (detailOpen) {
    renderPublicDetail(robot);
    return;
  }
  const online = robots.filter(isPublicOnline).length;
  const alerts = robots.reduce((total, robot) => total + (robot.summary?.diagnostic_count || 0), 0);
  $('#fleet-total').textContent = robots.length;
  $('#fleet-online').textContent = online;
  $('#fleet-alerts').textContent = alerts;
  $('#fleet-health').textContent = robots.length ? `${Math.round((online / robots.length) * 100)}% 正常运行` : '等待上报';
  renderRobotList();
  $('#empty-state').classList.toggle('hidden', robots.length > 0);
}

function renderRobotList() {
  const query = ($('#robot-search')?.value || '').trim().toLowerCase();
  const robots = state.robots.filter((robot) => [robot.code, robot.model, robot.remark].some((value) => (value || '').toLowerCase().includes(query)));
  $('#robot-list').innerHTML = robots.map((robot) => {
    const summary = robot.summary || {};
    const battery = summary.battery;
    const online = isPublicOnline(robot);
    const metric = (label, value, sub, level) => `<div class="robot-card-metric"><span>${label}</span><strong>${value}</strong><div class="meter"><i class="${meterClass(level)}"></i></div><small>${sub}</small></div>`;
    return `<a class="robot-card ${online ? 'online' : 'offline'}" data-key="${escapeHTML(robot.id)}" href="/robot/${encodeURIComponent(robot.id)}">
      <header><span class="robot-presence ${online ? 'online' : ''}"></span><div><strong>${escapeHTML(robot.code)}</strong><small>${escapeHTML(robot.remark || robot.model || '未命名设备')}</small></div><span class="status-label ${online ? 'online' : ''}">${online ? '在线' : '离线'}</span></header>
      <div class="robot-card-meta"><span>${escapeHTML(robot.model || '未知型号')}</span><time>${relativeTime(robot.last_seen)}</time></div>
      <div class="robot-card-metrics">
        ${metric('CPU', summary.has_telemetry ? `${fixed(summary.cpu_percent)}%` : '--', `负载 ${fixed(summary.load_1)}`, summary.cpu_percent)}
        ${metric('内存', summary.has_telemetry ? `${fixed(summary.memory_percent)}%` : '--', '系统内存', summary.memory_percent)}
        ${metric('磁盘', summary.has_telemetry ? `${fixed(summary.disk_percent)}%` : '--', '根目录', summary.disk_percent)}
        ${metric('电池', battery?.online ? `${fixed(battery.soc_percent)}%` : '--', battery?.online ? `${fixed(battery.voltage)} V` : '未接入', battery?.soc_percent)}
      </div>
      <footer><span>${summary.gpu ? `GPU ${fixed(summary.gpu.utilization_percent)}%` : 'GPU 无数据'}</span><span>${summary.motor_count || 0} 个电机</span><span class="robot-card-diagnostic ${summary.diagnostic_count ? 'has-alert' : ''}">${summary.diagnostic_count ? `${summary.diagnostic_count} 项诊断` : '诊断正常'}</span></footer>
    </a>`;
  }).join('') || '<div class="empty-line">没有匹配设备</div>';
  $$('#robot-list .robot-card').forEach((card) => card.addEventListener('click', (event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    openPublicRobot(card.dataset.key);
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

function publicRobotIDFromLocation() {
  const match = window.location.pathname.match(/^\/robot\/([^/]+)\/?$/);
  if (!match) return null;
  try { return decodeURIComponent(match[1]); } catch { return null; }
}

function openPublicRobot(id) {
  if (!id || state.selected === id) return;
  state.selected = id;
  state.publicHistory = [];
  state.publicHistoryRobot = null;
  state.publicHistoryDrawKey = '';
  window.history.pushState({}, '', `/robot/${encodeURIComponent(id)}`);
  render();
}

function showFleet(replace = false) {
  state.selected = null;
  state.publicHistory = [];
  state.publicHistoryRobot = null;
  state.publicHistoryDrawKey = '';
  if (replace) window.history.replaceState({}, '', '/');
  else window.history.pushState({}, '', '/');
  render();
}

function syncPublicRoute() {
  if (state.view !== 'display') return;
  const id = publicRobotIDFromLocation();
  if (id !== state.selected) {
    state.selected = id;
    state.publicHistory = [];
    state.publicHistoryRobot = null;
    state.publicHistoryDrawKey = '';
  }
  render();
}

function renderPublicHistoryControls() {
  const motors = new Map();
  state.publicHistory.forEach((point) => (point.motors || []).forEach((motor) => motors.set(motor.id, motor.label || motor.id)));
  const motorSelect = $('#public-motor-select');
  const current = state.publicHistoryMotor;
  const motorOptions = [...motors.entries()].map(([id, label]) => `<option value="${escapeHTML(id)}">${escapeHTML(label)}</option>`).join('');
  if (motorSelect.innerHTML !== motorOptions) motorSelect.innerHTML = motorOptions;
  if (motors.size && (!current || !motors.has(current))) state.publicHistoryMotor = motors.keys().next().value;
  motorSelect.value = state.publicHistoryMotor;
  motorSelect.classList.toggle('hidden', state.publicHistoryMode !== 'single' || !motors.size);
  $('#public-metric-select').classList.toggle('hidden', state.publicHistoryMode !== 'motors' || !motors.size);
  $$('[data-public-mode]').forEach((button) => button.classList.toggle('active', button.dataset.publicMode === state.publicHistoryMode));
}

function setPublicHistoryMode(mode) {
  state.publicHistoryMode = mode;
  state.publicHistoryDrawKey = '';
  renderPublicHistoryControls();
  drawPublicHistory(state.publicHistory);
}

async function loadPublicHistory(robot) {
  if (!robot || state.publicHistoryLoading) return;
  state.publicHistoryLoading = true;
  state.publicHistoryDrawKey = '';
  $('#public-chart-grid').innerHTML = '';
  $('#public-history-empty').textContent = '正在读取历史采样';
  $('#public-history-empty').classList.remove('hidden');
  try {
    const hours = Number($('#public-history-range').value) || 24;
    const data = await api(`/api/v1/robots/${encodeURIComponent(robot.id)}/history?hours=${hours}`);
    if (state.selected !== robot.id) return;
    state.publicHistory = data.points || [];
    state.publicHistoryRobot = robot.id;
    state.publicHistoryDrawKey = '';
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
    ['电机 Topic', motor.topic || '-'], ['电机来源', motor.source || '-'], ['BMS 协议', bms.protocol || '-'], ['BMS Topic', bms.interface || '-'], ['目标版本', robot.desired_version || '跟随发布']
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
  context.font = '11px "LXGW WenKai Screen", system-ui';
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
  const grid = $('#public-chart-grid');
  if (!grid || !selectedPublicRobot()) return;
  const specs = publicChartSpecs(points).filter((spec) => finiteSeriesValues(points, spec).length > 1);
  const drawKey = JSON.stringify([state.publicHistoryRobot, state.publicHistoryMode, state.publicHistoryMetric, state.publicHistoryMotor, points.length, specs.map((spec) => spec.key), Math.round(grid.getBoundingClientRect().width)]);
  if (drawKey === state.publicHistoryDrawKey && grid.childElementCount) return;
  const empty = $('#public-history-empty');
  empty.classList.toggle('hidden', specs.length > 0);
  if (!specs.length) {
    empty.textContent = points.length ? '当前维度暂无足够的历史采样' : '等待形成历史采样';
    grid.innerHTML = '';
    $('#public-history-meta').textContent = points.length ? `已有 ${points.length} 个采样点` : '暂无历史采样';
    state.publicHistoryDrawKey = drawKey;
    return;
  }
  grid.innerHTML = specs.map((spec, index) => {
    const values = finiteSeriesValues(points, spec);
    const latest = values.at(-1)?.value;
    return `<article class="chart-card"><header><div><span>${escapeHTML(spec.group)}</span><h3>${escapeHTML(spec.label)}</h3></div><strong>${escapeHTML(formatChartValue(latest, spec))}</strong></header><div class="chart-canvas"><canvas data-chart-index="${index}"></canvas></div><footer><span>${escapeHTML(formatChartTime(values[0]?.at))}</span><span>${escapeHTML(formatChartTime(values.at(-1)?.at))}</span></footer></article>`;
  }).join('');
  $$('#public-chart-grid canvas').forEach((canvas) => drawSingleMetricChart(canvas, points, specs[Number(canvas.dataset.chartIndex)]));
  const latestAt = points.at(-1)?.at;
  $('#public-history-meta').textContent = `${points.length} 个采样点 · ${latestAt ? `${relativeTime(latestAt)}更新` : '暂无更新时间'}`;
  state.publicHistoryDrawKey = drawKey;
}

function publicChartSpecs(points) {
  const host = [
    ['cpu_percent', 'CPU 使用率', '%', '#2f7d73', [0, 100]], ['memory_percent', '内存使用率', '%', '#3975a7', [0, 100]],
    ['disk_percent', '磁盘使用率', '%', '#6d7088', [0, 100]], ['load_1', '系统负载', '', '#a46a22'],
    ['temperature_max', '最高温度', '°C', '#b44d42'], ['gpu_utilization_percent', 'GPU 使用率', '%', '#517b46', [0, 100]],
    ['battery_soc_percent', '电池电量', '%', '#17835f', [0, 100]], ['battery_voltage', '电池电压', 'V', '#396eae'],
    ['battery_current', '电池电流', 'A', '#8e5e96'], ['battery_power_watts', '电池功率', 'W', '#ae7622']
  ];
  if (state.publicHistoryMode === 'host') return host.map(([field, label, unit, color, range]) => ({ key: field, group: '主机性能', label, unit, color, range, value: (point) => point[field] }));
  const motors = [...new Map(points.flatMap((point) => (point.motors || []).map((motor) => [motor.id, motor.label || motor.id]))).entries()];
  const metric = {
    torque_nm: ['转矩', 'N·m', '#b66b24'], velocity_rad_per_sec: ['速度', 'rad/s', '#3676ac'], position_rad: ['位置', 'rad', '#79559c']
  };
  if (state.publicHistoryMode === 'motors') {
    const [label, unit, color] = metric[state.publicHistoryMetric];
    return motors.map(([id, motorLabel]) => ({ key: `motor:${id}:${state.publicHistoryMetric}`, group: motorLabel, label, unit, color, value: (point) => (point.motors || []).find((motor) => motor.id === id)?.[state.publicHistoryMetric] }));
  }
  const selected = motors.find(([id]) => id === state.publicHistoryMotor);
  if (!selected) return [];
  return Object.entries(metric).map(([field, [label, unit, color]]) => ({ key: `motor:${selected[0]}:${field}`, group: selected[1], label, unit, color, value: (point) => (point.motors || []).find((motor) => motor.id === selected[0])?.[field] }));
}

function finiteSeriesValues(points, spec) {
  return points.map((point) => ({ at: point.at, value: Number(spec.value(point)) })).filter((entry) => Number.isFinite(entry.value));
}

function drawSingleMetricChart(canvas, points, spec) {
  const values = points.map((point) => Number(spec.value(point)));
  const finite = values.filter(Number.isFinite);
  if (finite.length < 2) return;
  const bounds = canvas.parentElement.getBoundingClientRect();
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  const width = Math.max(260, Math.floor(bounds.width));
  const height = Math.max(162, Math.floor(bounds.height));
  canvas.width = Math.floor(width * ratio); canvas.height = Math.floor(height * ratio);
  const context = canvas.getContext('2d');
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  context.clearRect(0, 0, width, height);
  const padding = { top: 12, right: 12, bottom: 12, left: 49 };
  const chartWidth = width - padding.left - padding.right;
  const chartHeight = height - padding.top - padding.bottom;
  let min = spec.range?.[0] ?? Math.min(...finite);
  let max = spec.range?.[1] ?? Math.max(...finite);
  if (!spec.range) {
    if (min === max) { const delta = Math.max(Math.abs(min) * 0.12, 1); min -= delta; max += delta; }
    else { const delta = (max - min) * 0.12; min -= delta; max += delta; }
  }
  context.font = '11px "LXGW WenKai Screen", system-ui';
  context.lineWidth = 1;
  context.strokeStyle = '#e4e7eb';
  context.fillStyle = '#7a838d';
  for (let index = 0; index <= 4; index += 1) {
    const y = padding.top + chartHeight * (index / 4);
    context.beginPath(); context.moveTo(padding.left, y); context.lineTo(width - padding.right, y); context.stroke();
    const value = max - (max - min) * (index / 4);
    context.fillText(formatAxisValue(value, spec.unit), 3, y + 4);
  }
  context.strokeStyle = spec.color;
  context.lineWidth = 2;
  context.lineJoin = 'round';
  context.lineCap = 'round';
  context.beginPath();
  let started = false;
  values.forEach((value, index) => {
    if (!Number.isFinite(value)) { started = false; return; }
    const x = padding.left + chartWidth * (points.length === 1 ? .5 : index / (points.length - 1));
    const y = padding.top + chartHeight * (1 - (value - min) / (max - min));
    if (!started) { context.moveTo(x, y); started = true; } else context.lineTo(x, y);
  });
  context.stroke();
}

function formatChartValue(value, spec) {
  if (!Number.isFinite(value)) return '--';
  const digits = Math.abs(value) >= 100 ? 0 : Math.abs(value) >= 10 ? 1 : 2;
  return `${value.toFixed(digits)}${spec.unit ? ` ${spec.unit}` : ''}`;
}

function formatAxisValue(value, unit) {
  const digits = Math.abs(value) >= 100 ? 0 : Math.abs(value) >= 10 ? 1 : 2;
  return `${value.toFixed(digits)}${unit ? ` ${unit}` : ''}`;
}

function formatChartTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
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

function meterClass(value) {
  const safe = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Number(value))) : 0;
  return `level-${Math.round(safe / 5) * 5}`;
}

function facts(items) { return items.map(([label, value]) => `<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value === undefined || value === null || value === '' ? '-' : String(value))}</dd></div>`).join(''); }
function updateClock() {
  $('#server-clock').textContent = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  if (state.view !== 'display' || !state.robots.length) return;
  updatePublicLiveState();
}

function updatePublicLiveState() {
  const latest = state.robots.reduce((value, robot) => !value || Date.parse(robot.last_seen) > Date.parse(value) ? robot.last_seen : value, '');
  if (latest) $('#last-event').textContent = `最新 ${relativeTime(latest)}`;
  const selected = selectedPublicRobot();
  if (selected) {
    const online = isPublicOnline(selected);
    $('#detail-updated-text').textContent = `采集于 ${formatDate(selected.collected_at)} · ${relativeTime(selected.last_seen)}收到`;
    $('#robot-status').textContent = online ? '在线运行' : '离线';
    $('#robot-status').classList.toggle('online', online);
    $('#detail-beacon').classList.toggle('online', online);
    return;
  }
  const online = state.robots.filter(isPublicOnline).length;
  $('#fleet-online').textContent = online;
  $('#fleet-health').textContent = `${Math.round((online / state.robots.length) * 100)}% 正常运行`;
  state.robots.forEach((robot) => {
    const card = document.querySelector(`.robot-card[data-key="${CSS.escape(robot.id)}"]`);
    if (!card) return;
    const active = isPublicOnline(robot);
    card.classList.toggle('online', active);
    card.classList.toggle('offline', !active);
    card.querySelector('.robot-presence')?.classList.toggle('online', active);
    const label = card.querySelector('.status-label');
    if (label) { label.textContent = active ? '在线' : '离线'; label.classList.toggle('online', active); }
    const time = card.querySelector('time');
    if (time) time.textContent = relativeTime(robot.last_seen);
  });
}
function formatDate(value) { const date = new Date(value); return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString('zh-CN', { hour12: false }); }
function relativeTime(value) { const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000)); if (!Number.isFinite(seconds)) return '-'; if (seconds < 2) return '刚刚'; if (seconds < 60) return `${seconds} 秒前`; const minutes = Math.floor(seconds / 60); if (minutes < 60) return `${minutes} 分钟前`; return `${Math.floor(minutes / 60)} 小时前`; }
function duration(value) { if (!Number.isFinite(Number(value))) return '-'; const seconds = Math.max(0, Math.floor(Number(value))); const days = Math.floor(seconds / 86400); const hours = Math.floor((seconds % 86400) / 3600); const minutes = Math.floor((seconds % 3600) / 60); return days ? `${days} 天 ${hours} 小时` : `${hours} 小时 ${minutes} 分`; }
function fixed(value, digits = 1) { return Number.isFinite(Number(value)) ? Number(value).toFixed(digits) : '-'; }
function powerStatusLabel(value) { return ({ charging: '充电中', discharging: '放电中', not_charging: '未充电', full: '已充满' })[value] || value || '在线'; }
function bytes(value) { const number = Number(value); if (!Number.isFinite(number) || number <= 0) return '-'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let size = number; let index = 0; while (size >= 1024 && index < units.length - 1) { size /= 1024; index += 1; } return `${size.toFixed(index ? 1 : 0)} ${units[index]}`; }
function escapeHTML(value) { return String(value ?? '').replace(/[&<>'"]/g, (character) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[character])); }
function toast(message, error = false) { const element = $('#toast'); element.textContent = message; element.className = `toast show${error ? ' error' : ''}`; clearTimeout(state.toastTimer); state.toastTimer = setTimeout(() => { element.className = 'toast'; }, 2600); }
