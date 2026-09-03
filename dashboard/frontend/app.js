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
  publicHistoryRequestID: 0,
  publicHistoryMode: 'host',
  publicHistoryMotor: '',
  publicHistoryMetric: 'torque_nm',
  publicHistoryDrawKey: '',
  publicStreamOptions: null,
  publicRealtimeStartedAt: 0,
};

const PUBLIC_REALTIME_WINDOW_SECONDS = 30 * 60;
const PUBLIC_ALL_MOTOR_LIMIT = 1_200;
const PUBLIC_SINGLE_MOTOR_LIMIT = 6_000;
const PUBLIC_SINGLE_CHART_LIMIT = 18_000;
let publicRecorder = null;
let publicRecordingDatabasePromise = null;

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const dashboardPath = window.location.pathname === '/dashboard' || window.location.pathname === '/dashboard/';

function renderIcons() {
  lucide.createIcons();
  $$('svg.lucide').forEach((icon) => icon.setAttribute('aria-hidden', 'true'));
}

document.addEventListener('DOMContentLoaded', boot);

async function boot() {
  renderIcons();
  bindEvents();
  window.addEventListener('pagehide', flushPublicRecording);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') flushPublicRecording(); });
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
  $('#public-history-range').addEventListener('change', () => {
    const robot = selectedPublicRobot();
    state.publicHistory = [];
    state.publicHistoryRobot = null;
    state.publicHistoryDrawKey = '';
    if (!robot) return;
    if (publicHistoryIsRealtime()) {
      startPublicRealtime(robot);
      renderPublicHistoryControls();
      drawPublicHistory(state.publicHistory);
    } else {
      syncPublicStream();
      loadPublicHistory(robot);
    }
  });
  $('#public-metric-select').addEventListener('change', (event) => { state.publicHistoryMetric = event.target.value; state.publicHistoryDrawKey = ''; drawPublicHistory(state.publicHistory); });
  $('#public-motor-select').addEventListener('change', (event) => {
    state.publicHistoryMotor = event.target.value;
    state.publicHistory = [];
    state.publicHistoryRobot = null;
    state.publicHistoryDrawKey = '';
    state.publicRealtimeStartedAt = 0;
    const robot = selectedPublicRobot();
    if (robot && publicHistoryIsRealtime()) startPublicRealtime(robot);
    else if (robot) loadPublicHistory(robot);
  });
  $('#public-download-button').addEventListener('click', downloadPublicRecording);
  $('#public-record-button').addEventListener('click', togglePublicRecording);
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
  $('#skip-link').href = state.view === 'settings' ? '#settings-view' : '#public-view';
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

function openStream(mode, publicOptions = null) {
  closeStream(false);
  state.streamMode = mode;
  state.publicStreamOptions = mode === 'public' ? publicOptions : null;
  setConnection('reconnecting', mode === 'admin' ? '后台通道连接中' : '实时通道连接中');
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws';
  let path = mode === 'admin' ? '/api/v1/admin/ws/robots' : '/api/v1/ws/robots';
  if (mode === 'public' && publicOptions?.includeSamples) {
    const query = new URLSearchParams({ include_samples: '1', robot_id: publicOptions.robotID });
    path += `?${query}`;
  }
  const socket = new WebSocket(`${scheme}://${window.location.host}${path}`);
  state.stream = socket;
  socket.addEventListener('open', () => {
    if (state.stream !== socket) return;
    state.reconnectAttempt = 0;
    setConnection('live', mode === 'admin' ? '后台实时' : '实时连接');
  });
  socket.addEventListener('message', (event) => {
    if (state.stream !== socket) return;
    try { receiveEvent(JSON.parse(event.data), mode, event.data); } catch { setConnection('error', '数据格式错误'); }
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
  if (state.view === 'settings') openStream('admin');
  else openStream('public', publicStreamOptionsForCurrent());
}

function scheduleReconnect() {
  if (state.reconnectTimer || !state.streamMode) return;
  const delay = Math.min(1000 * (2 ** Math.min(state.reconnectAttempt, 4)), 15000);
  state.reconnectAttempt += 1;
  state.reconnectTimer = setTimeout(() => {
    state.reconnectTimer = null;
    openStream(state.streamMode, state.streamMode === 'public' ? state.publicStreamOptions : null);
  }, delay);
}

function receiveEvent(event, mode, rawEvent = '') {
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
    if (mode === 'public' && state.selected === event.robot.id) {
      recordPublicTelemetry(rawEvent, event.robot);
      if (publicHistoryIsRealtime() && state.publicHistoryMode === 'host') appendPublicHostSample(event.robot);
      if (state.publicHistoryMode !== 'host') appendPublicMotorSamples(event.robot);
    }
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
  renderIcons();
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
  const robots = state.robots.filter((robot) => [robot.code, robot.remark].some((value) => (value || '').toLowerCase().includes(query)));
  $('#robot-list').innerHTML = robots.map((robot) => {
    const summary = robot.summary || {};
    const battery = summary.battery;
    const online = isPublicOnline(robot);
    const remark = (robot.remark || '').trim();
    const metric = (label, value, sub, level) => `<div class="robot-card-metric"><span>${label}</span><strong>${value}</strong><div class="meter"><i class="${meterClass(level)}"></i></div><small>${sub}</small></div>`;
    return `<a class="robot-card ${online ? 'online' : 'offline'}" data-key="${escapeHTML(robot.id)}" href="/robot/${encodeURIComponent(robot.id)}">
      <header><span class="robot-presence ${online ? 'online' : ''}"></span><div><strong>${escapeHTML(robot.code)}</strong>${remark ? `<small>${escapeHTML(remark)}</small>` : ''}</div><span class="status-label ${online ? 'online' : ''}">${online ? '在线' : '离线'}</span></header>
      <div class="robot-card-meta"><time>${relativeTime(robot.last_seen)}</time></div>
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
  if (publicHistoryIsRealtime()) {
    if (state.publicHistoryMode === 'host') primePublicRealtimeHistory(robot);
    else startPublicRealtime(robot);
    renderPublicHistoryControls();
    drawPublicHistory(state.publicHistory);
  } else if (state.publicHistoryRobot !== robot.id && !state.publicHistoryLoading) loadPublicHistory(robot);
  else if (!state.publicHistoryLoading) drawPublicHistory(state.publicHistory);
}

function selectedPublicRobot() { return state.robots.find((robot) => robot.id === state.selected); }

function publicRobotIDFromLocation() {
  const match = window.location.pathname.match(/^\/robot\/([^/]+)\/?$/);
  if (!match) return null;
  try { return decodeURIComponent(match[1]); } catch { return null; }
}

function openPublicRobot(id) {
  if (!id || state.selected === id) return;
  stopPublicRecording();
  openStream('public');
  state.selected = id;
  state.publicHistoryMotor = '';
  state.publicHistory = [];
  state.publicHistoryRobot = null;
  state.publicHistoryDrawKey = '';
  state.publicRealtimeStartedAt = 0;
  state.publicHistoryRequestID += 1;
  state.publicHistoryLoading = false;
  window.history.pushState({}, '', `/robot/${encodeURIComponent(id)}`);
  render();
}

function showFleet(replace = false) {
  stopPublicRecording();
  openStream('public');
  state.selected = null;
  state.publicHistoryMotor = '';
  state.publicHistory = [];
  state.publicHistoryRobot = null;
  state.publicHistoryDrawKey = '';
  state.publicRealtimeStartedAt = 0;
  state.publicHistoryRequestID += 1;
  state.publicHistoryLoading = false;
  if (replace) window.history.replaceState({}, '', '/');
  else window.history.pushState({}, '', '/');
  render();
}

function syncPublicRoute() {
  if (state.view !== 'display') return;
  const id = publicRobotIDFromLocation();
  if (id !== state.selected) {
    stopPublicRecording();
    openStream('public');
    state.selected = id;
    state.publicHistoryMotor = '';
    state.publicHistory = [];
    state.publicHistoryRobot = null;
    state.publicHistoryDrawKey = '';
    state.publicRealtimeStartedAt = 0;
    state.publicHistoryRequestID += 1;
    state.publicHistoryLoading = false;
  }
  render();
}

function renderPublicHistoryControls() {
  const range = $('#public-history-range');
  const rangeScope = state.publicHistoryMode === 'motors' ? 'all-motors' : state.publicHistoryMode === 'single' ? 'single-motor' : 'host';
  const rangeOptions = state.publicHistoryMode === 'motors'
    ? [['60', '最近 1 分钟']]
    : state.publicHistoryMode === 'single'
      ? [['60', '最近 1 分钟'], ['realtime', '实时']]
    : [['1', '1 小时'], ['6', '6 小时'], ['24', '1 天'], ['168', '7 天']];
  if (range.dataset.scope !== rangeScope) {
    range.innerHTML = rangeOptions.map(([value, label]) => `<option value="${value}">${label}</option>`).join('');
    range.value = state.publicHistoryMode === 'host' ? '1' : '60';
    range.dataset.scope = rangeScope;
  }
  const latestMotorPoint = [...state.publicHistory].reverse().find((point) => point.motors?.length);
  const motors = new Map((latestMotorPoint?.motors || []).map((motor) => [motor.id, motor.id]));
  const motorSelect = $('#public-motor-select');
  const current = state.publicHistoryMotor;
  const motorOptions = [...motors.entries()].map(([id, label]) => `<option value="${escapeHTML(id)}">${escapeHTML(label)}</option>`).join('');
  if (motorSelect.innerHTML !== motorOptions) motorSelect.innerHTML = motorOptions;
  if (motors.size && (!current || !motors.has(current))) state.publicHistoryMotor = motors.keys().next().value;
  motorSelect.value = state.publicHistoryMotor;
  motorSelect.classList.toggle('hidden', state.publicHistoryMode !== 'single' || !motors.size);
  $('#public-metric-select').classList.toggle('hidden', state.publicHistoryMode !== 'motors' || !motors.size);
  $('#public-history-range').classList.toggle('hidden', state.publicHistoryMode === 'motors');
  $('#public-history-fixed-range').classList.toggle('hidden', state.publicHistoryMode !== 'motors');
  $('#public-recording-indicator').classList.toggle('hidden', !publicRecorder?.active);
  const recordButton = $('#public-record-button');
  const selected = selectedPublicRobot();
  recordButton.disabled = !selected;
  recordButton.title = publicRecorder?.active ? '停止录制' : '开始录制';
  recordButton.setAttribute('aria-label', recordButton.title);
  recordButton.classList.toggle('recording', Boolean(publicRecorder?.active));
  recordButton.innerHTML = publicRecorder?.active
    ? '<i data-lucide="square"></i><span>停止录制</span>'
    : '<i data-lucide="circle"></i><span>开始录制</span>';
  $('#public-download-button').disabled = !publicRecorder || (!publicRecorder.active && !publicRecorder.hasData);
  $$('[data-public-mode]').forEach((button) => button.classList.toggle('active', button.dataset.publicMode === state.publicHistoryMode));
  renderIcons();
}

function setPublicHistoryMode(mode) {
  state.publicHistoryMode = mode;
  state.publicHistory = [];
  state.publicHistoryRobot = null;
  state.publicHistoryDrawKey = '';
  renderPublicHistoryControls();
  const robot = selectedPublicRobot();
  syncPublicStream();
  if (robot && publicHistoryIsRealtime()) {
    startPublicRealtime(robot);
    renderPublicHistoryControls();
    drawPublicHistory(state.publicHistory);
  } else if (robot) loadPublicHistory(robot);
  else drawPublicHistory(state.publicHistory);
}

function publicHistoryRange() { return $('#public-history-range')?.value || '1'; }
function publicHistoryIsRealtime() { return state.publicHistoryMode === 'single' && publicHistoryRange() === 'realtime'; }
function isPublicSingleRealtime() { return state.publicHistoryMode === 'single' && publicHistoryIsRealtime(); }

function publicStreamOptionsForCurrent() {
  const robot = selectedPublicRobot();
  if (!robot) return null;
  const recording = publicRecorder?.active && publicRecorder.robotID === robot.id;
  if (!recording && !isPublicSingleRealtime()) return null;
  return { includeSamples: true, robotID: robot.id };
}

function syncPublicStream() {
  if (state.view !== 'display') return;
  const desired = publicStreamOptionsForCurrent();
  const current = state.publicStreamOptions;
  const same = (desired?.includeSamples || false) === (current?.includeSamples || false)
    && (desired?.robotID || '') === (current?.robotID || '');
  if (!same || !state.stream) openStream('public', desired);
}

async function loadPublicHistory(robot) {
  if (!robot || publicHistoryIsRealtime()) {
    if (robot) primePublicRealtimeHistory(robot);
    return;
  }
  const requestID = ++state.publicHistoryRequestID;
  state.publicHistoryLoading = true;
  state.publicHistoryDrawKey = '';
  $('#public-chart-grid').innerHTML = '';
  $('#public-history-empty').classList.add('hidden');
  $('#public-chart-grid').innerHTML = '<div class="history-loading" aria-label="正在读取"><span></span></div>';
  try {
    const fastScope = state.publicHistoryMode !== 'host';
    const range = fastScope ? 60 : Number(publicHistoryRange()) || 24;
    const requestedMotor = state.publicHistoryMode === 'single' ? state.publicHistoryMotor : '';
    const limit = state.publicHistoryMode === 'single' && requestedMotor ? PUBLIC_SINGLE_MOTOR_LIMIT : PUBLIC_ALL_MOTOR_LIMIT;
    const motor = requestedMotor ? `&motor_id=${encodeURIComponent(requestedMotor)}` : '';
    const query = fastScope ? `scope=motors&seconds=${range}&limit=${limit}${motor}` : `hours=${range}`;
    const data = await api(`/api/v1/robots/${encodeURIComponent(robot.id)}/history?${query}`);
    if (state.selected !== robot.id || requestID !== state.publicHistoryRequestID) return;
    state.publicHistory = fastScope ? mergePublicMotorPoints(data.points || [], state.publicHistoryRobot === robot.id ? state.publicHistory : []) : (data.points || []);
    state.publicHistoryRobot = robot.id;
    state.publicHistoryDrawKey = '';
    state.publicHistoryLoading = false;
    renderPublicHistoryControls();
    if (state.publicHistoryMode === 'single' && !requestedMotor && state.publicHistoryMotor) {
      state.publicHistory = [];
      state.publicHistoryRobot = null;
      return loadPublicHistory(robot);
    }
    drawPublicHistory(state.publicHistory);
  } catch (error) {
    if (requestID === state.publicHistoryRequestID && state.selected === robot.id) {
      $('#public-chart-grid').innerHTML = '';
      $('#public-history-empty').textContent = error.message;
      $('#public-history-empty').classList.remove('hidden');
    }
  } finally {
    if (requestID === state.publicHistoryRequestID) state.publicHistoryLoading = false;
    const selected = selectedPublicRobot();
    if (selected && selected.id !== robot.id) window.queueMicrotask(() => loadPublicHistory(selected));
  }
}

function motorSamplesToPoints(samples, labels = {}) {
  return (samples || []).map((sample) => ({
    at: sample.at,
    motor_count: (sample.motors || []).length,
    motor_topic_online: true,
    motors: (sample.motors || []).map((motor) => ({ ...motor, label: motor.id })),
  }));
}

function mergePublicMotorPoints(fresh, existing = []) {
  const byAt = new Map();
  [...existing, ...fresh].forEach((point) => {
    if (!point?.at || !Number.isFinite(Date.parse(point.at))) return;
    byAt.set(new Date(point.at).toISOString(), { ...point, at: new Date(point.at).toISOString() });
  });
  const points = [...byAt.values()].sort((left, right) => Date.parse(left.at) - Date.parse(right.at));
  if (!points.length) return [];
  const seconds = isPublicSingleRealtime() ? PUBLIC_REALTIME_WINDOW_SECONDS : Number(publicHistoryRange()) || 60;
  const cutoff = Date.parse(points.at(-1).at) - seconds * 1000;
  const visible = points.filter((point) => Date.parse(point.at) >= cutoff);
  const maxPoints = isPublicSingleRealtime() ? PUBLIC_SINGLE_CHART_LIMIT : (state.publicHistoryMode === 'single' ? PUBLIC_SINGLE_MOTOR_LIMIT : PUBLIC_ALL_MOTOR_LIMIT);
  return visible.length > maxPoints ? visible.slice(-maxPoints) : visible;
}

function primePublicRealtimeHistory(robot) {
  if (!robot || state.publicHistoryMode !== 'host' || !publicHistoryIsRealtime()) {
    if (robot && state.publicHistoryMode !== 'host' && publicHistoryIsRealtime()) appendPublicMotorSamples(robot);
    return;
  }
  if (state.publicHistoryRobot !== robot.id) {
    state.publicHistory = [];
    state.publicHistoryRobot = robot.id;
  }
  appendPublicHostSample(robot);
}

function startPublicRealtime(robot, reset = true) {
  if (!robot || state.publicHistoryMode !== 'single' || !publicHistoryIsRealtime()) return;
  if (reset && state.publicHistoryRobot !== robot.id) state.publicHistory = [];
  if (reset && state.publicHistoryRobot !== robot.id) state.publicRealtimeStartedAt = Date.now();
  state.publicHistoryRobot = robot.id;
  syncPublicStream();
  renderPublicHistoryControls();
}

function appendPublicHostSample(robot) {
  if (!robot || state.publicHistoryMode !== 'host' || !publicHistoryIsRealtime()) return;
  const summary = robot.summary || {};
  if (!summary.has_telemetry) return;
  const battery = summary.battery?.online ? summary.battery : null;
  const gpu = summary.gpu;
  const at = robot.collected_at || robot.last_seen;
  const point = {
    at,
    cpu_percent: summary.cpu_percent,
    memory_percent: summary.memory_percent,
    disk_percent: summary.disk_percent,
    load_1: summary.load_1,
    temperature_max: summary.temperature_max,
    gpu_utilization_percent: gpu?.utilization_percent,
    battery_soc_percent: battery?.soc_percent,
    battery_voltage: battery?.voltage,
    battery_current: battery?.current,
    battery_power_watts: battery?.power_watts,
    battery_temperature: battery?.temperature,
  };
  state.publicHistory = mergePublicMotorPoints([point], state.publicHistoryRobot === robot.id ? state.publicHistory : []);
  state.publicHistoryRobot = robot.id;
  state.publicHistoryDrawKey = '';
}

function appendPublicMotorSamples(robot) {
  if (state.publicHistoryMode === 'host' || !robot?.motor_samples?.length) return;
  const points = motorSamplesToPoints(robot.motor_samples, robot.motor_labels);
  state.publicHistory = mergePublicMotorPoints(points, state.publicHistoryRobot === robot.id ? state.publicHistory : []);
  state.publicHistoryRobot = robot.id;
  state.publicHistoryDrawKey = '';
}

function startPublicRecording(robotID) {
  if (publicRecorder?.active && publicRecorder.robotID === robotID) return;
  stopPublicRecording();
  publicRecorder = {
    id: crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    robotID,
    startedAt: new Date().toISOString(),
    active: true,
    hasData: false,
    index: 0,
    pending: [],
    chunks: [],
    writePromise: Promise.resolve(),
  };
  renderPublicHistoryControls();
}

function togglePublicRecording() {
  const robot = selectedPublicRobot();
  if (!robot) return;
  if (publicRecorder?.active) {
    stopPublicRecording();
  } else {
    startPublicRecording(robot.id);
  }
  syncPublicStream();
  renderPublicHistoryControls();
}

function recordPublicTelemetry(rawEvent, robot) {
  if (!publicRecorder?.active || !rawEvent || !robot || robot.id !== publicRecorder.robotID) return;
  publicRecorder.pending.push(rawEvent);
  publicRecorder.hasData = true;
  if (publicRecorder.pending.length >= 24 || publicRecorder.pending.join('').length >= 384 * 1024) flushPublicRecording();
  renderPublicHistoryControls();
}

function stopPublicRecording() {
  if (!publicRecorder?.active) return;
  publicRecorder.active = false;
  flushPublicRecording();
  renderPublicHistoryControls();
}

function openRecordingDatabase() {
  if (publicRecordingDatabasePromise) return publicRecordingDatabasePromise;
  if (!window.indexedDB) return Promise.resolve(null);
  publicRecordingDatabasePromise = new Promise((resolve) => {
    const request = indexedDB.open('baize-monitor-recordings-v1', 1);
    request.onupgradeneeded = () => request.result.createObjectStore('chunks', { keyPath: 'key' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => resolve(null);
  });
  return publicRecordingDatabasePromise;
}

function flushPublicRecording() {
  if (!publicRecorder?.pending.length) return;
  const recorder = publicRecorder;
  const events = recorder.pending.splice(0);
  const chunk = { key: `${recorder.id}:${recorder.index++}`, id: recorder.id, events };
  recorder.writePromise = recorder.writePromise.then(async () => {
    const database = await openRecordingDatabase();
    if (!database) {
      recorder.chunks.push(chunk);
      return;
    }
    await new Promise((resolve) => {
      const transaction = database.transaction('chunks', 'readwrite');
      transaction.objectStore('chunks').put(chunk);
      transaction.oncomplete = resolve;
      transaction.onerror = resolve;
    });
  });
}

async function downloadPublicRecording() {
  if (!publicRecorder) return;
  flushPublicRecording();
  await publicRecorder.writePromise;
  const chunks = [...publicRecorder.chunks];
  const database = await openRecordingDatabase();
  if (database) {
    const stored = await new Promise((resolve) => {
      const request = database.transaction('chunks', 'readonly').objectStore('chunks').getAll();
      request.onsuccess = () => resolve(request.result.filter((chunk) => chunk.id === publicRecorder.id));
      request.onerror = () => resolve([]);
    });
    chunks.push(...stored);
  }
  chunks.sort((left, right) => left.key.localeCompare(right.key, undefined, { numeric: true }));
  const events = chunks.flatMap((chunk) => chunk.events || []);
  const rows = publicRecordingRows(events, publicRecorder.robotID);
  if (!rows.length) { toast('暂无可下载的录制数据', true); return; }
  const headers = [
    '序号', '采样时间（UTC）', '机器人编码', '机器人ID', '记录类型', '电机ID', '电机名称',
    '电机位置（rad）', '电机速度（rad/s）', '电机转矩（N·m）', 'CPU使用率（%）', '内存使用率（%）',
    '磁盘使用率（%）', '系统负载（1m）', '最高温度（°C）', 'GPU使用率（%）', 'GPU温度（°C）',
    '电池电量（%）', '电池电压（V）', '电池电流（A）', '电池功率（W）'
  ];
  const body = [headers, ...rows.map((row, index) => [
    index + 1, row.at, row.code, row.robotID, row.kind, row.motorID, row.motorLabel,
    csvNumber(row.positionRad), csvNumber(row.velocityRadPerSec), csvNumber(row.torqueNm),
    csvNumber(row.cpuPercent), csvNumber(row.memoryPercent), csvNumber(row.diskPercent), csvNumber(row.load1),
    csvNumber(row.temperatureMax), csvNumber(row.gpuUtilizationPercent), csvNumber(row.gpuTemperatureCelsius),
    csvNumber(row.batterySocPercent), csvNumber(row.batteryVoltage), csvNumber(row.batteryCurrent), csvNumber(row.batteryPowerWatts)
  ])]
    .map((columns) => columns.map(csvCell).join(','))
    .join('\r\n');
  const link = document.createElement('a');
  link.href = URL.createObjectURL(new Blob([`\uFEFF${body}\r\n`], { type: 'text/csv;charset=utf-8' }));
  link.download = `baize-${publicRecorder.robotID}-${publicRecorder.startedAt.replace(/[:.]/g, '-')}.csv`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(link.href), 1000);
  toast(`已下载 ${rows.length} 条有序遥测记录`);
}

function publicRecordingRows(events, robotID) {
  const rows = [];
  const seen = new Set();
  for (const rawEvent of events) {
    let event;
    try { event = JSON.parse(rawEvent); } catch { continue; }
    const robot = event?.robot;
    if (!robot || robot.id !== robotID) continue;
    const summary = robot.summary || {};
    const hostAt = robot.collected_at || robot.last_seen || event.server_time;
    if (hostAt && !seen.has(`host:${hostAt}`)) {
      seen.add(`host:${hostAt}`);
      rows.push({
        at: hostAt, code: robot.code || '', robotID, kind: '主机摘要', motorID: '', motorLabel: '',
        cpuPercent: summary.cpu_percent, memoryPercent: summary.memory_percent, diskPercent: summary.disk_percent,
        load1: summary.load_1, temperatureMax: summary.temperature_max,
        gpuUtilizationPercent: summary.gpu?.utilization_percent, gpuTemperatureCelsius: summary.gpu?.temperature_celsius,
        batterySocPercent: summary.battery?.soc_percent, batteryVoltage: summary.battery?.voltage,
        batteryCurrent: summary.battery?.current, batteryPowerWatts: summary.battery?.power_watts
      });
    }
    for (const sample of robot.motor_samples || []) {
      if (!sample?.at) continue;
      for (const motor of sample.motors || []) {
        if (!motor?.id) continue;
        const key = `motor:${sample.at}:${motor.id}`;
        if (seen.has(key)) continue;
        seen.add(key);
        rows.push({
          at: sample.at, code: robot.code || '', robotID, kind: '电机采样', motorID: motor.id,
          motorLabel: motor.label || robot.motor_labels?.[motor.id] || motor.id,
          positionRad: motor.position_rad, velocityRadPerSec: motor.velocity_rad_per_sec, torqueNm: motor.torque_nm
        });
      }
    }
  }
  rows.sort((left, right) => {
    const leftAt = Date.parse(left.at);
    const rightAt = Date.parse(right.at);
    if (Number.isFinite(leftAt) && Number.isFinite(rightAt) && leftAt !== rightAt) return leftAt - rightAt;
    if (Number.isFinite(leftAt) !== Number.isFinite(rightAt)) return Number.isFinite(leftAt) ? -1 : 1;
    if (left.kind !== right.kind) return left.kind === '主机摘要' ? -1 : 1;
    return String(left.motorID).localeCompare(String(right.motorID), undefined, { numeric: true });
  });
  return rows;
}

function csvNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) ? String(number) : '';
}

function csvCell(value) {
  if (value === null || value === undefined) return '';
  const text = String(value);
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
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
  renderIcons();
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
    $('#history-empty').textContent = '暂无历史数据';
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
}

function drawPublicHistory(points) {
  const grid = $('#public-chart-grid');
  if (!grid || !selectedPublicRobot()) return;
  grid.classList.toggle('single-mode', state.publicHistoryMode === 'single');
  const specs = publicChartSpecs(points).map((spec) => ({ ...spec, values: finiteSeriesValues(points, spec) })).filter((spec) => spec.values.length > 0);
  const drawKey = JSON.stringify([
    state.publicHistoryRobot,
    state.publicHistoryMode,
    state.publicHistoryMetric,
    state.publicHistoryMotor,
    points.length,
    points[0]?.at,
    points.at(-1)?.at,
    specs.map((spec) => [spec.key, spec.values.at(-1)?.at, spec.values.at(-1)?.value]),
    Math.round(grid.getBoundingClientRect().width),
  ]);
  if (drawKey === state.publicHistoryDrawKey && grid.childElementCount) return;
  const empty = $('#public-history-empty');
  empty.classList.toggle('hidden', specs.length > 0 || state.publicHistoryLoading);
  if (!specs.length) {
    empty.textContent = points.length ? '当前维度暂无可用数据' : (publicHistoryIsRealtime() ? '暂无实时数据' : '暂无历史数据');
    grid.innerHTML = '';
    state.publicHistoryDrawKey = drawKey;
    return;
  }
  grid.innerHTML = specs.map((spec, index) => {
    const latest = spec.values.at(-1)?.value;
    const realtime = publicHistoryIsRealtime();
    const start = realtime ? realtimeAxisStart(spec.values) : 0;
    const end = realtime ? realtimeAxisEnd(spec.values) : 0;
    return `<article class="chart-card"><header><div><span>${escapeHTML(spec.group)}</span><h3>${escapeHTML(spec.label)}</h3></div><strong>${escapeHTML(formatChartValue(latest, spec))}</strong></header><div class="chart-canvas"><canvas data-chart-index="${index}"></canvas></div><footer><span>${escapeHTML(realtime ? formatElapsedAxis(0) : formatChartTime(spec.values[0]?.at))}</span><span>${escapeHTML(realtime ? formatElapsedAxis((end - start) / 1000) : formatChartTime(spec.values.at(-1)?.at))}</span></footer></article>`;
  }).join('');
  $$('#public-chart-grid canvas').forEach((canvas) => drawSingleMetricChart(canvas, specs[Number(canvas.dataset.chartIndex)]));
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
  if (state.publicHistoryMode === 'host') return host.map(([field, label, unit, color, range]) => ({ key: field, group: '主机性能', label, unit, color, range, realtime: publicHistoryIsRealtime(), value: (point) => point[field] }));
  const motors = motorDescriptors(points);
  const metric = {
    torque_nm: ['转矩', 'N·m', '#b66b24'], velocity_rad_per_sec: ['速度', 'rad/s', '#3676ac'], position_rad: ['位置', 'rad', '#79559c']
  };
  if (state.publicHistoryMode === 'motors') {
    const [label, unit, color] = metric[state.publicHistoryMetric];
    return motors.map(({ id, label: motorLabel, index }) => ({ key: `motor:${id}:${state.publicHistoryMetric}`, group: motorLabel, label, unit, color, value: (point) => motorValue(point, id, index, state.publicHistoryMetric) }));
  }
  const selected = motors.find(({ id }) => id === state.publicHistoryMotor);
  if (!selected) return [];
  return Object.entries(metric).map(([field, [label, unit, color]]) => ({ key: `motor:${selected.id}:${field}`, group: selected.label, label, unit, color, realtime: publicHistoryIsRealtime(), value: (point) => motorValue(point, selected.id, selected.index, field) }));
}

function motorDescriptors(points) {
  const point = [...points].reverse().find((item) => item.motors?.length);
  return (point?.motors || []).map((motor, index) => ({ id: motor.id, label: motor.id, index }));
}

function motorValue(point, id, index, field) {
  const motor = point.motors?.[index];
  if (motor?.id === id) return motor[field];
  return point.motors?.find((item) => item.id === id)?.[field];
}

function finiteSeriesValues(points, spec) {
  return points.map((point) => ({ at: point.at, value: Number(spec.value(point)) })).filter((entry) => Number.isFinite(entry.value));
}

function drawSingleMetricChart(canvas, spec) {
  const entries = spec.values || [];
  const values = entries.map((entry) => Number(entry.value));
  const finite = values.filter(Number.isFinite);
  if (!finite.length) return;
  const bounds = canvas.parentElement.getBoundingClientRect();
  const ratio = Math.min(window.devicePixelRatio || 1, 2);
  const width = Math.max(260, Math.floor(bounds.width));
  const height = Math.max(210, Math.floor(bounds.height));
  canvas.width = Math.floor(width * ratio); canvas.height = Math.floor(height * ratio);
  const context = canvas.getContext('2d');
  context.setTransform(ratio, 0, 0, ratio, 0, 0);
  context.clearRect(0, 0, width, height);
  const padding = { top: 12, right: 12, bottom: 28, left: 49 };
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
  const realtime = Boolean(spec.realtime);
  const windowStart = realtime ? realtimeAxisStart(entries) : 0;
  const windowEnd = realtime ? realtimeAxisEnd(entries) : Date.parse(entries.at(-1)?.at);
  const windowDuration = Math.max(1, windowEnd - windowStart);
  if (realtime) {
    context.strokeStyle = '#e4e7eb';
    context.fillStyle = '#7a838d';
    context.textAlign = 'center';
    for (let index = 0; index <= 5; index += 1) {
      const x = padding.left + chartWidth * (index / 5);
      context.beginPath(); context.moveTo(x, padding.top); context.lineTo(x, height - padding.bottom); context.stroke();
      context.fillText(formatElapsedAxis((windowEnd - windowStart) * index / 5 / 1000), x, height - 8);
    }
    context.textAlign = 'start';
  }
  context.strokeStyle = spec.color;
  context.lineWidth = 2;
  context.lineJoin = 'round';
  context.lineCap = 'round';
  context.beginPath();
  let started = false;
  values.forEach((value, index) => {
    if (!Number.isFinite(value)) { started = false; return; }
    const timestamp = Date.parse(entries[index]?.at);
    const ratioX = realtime && Number.isFinite(timestamp) ? Math.max(0, Math.min(1, (timestamp - windowStart) / windowDuration)) : (entries.length === 1 ? .5 : index / (entries.length - 1));
    const x = padding.left + chartWidth * ratioX;
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

function formatNumberWithUnit(value, unit) {
  if (!Number.isFinite(value)) return '--';
  const digits = Math.abs(value) >= 100 ? 0 : Math.abs(value) >= 10 ? 1 : 2;
  return `${value.toFixed(digits)}${unit ? ` ${unit}` : ''}`;
}

function formatRelativeAxis(seconds) {
  if (!Number.isFinite(seconds) || seconds >= -0.5) return '0';
  const total = Math.max(0, Math.round(Math.abs(seconds)));
  if (total < 60) return `-${total}s`;
  const minutes = Math.floor(total / 60);
  const remainder = total % 60;
  return remainder ? `-${minutes}m${String(remainder).padStart(2, '0')}s` : `-${minutes}m`;
}

function formatElapsedAxis(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0s';
  const total = Math.max(0, Math.round(seconds));
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  const remainder = total % 60;
  if (minutes < 60) return remainder ? `${minutes}m${String(remainder).padStart(2, '0')}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${minutes % 60 ? `${String(minutes % 60).padStart(2, '0')}m` : ''}`;
}

function realtimeAxisStart(entries) {
  const valid = entries.filter((entry) => Number.isFinite(Date.parse(entry.at)));
  const first = Date.parse(valid[0]?.at);
  const last = Date.parse(valid.at(-1)?.at);
  if (!Number.isFinite(first)) return Number.isFinite(state.publicRealtimeStartedAt) && state.publicRealtimeStartedAt > 0 ? state.publicRealtimeStartedAt : Date.now();
  // With one sample, give the point a short runway so it remains visible at the right edge.
  return first === last ? first - 1000 : first;
}

function realtimeAxisEnd(entries) {
  const last = Date.parse([...entries].reverse().find((entry) => Number.isFinite(Date.parse(entry.at)))?.at);
  const start = realtimeAxisStart(entries);
  return Number.isFinite(last) ? Math.max(last, start + 1000) : start + 1000;
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
