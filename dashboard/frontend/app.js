const state = { robots: [], releases: [], selected: null, tab: 'overview', timer: null };
const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

document.addEventListener('DOMContentLoaded', async () => {
  lucide.createIcons();
  bindEvents();
  updateClock();
  setInterval(updateClock, 1000);
  try {
    const session = await api('/api/v1/session');
    session.authenticated ? showApp() : showLogin();
  } catch {
    showLogin();
  }
});

function bindEvents() {
  $('#login-form').addEventListener('submit', login);
  $('#logout-button').addEventListener('click', logout);
  $('#refresh-button').addEventListener('click', refresh);
  $('#robot-search').addEventListener('input', renderRobotList);
  $('#remark-button').addEventListener('click', openRemark);
  $('#remark-form').addEventListener('submit', saveRemark);
  $('#update-button').addEventListener('click', openUpdate);
  $('#update-form').addEventListener('submit', assignUpdate);
  $('#clear-update-button').addEventListener('click', clearUpdate);
  $('#release-button').addEventListener('click', openReleases);
  $('#release-form').addEventListener('submit', uploadRelease);
  $$('.dialog-close').forEach(button => button.addEventListener('click', () => button.closest('dialog').close()));
  $$('.tab').forEach(button => button.addEventListener('click', () => setTab(button.dataset.tab)));
}

async function api(path, options = {}) {
  const response = await fetch(path, { credentials: 'same-origin', ...options, headers: { ...(options.body instanceof FormData ? {} : { 'Content-Type': 'application/json' }), ...options.headers } });
  if (response.status === 401) {
    showLogin();
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
    await api('/api/v1/session', { method: 'POST', body: JSON.stringify({ password: $('#password').value }) });
    $('#password').value = '';
    showApp();
  } catch (error) {
    $('#login-error').textContent = error.message;
  }
}

async function logout() {
  try { await api('/api/v1/session', { method: 'DELETE' }); } catch {}
  showLogin();
}

function showLogin() {
  clearInterval(state.timer);
  $('#app-view').classList.add('hidden');
  $('#login-view').classList.remove('hidden');
  setTimeout(() => $('#password').focus(), 0);
}

function showApp() {
  $('#login-view').classList.add('hidden');
  $('#app-view').classList.remove('hidden');
  refresh();
  clearInterval(state.timer);
  state.timer = setInterval(refresh, 3000);
}

async function refresh() {
  try {
    const [robotData, releaseData] = await Promise.all([api('/api/v1/admin/robots'), api('/api/v1/admin/releases')]);
    state.robots = robotData.robots || [];
    state.releases = releaseData.releases || [];
    if (!state.selected || !state.robots.some(robot => robot.uuid === state.selected)) state.selected = state.robots[0]?.uuid || null;
    render();
  } catch (error) {
    if (error.message !== '登录已失效') toast(error.message, true);
  }
}

function render() {
  renderRobotList();
  const robot = selectedRobot();
  $('#empty-state').classList.toggle('hidden', Boolean(robot));
  $('#robot-detail').classList.toggle('hidden', !robot);
  if (!robot) return;
  const online = isOnline(robot);
  $('#robot-code').textContent = robot.code;
  $('#robot-status').textContent = online ? '在线' : '离线';
  $('#robot-status').classList.toggle('online', online);
  $('#detail-beacon').classList.toggle('online', online);
  $('#robot-identity').textContent = `${robot.model || '未知型号'} · ${robot.hostname} · ${robot.os}/${robot.arch} · ${robot.uuid}`;
  $('#robot-remark').textContent = robot.remark || '-';
  $('#agent-version').textContent = robot.agent_version || '-';
  $('#desired-version').textContent = robot.desired_version || '跟随配置';
  $('#last-seen').textContent = relativeTime(robot.last_seen);
  renderMetrics(robot.telemetry || {});
  renderMotors(robot.telemetry?.motors);
  renderDiagnostics(robot.telemetry?.errors || []);
}

function renderRobotList() {
  const query = ($('#robot-search')?.value || '').trim().toLowerCase();
  const robots = state.robots.filter(robot => [robot.code, robot.hostname, robot.remark, robot.uuid].some(value => (value || '').toLowerCase().includes(query)));
  $('#robot-count').textContent = `${state.robots.length} 台`;
  $('#online-count').textContent = `${state.robots.filter(isOnline).length} 在线`;
  $('#robot-list').innerHTML = robots.map(robot => `
    <button class="robot-item ${robot.uuid === state.selected ? 'active' : ''}" data-uuid="${escapeHTML(robot.uuid)}" type="button">
      <span class="dot ${isOnline(robot) ? 'online' : ''}"></span>
      <span class="item-main"><strong>${escapeHTML(robot.code)}</strong><small>${escapeHTML(robot.remark || robot.hostname)}</small></span>
      <time>${relativeTime(robot.last_seen)}</time>
    </button>`).join('') || '<div class="empty-line">无匹配机器人</div>';
  $$('.robot-item').forEach(button => button.addEventListener('click', () => { state.selected = button.dataset.uuid; render(); }));
}

function renderMetrics(telemetry) {
  const system = telemetry.system || {};
  const gpu = (telemetry.gpus || [])[0];
  const bms = telemetry.bms;
  setMetric('cpu', system.cpu_usage_percent, `${fixed(system.cpu_usage_percent)}%`, `负载 ${fixed(system.load_1)} · ${system.cpu_cores || 0} 核`);
  const memoryPercent = percent(system.memory_used_bytes, system.memory_total_bytes);
  setMetric('memory', memoryPercent, `${fixed(memoryPercent)}%`, `${bytes(system.memory_used_bytes)} / ${bytes(system.memory_total_bytes)}`);
  setMetric('gpu', gpu?.utilization_percent, gpu ? `${fixed(gpu.utilization_percent)}%` : '-', gpu ? `${gpu.name} · ${fixed(gpu.temperature_celsius)} °C` : '无 GPU 数据');
  setMetric('battery', bms?.soc_percent, bms?.online ? `${fixed(bms.soc_percent)}%` : '-', bms ? `${fixed(bms.voltage)} V · ${fixed(bms.current)} A` : 'BMS 未启用');

  const disk = (system.disks || [])[0];
  $('#system-facts').innerHTML = facts([
    ['CPU 型号', system.cpu_model || '-'], ['运行时间', duration(system.uptime_seconds)],
    ['负载 1/5/15', `${fixed(system.load_1)} / ${fixed(system.load_5)} / ${fixed(system.load_15)}`],
    ['Swap', `${bytes(system.swap_used_bytes)} / ${bytes(system.swap_total_bytes)}`],
    ['磁盘 ' + (disk?.path || ''), disk ? `${bytes(disk.used_bytes)} / ${bytes(disk.total_bytes)}` : '-']
  ]);
  const temperatures = system.temperatures || [];
  $('#temperature-list').innerHTML = temperatures.map(item => `<div class="temperature-row ${item.celsius >= 80 ? 'hot' : ''}"><span title="${escapeHTML(item.name)}">${escapeHTML(item.name)}</span><strong>${fixed(item.celsius)} °C</strong></div>`).join('') || '<div class="empty-line">无温度数据</div>';
  const batterySpec = bms?.specification || {};
  $('#bms-facts').innerHTML = bms ? facts([
    ['状态', bms.online ? '在线' : '离线'], ['BMS 型号 / 接口', `${bms.protocol} / ${bms.interface}`],
		['电压', `${fixed(bms.voltage)} V`], ['电流', `${fixed(bms.current)} A`],
		['温度', `${fixed(bms.temperature)} °C`], ['功率 / 累计能量', `${fixed(bms.power_watts)} W / ${fixed(bms.total_energy_wh)} Wh`],
		['MOS / 板温 / 加热', `${fixed(bms.mos_celsius)} / ${fixed(bms.board_celsius)} / ${fixed(bms.heater_celsius)} °C`],
		['串数 / 温探', `${bms.cell_count || batterySpec.series_cells || '-'} / ${bms.temperature_count || '-'}`],
    ['剩余容量', bms.remaining_capacity_ah ? `${fixed(bms.remaining_capacity_ah, 2)} Ah` : '-'],
    ['SOH / 循环', `${bms.soh_percent ? fixed(bms.soh_percent) + '%' : '-'} / ${bms.cycle_count || '-'}`],
		['单体电压 Max/Min/Δ', bms.max_cell_voltage ? `${fixed(bms.max_cell_voltage, 3)} / ${fixed(bms.min_cell_voltage, 3)} / ${fixed(bms.cell_voltage_delta, 3)} V` : '-'],
		['单体温度 Max/Min/Δ', bms.max_cell_temperature ? `${fixed(bms.max_cell_temperature)} / ${fixed(bms.min_cell_temperature)} / ${fixed(bms.cell_temperature_delta)} °C` : '-'],
    ['BMS / 电池规格', [batterySpec.vendor, batterySpec.pack_model].filter(Boolean).join(' / ') || '-'],
    ['充放电', bms.power_supply_status || '-'],
    ['ROS2 发布', bms.published_to_ros2 ? '已启用' : '未发布'], ['故障', (bms.faults || []).join(', ') || '无']
  ]) : facts([['状态', '未启用']]);
}

function renderMotors(motors) {
  const items = motors?.items || [];
  $('#motor-count').textContent = items.length;
  $('#motor-source').textContent = motors?.source || '-';
  $('#motor-topic').textContent = motors?.topic || '-';
  $('#motor-topic-state').textContent = motors?.topic_online ? '在线' : '无数据';
  $('#motor-sampled').textContent = motors?.sampled_at ? relativeTime(motors.sampled_at) : '-';
  $('#motor-limit').textContent = motors?.temperature_supported && motors?.per_motor_online_supported ? '' : '当前 JointState 不含逐电机在线状态和温度；表内扭矩由主控根据电流与电机参数估算。';
  $('#motor-limit').classList.toggle('hidden', motors?.temperature_supported && motors?.per_motor_online_supported);
  $('#motor-table').innerHTML = items.map(item => `<tr><td>${escapeHTML(item.id)}</td><td>${escapeHTML(item.label || '-')}</td><td>${escapeHTML([item.brand, item.model].filter(Boolean).join(' / ') || '-')}</td><td>${escapeHTML(item.can_interface || '-')}</td><td>${escapeHTML(item.control_mode || '-')}</td><td>${fixed(item.position_rad, 4)}</td><td>${fixed(item.velocity_rps, 4)}</td><td>${fixed(item.torque_nm, 3)}</td></tr>`).join('') || '<tr><td colspan="8" class="empty-line">无电机数据</td></tr>';
}

function renderDiagnostics(errors) {
  $('#error-count').textContent = errors.length;
  $('#diagnostic-list').innerHTML = errors.map(error => `<div class="diagnostic-row"><strong>${escapeHTML(error.component)}</strong><span>${escapeHTML(error.message)}</span><time>${relativeTime(error.at)}</time></div>`).join('') || '<div class="empty-line">当前无采集错误</div>';
}

function setMetric(name, value, display, sub) {
  const safe = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Number(value))) : 0;
  $(`#${name}-value`).textContent = display;
  $(`#${name}-sub`).textContent = sub;
  $(`#${name}-meter`).style.width = `${safe}%`;
}

function setTab(tab) {
  state.tab = tab;
  $$('.tab').forEach(button => button.classList.toggle('active', button.dataset.tab === tab));
  $$('.tab-view').forEach(view => view.classList.add('hidden'));
  $(`#tab-${tab}`).classList.remove('hidden');
}

function openRemark() {
  const robot = selectedRobot(); if (!robot) return;
  $('#remark-dialog-title').textContent = robot.code;
  $('#remark-input').value = robot.remark || '';
  $('#remark-dialog').showModal();
}

async function saveRemark(event) {
  event.preventDefault(); const robot = selectedRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${robot.uuid}/remark`, { method: 'PATCH', body: JSON.stringify({ remark: $('#remark-input').value }) });
    $('#remark-dialog').close(); toast('备注已保存'); await refresh();
  } catch (error) { toast(error.message, true); }
}

function openUpdate() {
  const robot = selectedRobot(); if (!robot) return;
  const matching = state.releases.filter(release => release.os === robot.os && release.arch === robot.arch);
  $('#update-dialog-title').textContent = robot.code;
  $('#update-version').innerHTML = matching.map(release => `<option value="${escapeHTML(release.version)}">${escapeHTML(release.version)} · ${bytes(release.size)}</option>`).join('');
  $('#update-button').disabled = matching.length === 0;
  if (!matching.length) { toast('没有匹配此机器人平台的版本', true); return; }
  $('#update-version').value = robot.desired_version || matching[0].version;
  $('#update-dialog').showModal();
}

async function assignUpdate(event) {
  event.preventDefault(); const robot = selectedRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${robot.uuid}/update`, { method: 'POST', body: JSON.stringify({ version: $('#update-version').value }) });
    $('#update-dialog').close(); toast('更新任务已下发'); await refresh();
  } catch (error) { toast(error.message, true); }
}

async function clearUpdate() {
  const robot = selectedRobot(); if (!robot) return;
  try {
    await api(`/api/v1/admin/robots/${robot.uuid}/update`, { method: 'DELETE' });
    $('#update-dialog').close(); toast('已清除指定版本'); await refresh();
  } catch (error) { toast(error.message, true); }
}

function openReleases() { renderReleases(); $('#release-dialog').showModal(); }

function renderReleases() {
  $('#release-list').innerHTML = state.releases.map(release => `<div class="release-row"><strong>${escapeHTML(release.version)}</strong><span>${release.os}/${release.arch}</span><code>${release.sha256.slice(0, 12)}</code><span>${bytes(release.size)}</span><button class="icon-button delete-release" data-id="${escapeHTML(release.id)}" title="删除" type="button"><i data-lucide="trash-2"></i></button></div>`).join('') || '<div class="empty-line">暂无版本</div>';
  lucide.createIcons();
  $$('.delete-release').forEach(button => button.addEventListener('click', () => deleteRelease(button.dataset.id)));
}

async function uploadRelease(event) {
  event.preventDefault();
  try {
    await api('/api/v1/admin/releases', { method: 'POST', body: new FormData(event.currentTarget) });
    event.currentTarget.reset(); toast('版本上传完成'); await refresh(); renderReleases();
  } catch (error) { toast(error.message, true); }
}

async function deleteRelease(id) {
  try { await api(`/api/v1/admin/releases/${encodeURIComponent(id)}`, { method: 'DELETE' }); toast('版本已删除'); await refresh(); renderReleases(); }
  catch (error) { toast(error.message, true); }
}

function selectedRobot() { return state.robots.find(robot => robot.uuid === state.selected); }
function isOnline(robot) { return Date.now() - new Date(robot.last_seen).getTime() < 10000; }
function percent(used, total) { return total > 0 ? used / total * 100 : 0; }
function fixed(value, digits = 1) { return Number.isFinite(Number(value)) ? Number(value).toFixed(digits) : '-'; }
function bytes(value) { if (!value) return '0 B'; const units = ['B','KiB','MiB','GiB','TiB']; const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1); return `${(value / 1024 ** index).toFixed(index > 2 ? 1 : 0)} ${units[index]}`; }
function duration(seconds) { if (!seconds) return '-'; const days = Math.floor(seconds / 86400), hours = Math.floor(seconds % 86400 / 3600), minutes = Math.floor(seconds % 3600 / 60); return `${days ? days + ' 天 ' : ''}${hours} 小时 ${minutes} 分`; }
function relativeTime(value) { const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000)); if (seconds < 5) return '刚刚'; if (seconds < 60) return `${seconds} 秒前`; if (seconds < 3600) return `${Math.floor(seconds/60)} 分前`; if (seconds < 86400) return `${Math.floor(seconds/3600)} 小时前`; return `${Math.floor(seconds/86400)} 天前`; }
function facts(rows) { return rows.map(([key, value]) => `<div><dt>${escapeHTML(key)}</dt><dd>${escapeHTML(String(value))}</dd></div>`).join(''); }
function escapeHTML(value) { return String(value ?? '').replace(/[&<>'"]/g, character => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[character])); }
function updateClock() { $('#clock').textContent = new Intl.DateTimeFormat('zh-CN', { month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', second:'2-digit', hour12:false }).format(new Date()); }
let toastTimer; function toast(message, error = false) { const element = $('#toast'); element.textContent = message; element.classList.toggle('error', error); element.classList.add('show'); clearTimeout(toastTimer); toastTimer = setTimeout(() => element.classList.remove('show'), 2800); }
