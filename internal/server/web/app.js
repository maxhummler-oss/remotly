'use strict';
/* Remotly web viewer & admin UI. No dependencies, no build step. */

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));
const params = new URLSearchParams(location.search);
const KIOSK = params.has('kiosk');

const state = {
  me: null,
  devices: [],
  ws: null,
  wsRetry: 0,
  admin: null,
  session: null,
};

// ---------------------------------------------------------------------------
// Utilities

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (k === 'html') e.innerHTML = v;
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

let toastTimer;
function toast(msg, isError = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.toggle('err', isError);
  t.classList.remove('hidden');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add('hidden'), isError ? 6000 : 3500);
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try { data = await res.json(); } catch { /* empty */ }
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

function show(view) {
  for (const v of ['login', 'app', 'kiosk', 'session']) {
    $('#view-' + v).classList.toggle('hidden', v !== view);
  }
}

function fmtTime(iso) {
  if (!iso || iso.startsWith('0001')) return '–';
  const d = new Date(iso);
  const diff = (Date.now() - d) / 1000;
  if (diff < 60) return 'gerade eben';
  if (diff < 3600) return `vor ${Math.floor(diff / 60)} Min.`;
  if (diff < 86400) return `vor ${Math.floor(diff / 3600)} Std.`;
  return d.toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' });
}

function fmtDuration(iso) {
  const s = Math.max(0, Math.floor((Date.now() - new Date(iso)) / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  return h ? `${h} Std. ${m} Min.` : `${m} Min.`;
}

const OS_ICONS = {
  windows: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M3 5.5 10.5 4.5v7H3zM11.5 4.4 21 3v8.5h-9.5zM3 12.5h7.5v7L3 18.5zM11.5 12.5H21V21l-9.5-1.4z"/></svg>',
  darwin: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M16.4 12.6c0-2.5 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-2-1.5-.2-3 .9-3.8.9-.8 0-2-.9-3.3-.9-1.7 0-3.3 1-4.1 2.5-1.8 3.1-.5 7.6 1.3 10.1.8 1.2 1.8 2.6 3.1 2.5 1.3-.1 1.7-.8 3.2-.8s1.9.8 3.2.8c1.4 0 2.2-1.2 3-2.4.9-1.4 1.3-2.7 1.3-2.8-.1 0-2.4-.9-2.4-3.2zM14 5.3c.7-.8 1.1-1.9 1-3-1 0-2.2.7-2.9 1.5-.6.7-1.2 1.8-1 2.9 1.1.1 2.2-.6 2.9-1.4z"/></svg>',
  linux: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M12 2c-2.2 0-3.5 1.9-3.5 4.4 0 1.4.4 2.4-.4 3.9-.9 1.6-2.6 3.5-2.6 6.3 0 .6.1 1.1.3 1.6-.6.4-1.3.9-1.3 1.6 0 1.2 1.9 1.4 3.6 1.9 1 .3 1.6.3 2.2-.2.5.2 1.1.3 1.7.3s1.2-.1 1.7-.3c.6.5 1.2.5 2.2.2 1.7-.5 3.6-.7 3.6-1.9 0-.7-.7-1.2-1.3-1.6.2-.5.3-1 .3-1.6 0-2.8-1.7-4.7-2.6-6.3-.8-1.5-.4-2.5-.4-3.9C15.5 3.9 14.2 2 12 2zm-1.5 4.2c.4 0 .7.5.7 1.1s-.3 1.1-.7 1.1-.7-.5-.7-1.1.3-1.1.7-1.1zm3 0c.4 0 .7.5.7 1.1s-.3 1.1-.7 1.1-.7-.5-.7-1.1.3-1.1.7-1.1zM12 9.4c.9 0 2 .6 2 1s-1.1 1-2 1-2-.6-2-1 1.1-1 2-1z"/></svg>',
};
const OS_NAMES = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
const osIcon = (os) => el('div', { class: 'os-icon', html: OS_ICONS[os] || OS_ICONS.linux });

function initials(name) {
  return (name || '?').split(/\s+/).map((p) => p[0]).join('').slice(0, 2).toUpperCase();
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast('In die Zwischenablage kopiert');
  } catch {
    toast('Kopieren nicht möglich – bitte manuell markieren', true);
  }
}

// Generic dialog. fields: [{name,label,type,value,placeholder,required,options}]
function dialog({ title, body, okText = 'OK', danger = false, onOk }) {
  const dlg = $('#dlg');
  $('#dlg-title').textContent = title;
  const b = $('#dlg-body');
  b.replaceChildren(...(Array.isArray(body) ? body : [body]));
  $('#dlg-error').textContent = '';
  const ok = $('#dlg-ok');
  ok.textContent = okText;
  ok.className = 'btn ' + (danger ? 'danger' : 'primary');
  return new Promise((resolve) => {
    const submit = async (ev) => {
      ev.preventDefault();
      if (ev.submitter && ev.submitter.value === 'cancel') { dlg.close(); return; }
      ok.disabled = true;
      try {
        const res = onOk ? await onOk() : true;
        dlg.close();
        resolve(res);
      } catch (e) {
        $('#dlg-error').textContent = e.message;
      } finally {
        ok.disabled = false;
      }
    };
    const form = $('#dlg-form');
    form.onsubmit = submit;
    dlg.onclose = () => resolve(undefined);
    dlg.showModal();
    const first = b.querySelector('input:not([type=checkbox])');
    if (first) first.focus();
  });
}

const field = (label, input) => el('label', { class: 'field' }, el('span', {}, label), input);
const check = (label, input) => el('label', { class: 'check' }, input, el('span', {}, label));

// ---------------------------------------------------------------------------
// Login

async function boot() {
  try {
    state.me = await api('GET', '/api/me');
    enterApp();
  } catch (e) {
    const info = (e.data) || {};
    $('#login-server').textContent = info.serverName && info.serverName !== 'Remotly' ? info.serverName : 'Fernzugriff auf deine Arbeitsplatzrechner';
    $('#login-setup').classList.toggle('hidden', !info.needsSetup);
    show('login');
    $('#login-user').focus();
  }
}

$('#login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const btn = $('#login-submit');
  btn.disabled = true;
  $('#login-error').textContent = '';
  try {
    state.me = await api('POST', '/api/login', { username: $('#login-user').value, password: $('#login-pass').value });
    $('#login-pass').value = '';
    enterApp();
  } catch (e) {
    $('#login-error').textContent = e.message;
  } finally {
    btn.disabled = false;
  }
});

async function logout() {
  try { await api('POST', '/api/logout'); } catch { /* ignore */ }
  if (state.ws) { state.ws.onclose = null; state.ws.close(); }
  state.ws = null;
  state.me = null;
  show('login');
}

function enterApp() {
  const me = state.me;
  document.title = `${me.serverName || 'Remotly'}`;
  $('#server-name').textContent = me.serverName && me.serverName !== 'Remotly' ? '· ' + me.serverName : '';
  $('#user-name').textContent = me.displayName;
  $('#user-avatar').textContent = initials(me.displayName);
  $('#tab-admin').classList.toggle('hidden', !me.admin || KIOSK);
  if (KIOSK) {
    $('#kiosk-hello').textContent = `Hallo ${me.displayName.split(' ')[0]}!`;
    show('kiosk');
  } else {
    show('app');
  }
  connectWS();
}

// ---------------------------------------------------------------------------
// Signaling WebSocket

function connectWS() {
  if (state.ws) return;
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/ws/viewer`);
  state.ws = ws;
  setConn('connecting');
  ws.onopen = () => { state.wsRetry = 0; setConn('ok'); };
  ws.onmessage = (ev) => onSignal(JSON.parse(ev.data));
  ws.onclose = async () => {
    state.ws = null;
    setConn('bad');
    if (state.session) state.session.signalingLost();
    // Session expired or revoked?
    try { await api('GET', '/api/me'); } catch (e) {
      if (e.status === 401) { toast('Abgemeldet – bitte erneut anmelden', true); show('login'); return; }
    }
    const delay = Math.min(1000 * 2 ** state.wsRetry++, 15000);
    setTimeout(() => { if (state.me) connectWS(); }, delay);
  };
}

function setConn(s) {
  const c = $('#conn-state');
  c.className = 'conn' + (s === 'ok' ? ' ok' : s === 'bad' ? ' bad' : '');
  c.title = s === 'ok' ? 'Mit Server verbunden' : s === 'bad' ? 'Keine Verbindung zum Server' : 'Verbinde…';
}

function sendSignal(msg) {
  if (state.ws && state.ws.readyState === WebSocket.OPEN) state.ws.send(JSON.stringify(msg));
}

function onSignal(msg) {
  switch (msg.type) {
    case 'devices':
      state.devices = msg.devices || [];
      renderDevices();
      renderKiosk();
      if (state.session) state.session.devicesUpdated();
      break;
    case 'error':
      if (state.session && !state.session.id) {
        state.session.fail(msg.reason);
      } else {
        toast(msg.reason, true);
      }
      break;
    default:
      if (state.session) state.session.onSignal(msg);
  }
}

// ---------------------------------------------------------------------------
// Device list

function deviceStatus(d) {
  if (!d.online) return el('span', { class: 'status off' }, 'Offline');
  if (d.inUseBy && d.inUseBy.length) return el('span', { class: 'status busy', title: d.inUseBy.join(', ') }, 'Verwendet: ' + d.inUseBy.join(', '));
  return el('span', { class: 'status on' }, 'Bereit');
}

function busyByOthers(d) {
  return (d.inUseBy || []).filter((n) => n !== state.me.displayName);
}

function renderDevices() {
  const list = $('#device-list');
  const q = ($('#device-search').value || '').toLowerCase();
  const devices = state.devices.filter((d) => !q || d.name.toLowerCase().includes(q));
  $('#no-devices').classList.toggle('hidden', state.devices.length > 0);
  const online = state.devices.filter((d) => d.online).length;
  $('#devices-sub').textContent = state.devices.length
    ? `${online} von ${state.devices.length} online. Wähle einen Rechner, um dich zu verbinden.`
    : '';
  list.replaceChildren(...devices.map((d) => {
    const others = busyByOthers(d);
    const actions = el('div', { class: 'device-actions' });
    if (d.online) {
      if (others.length && state.me.admin) {
        actions.append(el('button', { class: 'btn small', title: 'Andere Sitzung beenden und übernehmen', onclick: () => startSession(d, { takeover: true }) }, 'Übernehmen'));
        actions.append(el('button', { class: 'btn primary small', title: 'Zusätzlich verbinden (Support)', onclick: () => startSession(d) }, 'Zuschauen'));
      } else {
        actions.append(el('button', { class: 'btn primary small', disabled: others.length > 0, onclick: () => startSession(d) }, 'Verbinden'));
      }
    } else if (d.canWake) {
      actions.append(el('button', { class: 'btn small', onclick: () => wakeDevice(d) }, '⏻ Einschalten'));
    }
    return el('div', { class: 'device ' + (d.online ? 'online' : 'offline') },
      el('div', { class: 'device-head' },
        osIcon(d.os),
        el('div', {},
          el('div', { class: 'device-name' }, d.name),
          el('div', { class: 'device-meta' }, [OS_NAMES[d.os] || d.os, d.encoder && encoderLabel(d.encoder)].filter(Boolean).join(' · ')))),
      el('div', { class: 'device-foot' }, deviceStatus(d), actions));
  }));
}

function encoderLabel(enc) {
  return {
    h264_nvenc: 'NVIDIA NVENC', h264_amf: 'AMD AMF', h264_qsv: 'Intel Quick Sync',
    h264_videotoolbox: 'Apple VideoToolbox', libx264: 'Software (x264)', h264_mf: 'Media Foundation',
  }[enc] || enc;
}

$('#device-search').addEventListener('input', renderDevices);

async function wakeDevice(d) {
  try {
    await api('POST', `/api/devices/${encodeURIComponent(d.id)}/wake`);
    toast(`Einschaltsignal an „${d.name}“ gesendet. Das kann bis zu einer Minute dauern.`);
  } catch (e) {
    toast(e.message, true);
  }
}

// Kiosk: big buttons for the user's machines, auto-connect when only one.
let kioskAutoDone = false;
function renderKiosk() {
  if (!KIOSK) return;
  const box = $('#kiosk-devices');
  const wanted = params.get('device');
  const devices = wanted ? state.devices.filter((d) => d.id === wanted || d.name === wanted) : state.devices;
  if (!devices.length) {
    box.replaceChildren(el('p', { class: 'muted' }, 'Dir ist noch kein Rechner zugewiesen.'));
    return;
  }
  box.replaceChildren(...devices.map((d) => {
    const others = busyByOthers(d);
    let label = 'Verbinden →';
    if (!d.online) label = d.canWake ? '⏻ Einschalten' : 'Offline';
    else if (others.length) label = 'Verwendet von ' + others.join(', ');
    return el('button', {
      class: 'kiosk-device',
      disabled: (!d.online && !d.canWake) || others.length > 0,
      onclick: () => {
        if (!d.online) { wakeDevice(d); return; }
        enterFullscreen();
        startSession(d);
      },
    }, osIcon(d.os), el('div', { class: 'grow' }, el('div', { class: 'device-name' }, d.name),
      el('div', { class: 'device-meta' }, OS_NAMES[d.os] || d.os)), el('span', { class: 'go' }, label));
  }));
  // Reconnect automatically after a session ended unexpectedly.
  if (!kioskAutoDone && devices.length === 1 && devices[0].online && !busyByOthers(devices[0]).length && params.has('auto')) {
    kioskAutoDone = true;
    startSession(devices[0]);
  }
}

$('#kiosk-logout').addEventListener('click', logout);

// ---------------------------------------------------------------------------
// User menu & settings

const settings = {
  forceRelay: false,
  swapMeta: null, // null = automatic
  showStats: true,
  ...JSON.parse(localStorage.getItem('remotly-settings') || '{}'),
};
const saveSettings = () => localStorage.setItem('remotly-settings', JSON.stringify(settings));

$('#btn-user').addEventListener('click', (ev) => {
  ev.stopPropagation();
  $('#user-menu').classList.toggle('hidden');
});
document.addEventListener('click', () => $('#user-menu').classList.add('hidden'));
$('#user-menu').addEventListener('click', (ev) => {
  const action = ev.target.dataset.action;
  if (action === 'logout') logout();
  if (action === 'password') changePassword();
  if (action === 'settings') openSettings();
});

function changePassword() {
  const oldPw = el('input', { type: 'password', autocomplete: 'current-password', required: true });
  const newPw = el('input', { type: 'password', autocomplete: 'new-password', required: true, minlength: 10 });
  const again = el('input', { type: 'password', autocomplete: 'new-password', required: true });
  dialog({
    title: 'Passwort ändern',
    body: [field('Aktuelles Passwort', oldPw), field('Neues Passwort (mind. 10 Zeichen)', newPw), field('Wiederholen', again)],
    okText: 'Ändern',
    onOk: async () => {
      if (newPw.value !== again.value) throw new Error('Passwörter stimmen nicht überein');
      await api('POST', '/api/password', { old: oldPw.value, new: newPw.value });
      toast('Passwort geändert');
    },
  });
}

function openSettings() {
  const relay = el('input', { type: 'checkbox', checked: settings.forceRelay });
  const stats = el('input', { type: 'checkbox', checked: settings.showStats });
  dialog({
    title: 'Einstellungen',
    body: [
      check('Leistungsanzeige in der Sitzung', stats),
      check('Immer über das Server-Relay verbinden (nur nötig, wenn direkte Verbindungen blockiert sind – erhöht die Latenz)', relay),
    ],
    okText: 'Speichern',
    onOk: () => {
      settings.forceRelay = relay.checked;
      settings.showStats = stats.checked;
      saveSettings();
    },
  });
}

// Tabs
$('#tabs').addEventListener('click', (ev) => {
  const tab = ev.target.dataset && ev.target.dataset.tab;
  if (!tab) return;
  $$('.tab').forEach((t) => t.classList.toggle('active', t.dataset.tab === tab));
  $('#tab-devices').classList.toggle('hidden', tab !== 'devices');
  $('#tab-admin-panel').classList.toggle('hidden', tab !== 'admin');
  if (tab === 'admin') loadAdmin();
});

// ---------------------------------------------------------------------------
// Admin

let adminTimer;
async function loadAdmin() {
  clearInterval(adminTimer);
  try {
    state.admin = await api('GET', '/api/admin/overview');
  } catch (e) {
    toast(e.message, true);
    return;
  }
  renderAdmin();
  loadAudit();
  adminTimer = setInterval(async () => {
    if ($('#tab-admin-panel').classList.contains('hidden') || $('#dlg').open) return;
    try { state.admin = await api('GET', '/api/admin/overview'); renderAdmin(); } catch { /* ignore */ }
  }, 5000);
}

function table(id, headers, rows, emptyText) {
  const t = $(id);
  const head = el('thead', {}, el('tr', {}, headers.map((h) => el('th', {}, h))));
  const body = el('tbody', {}, rows.length ? rows : el('tr', {}, el('td', { class: 'empty-row', colspan: headers.length }, emptyText)));
  t.replaceChildren(head, body);
}

function renderAdmin() {
  const a = state.admin;
  const devName = Object.fromEntries(a.devices.map((d) => [d.id, d.name]));
  const online = a.devices.filter((d) => d.online).length;

  $('#admin-kpis').replaceChildren(
    el('div', { class: 'kpi' }, el('b', {}, a.sessions.length), el('span', {}, 'aktive Sitzungen')),
    el('div', { class: 'kpi' }, el('b', {}, `${online}/${a.devices.length}`), el('span', {}, 'Geräte online')),
    el('div', { class: 'kpi' }, el('b', {}, a.users.length), el('span', {}, 'Benutzer')),
  );

  table('#tbl-sessions', ['Benutzer', 'Gerät', 'Von', 'Dauer', ''], a.sessions.map((s) => el('tr', {},
    el('td', {}, s.user), el('td', {}, s.device), el('td', { class: 'mono' }, s.ip), el('td', {}, fmtDuration(s.started)),
    el('td', { class: 'actions' }, el('button', {
      class: 'btn danger small',
      onclick: async () => {
        try { await api('DELETE', `/api/admin/sessions/${s.id}`); toast('Sitzung beendet'); loadAdmin(); } catch (e) { toast(e.message, true); }
      },
    }, 'Beenden')))), 'Keine aktiven Sitzungen');

  table('#tbl-users', ['Name', 'Benutzer', 'Rolle', 'Geräte', 'Letzte Anmeldung', ''], a.users.map((u) => el('tr', {},
    el('td', {}, u.displayName, u.disabled ? el('span', { class: 'tag red' }, ' deaktiviert') : null),
    el('td', { class: 'mono' }, u.name),
    el('td', {}, u.admin ? el('span', { class: 'tag accent' }, 'Admin') : el('span', { class: 'tag' }, 'Benutzer')),
    el('td', {}, u.admin ? 'alle' : (u.devices.map((id) => devName[id]).filter(Boolean).join(', ') || '–')),
    el('td', {}, fmtTime(u.lastLogin)),
    el('td', { class: 'actions' },
      el('button', { class: 'btn small', onclick: () => editUser(u) }, 'Bearbeiten'),
      el('button', { class: 'btn danger small', onclick: () => deleteUser(u) }, 'Löschen')))), 'Keine Benutzer');

  table('#tbl-devices', ['Gerät', 'System', 'Aufnahme', 'IP', 'Zuletzt', ''], a.devices.map((d) => el('tr', {},
    el('td', {}, el('span', { class: 'status ' + (d.online ? 'on' : 'off') }, d.name)),
    el('td', {}, `${OS_NAMES[d.os] || d.os} ${d.arch || ''}`, d.info && d.info.version ? el('div', { class: 'muted small' }, 'Agent ' + d.info.version) : null),
    el('td', {}, d.info ? `${encoderLabel(d.info.encoder)}${d.info.capture ? ' · ' + d.info.capture : ''}${d.info.audio ? ' · Ton' : ''}` : '–',
      d.info && d.info.desktop && d.info.desktop !== 'Default' ? el('div', { class: 'muted small' }, 'Desktop: ' + d.info.desktop) : null),
    el('td', { class: 'mono' }, d.lastIp || '–'),
    el('td', {}, d.online ? 'jetzt' : fmtTime(d.lastSeen)),
    el('td', { class: 'actions' },
      el('button', { class: 'btn small', onclick: () => renameDevice(d) }, 'Umbenennen'),
      el('button', { class: 'btn danger small', onclick: () => deleteDevice(d) }, 'Entfernen')))), 'Noch keine Geräte registriert');

  $('#kiosk-cmd').textContent = `chromium --kiosk --noerrdialogs --disable-session-crashed-bubble "${location.origin}/?kiosk&auto"`;
  $('#ca-fp').textContent = a.caFingerprint || '(eigenes Zertifikat)';
  $('#ca-link').classList.toggle('hidden', !a.caFingerprint);
}

async function loadAudit() {
  try {
    const entries = await api('GET', '/api/admin/audit');
    const labels = {
      'login': 'Anmeldung', 'login.failed': 'Fehlgeschlagene Anmeldung', 'session.start': 'Sitzung gestartet',
      'session.end': 'Sitzung beendet', 'session.denied': 'Zugriff verweigert', 'agent.enrolled': 'Gerät registriert',
      'agent.enroll_failed': 'Registrierung abgelehnt', 'agent.auth_failed': 'Gerät abgewiesen', 'device.wake': 'Wake-on-LAN',
      'device.renamed': 'Gerät umbenannt', 'device.revoked': 'Gerät entfernt', 'user.created': 'Benutzer angelegt',
      'user.updated': 'Benutzer geändert', 'user.deleted': 'Benutzer gelöscht', 'user.password_changed': 'Passwort geändert',
      'enroll_key.rotated': 'Registrierungsschlüssel erneuert',
    };
    table('#tbl-audit', ['Zeit', 'Ereignis', 'Benutzer', 'Gerät', 'IP', 'Details'], (entries || []).map((e) => el('tr', {},
      el('td', { class: 'small nowrap' }, new Date(e.time).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'medium' })),
      el('td', {}, e.event.includes('fail') || e.event.includes('denied') ? el('span', { class: 'tag red' }, labels[e.event] || e.event) : (labels[e.event] || e.event)),
      el('td', {}, e.user || ''), el('td', {}, e.device || ''), el('td', { class: 'mono' }, e.ip || ''),
      el('td', { class: 'muted small' }, e.detail || ''))), 'Keine Einträge');
  } catch (e) {
    toast(e.message, true);
  }
}
$('#btn-reload-audit').addEventListener('click', loadAudit);

function deviceChecks(selected) {
  const box = el('div', { class: 'device-checks' });
  const inputs = state.admin.devices.map((d) => {
    const cb = el('input', { type: 'checkbox', value: d.id, checked: selected.includes(d.id) });
    box.append(check(`${d.name} (${OS_NAMES[d.os] || d.os})`, cb));
    return cb;
  });
  if (!inputs.length) box.append(el('p', { class: 'muted small' }, 'Noch keine Geräte registriert.'));
  return { box, values: () => inputs.filter((i) => i.checked).map((i) => i.value) };
}

function genPassword() {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
  const a = crypto.getRandomValues(new Uint32Array(14));
  return Array.from(a, (n) => chars[n % chars.length]).join('');
}

$('#btn-add-user').addEventListener('click', () => {
  const name = el('input', { required: true, pattern: '[a-zA-Z0-9][a-zA-Z0-9._-]*', placeholder: 'max.mustermann', autocomplete: 'off' });
  const display = el('input', { placeholder: 'Max Mustermann', autocomplete: 'off' });
  const pw = el('input', { required: true, minlength: 10, value: genPassword(), autocomplete: 'new-password' });
  const admin = el('input', { type: 'checkbox' });
  const devs = deviceChecks([]);
  dialog({
    title: 'Benutzer anlegen',
    body: [field('Benutzername', name), field('Anzeigename', display), field('Startpasswort (bitte notieren)', pw),
      check('Administrator', admin), field('Erlaubte Geräte', devs.box)],
    okText: 'Anlegen',
    onOk: async () => {
      await api('POST', '/api/admin/users', { name: name.value, displayName: display.value, password: pw.value, admin: admin.checked, devices: devs.values() });
      toast('Benutzer angelegt');
      loadAdmin();
    },
  });
});

function editUser(u) {
  const display = el('input', { value: u.displayName });
  const pw = el('input', { placeholder: 'leer lassen = unverändert', minlength: 10, autocomplete: 'new-password' });
  const admin = el('input', { type: 'checkbox', checked: u.admin });
  const disabled = el('input', { type: 'checkbox', checked: !!u.disabled });
  const devs = deviceChecks(u.devices);
  dialog({
    title: `Benutzer „${u.name}“`,
    body: [field('Anzeigename', display), field('Neues Passwort', pw), check('Administrator', admin), check('Deaktiviert', disabled), field('Erlaubte Geräte', devs.box)],
    okText: 'Speichern',
    onOk: async () => {
      const patch = { displayName: display.value, devices: devs.values() };
      if (pw.value) patch.password = pw.value;
      if (admin.checked !== u.admin) patch.admin = admin.checked;
      if (disabled.checked !== !!u.disabled) patch.disabled = disabled.checked;
      await api('PATCH', `/api/admin/users/${encodeURIComponent(u.name)}`, patch);
      toast('Gespeichert');
      loadAdmin();
    },
  });
}

function deleteUser(u) {
  dialog({
    title: 'Benutzer löschen?',
    body: el('p', {}, `„${u.displayName}“ (${u.name}) wird gelöscht und sofort abgemeldet.`),
    okText: 'Löschen', danger: true,
    onOk: async () => { await api('DELETE', `/api/admin/users/${encodeURIComponent(u.name)}`); toast('Benutzer gelöscht'); loadAdmin(); },
  });
}

function renameDevice(d) {
  const name = el('input', { value: d.name, required: true, maxlength: 64 });
  dialog({
    title: 'Gerät umbenennen', body: field('Name', name), okText: 'Speichern',
    onOk: async () => { await api('PATCH', `/api/admin/devices/${d.id}`, { name: name.value }); loadAdmin(); },
  });
}

function deleteDevice(d) {
  dialog({
    title: 'Gerät entfernen?',
    body: el('p', {}, `„${d.name}“ wird abgemeldet und kann sich erst nach erneuter Registrierung wieder verbinden.`),
    okText: 'Entfernen', danger: true,
    onOk: async () => { await api('DELETE', `/api/admin/devices/${d.id}`); toast('Gerät entfernt'); loadAdmin(); },
  });
}

$('#btn-add-device').addEventListener('click', () => {
  const a = state.admin;
  const key = a.enrollKey;
  const origin = location.origin;
  const win = `[Net.ServicePointManager]::ServerCertificateValidationCallback={$true}; irm "${origin}/install/agent.ps1?key=${key}" | iex`;
  const unix = `curl -kfsSL "${origin}/install/agent.sh?key=${key}" | sh`;
  const cmd = (id, text) => el('div', { class: 'cmd' }, el('code', { id }, text),
    el('button', { type: 'button', class: 'btn copy', onclick: () => copyText(text) }, 'Kopieren'));
  const missing = ['windows', 'darwin', 'linux'].filter((os) => !a.agents.some((b) => b.os === os));
  dialog({
    title: 'Gerät hinzufügen',
    body: [
      el('p', { class: 'muted small' }, 'Auf dem Rechner, der ferngesteuert werden soll, einmal ausführen. Das Skript prüft die Prüfsumme des Agents und pinnt das Server-Zertifikat.'),
      el('h3', {}, 'Windows'), el('p', { class: 'muted small' }, 'PowerShell „Als Administrator ausführen“ – installiert den Dienst (funktioniert auch am Sperr- und Anmeldebildschirm).'),
      cmd('cmd-win', win),
      el('h3', {}, 'macOS / Linux'), cmd('cmd-unix', unix),
      missing.length ? el('p', { class: 'hint' }, `Hinweis: Für ${missing.map((m) => OS_NAMES[m]).join(', ')} liegt kein Agent auf dem Server.`) : null,
      el('p', { class: 'muted small' }, 'Registrierungsschlüssel: ', el('code', {}, key)),
    ],
    okText: 'Schlüssel erneuern',
    danger: true,
    onOk: async () => {
      const r = await api('POST', '/api/admin/enroll-key');
      state.admin.enrollKey = r.enrollKey;
      toast('Neuer Registrierungsschlüssel erstellt – alte Befehle sind ungültig');
    },
  });
});

// ---------------------------------------------------------------------------
// Remote session

const isMac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

function enterFullscreen() {
  const s = $('#view-session');
  if (!document.fullscreenElement && s.requestFullscreen) {
    // Must be called synchronously within the click handler.
    $('#view-session').classList.remove('hidden');
    s.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
  }
}

function startSession(device, opts = {}) {
  if (state.session) state.session.close('switch');
  state.session = new Session(device, opts);
}

class Session {
  constructor(device, opts) {
    this.device = device;
    this.opts = opts;
    this.id = null;
    this.pc = null;
    this.control = null;
    this.motion = null;
    this.pendingCandidates = [];
    this.closed = false;
    this.userClosed = false;
    this.retries = 0;
    this.info = {};
    this.cursorMode = 'stream';
    this.pointerLocked = false;
    this.wheelAcc = { x: 0, y: 0 };
    this.keyChain = Promise.resolve();
    this.lastClip = null;
    this.pingRTT = null;
    this.prevStats = null;
    this.swapMeta = settings.swapMeta ?? (isMac !== (device.os === 'darwin'));

    show('session');
    $('#session-name').textContent = device.name;
    $('#swap-meta').checked = this.swapMeta;
    this.setStatus('Verbinde…', true);
    this.bindUI();
    this.request();
  }

  setStatus(text, spinner = false) {
    $('#session-overlay').classList.toggle('hidden', !text);
    $('#session-status').textContent = text || '';
    $('.spinner').classList.toggle('hidden', !spinner);
  }

  request() {
    this.id = null;
    const codecs = [...new Set(((RTCRtpReceiver.getCapabilities && RTCRtpReceiver.getCapabilities('video')) || { codecs: [] })
      .codecs.map((c) => c.mimeType))];
    sendSignal({ type: 'connect', target: this.device.id, forceRelay: settings.forceRelay, takeover: !!this.opts.takeover, codecs });
    this.opts.takeover = false;
  }

  fail(reason) {
    toast(reason, true);
    this.close('error');
  }

  devicesUpdated() {
    const d = state.devices.find((x) => x.id === this.device.id);
    if (d) this.device = d;
  }

  signalingLost() {
    // The media path is independent of the server; keep the session if
    // it is up, otherwise retry once the WebSocket is back.
    if (!this.pc || this.pc.connectionState !== 'connected') this.scheduleRetry('Verbindung zum Server verloren');
  }

  onSignal(msg) {
    switch (msg.type) {
      case 'session-started':
        if (msg.target !== this.device.id) return;
        this.id = msg.session;
        this.createPC(msg.iceServers || []);
        break;
      case 'offer':
        if (msg.session === this.id) this.onOffer(msg.sdp);
        break;
      case 'candidate':
        if (msg.session !== this.id) return;
        if (this.pc && this.pc.remoteDescription) this.pc.addIceCandidate(msg.candidate).catch(() => {});
        else this.pendingCandidates.push(msg.candidate);
        break;
      case 'end':
        if (msg.session !== this.id) return;
        this.ended(msg.reason);
        break;
    }
  }

  createPC(iceServers) {
    const pc = new RTCPeerConnection({
      iceServers,
      iceTransportPolicy: settings.forceRelay ? 'relay' : 'all',
    });
    this.pc = pc;
    pc.onicecandidate = (ev) => {
      if (ev.candidate) sendSignal({ type: 'candidate', session: this.id, candidate: ev.candidate.toJSON() });
    };
    pc.ontrack = (ev) => {
      // Render every frame immediately: no extra jitter buffering on a LAN.
      try { ev.receiver.jitterBufferTarget = 0; } catch { /* unsupported */ }
      try { ev.receiver.playoutDelayHint = 0; } catch { /* unsupported */ }
      const v = $('#video');
      if (!v.srcObject) v.srcObject = new MediaStream();
      v.srcObject.addTrack(ev.track);
      if (ev.track.kind === 'audio') this.tryUnmute();
      v.play().catch(() => {});
    };
    pc.ondatachannel = (ev) => {
      const dc = ev.channel;
      if (dc.label === 'input') {
        this.control = dc;
        dc.onmessage = (m) => this.onControl(m.data);
        dc.onopen = () => this.syncClipboard();
      } else if (dc.label === 'motion') {
        this.motion = dc;
      }
    };
    pc.onconnectionstatechange = () => {
      const s = pc.connectionState;
      if (s === 'connected') {
        this.retries = 0;
        this.setStatus('');
        $('#tb-dot').classList.add('ok');
        this.flashToolbar();
        this.startStats();
      } else if (s === 'disconnected') {
        $('#tb-dot').classList.remove('ok');
        this.setStatus('Verbindung unterbrochen – stelle wieder her…', true);
      } else if (s === 'failed') {
        this.scheduleRetry('Verbindung fehlgeschlagen');
      }
    };
  }

  async onOffer(sdp) {
    const pc = this.pc;
    await pc.setRemoteDescription({ type: 'offer', sdp });
    for (const c of this.pendingCandidates) pc.addIceCandidate(c).catch(() => {});
    this.pendingCandidates = [];
    const answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);
    sendSignal({ type: 'answer', session: this.id, sdp: pc.localDescription.sdp });
  }

  ended(reason) {
    if (this.closed) return;
    const permanent = /übernommen|Administrator|anderen Arbeitsplatz|Berechtigung|wrong|revoked/i.test(reason || '');
    if (permanent || this.userClosed) {
      toast(reason || 'Sitzung beendet', !this.userClosed);
      this.close('ended');
    } else {
      this.scheduleRetry(reason || 'Sitzung beendet');
    }
  }

  scheduleRetry(reason) {
    if (this.closed || this.userClosed) return;
    this.teardownPC();
    if (this.retries >= 8) {
      toast(`${reason}. Bitte erneut verbinden.`, true);
      this.close('failed');
      return;
    }
    const delay = Math.min(1000 * 2 ** this.retries++, 10000);
    this.setStatus(`${reason} – neuer Versuch in ${Math.round(delay / 1000)} s…`, true);
    clearTimeout(this.retryTimer);
    this.retryTimer = setTimeout(() => {
      if (this.closed) return;
      if (state.ws && state.ws.readyState === WebSocket.OPEN) this.request();
      else this.scheduleRetry('Warte auf Server');
    }, delay);
  }

  teardownPC() {
    clearInterval(this.statsTimer);
    clearInterval(this.pingTimer);
    if (this.pc) { this.pc.onconnectionstatechange = null; this.pc.close(); }
    this.pc = null;
    this.control = null;
    this.motion = null;
    const v = $('#video');
    v.srcObject = null;
    $('#tb-dot').classList.remove('ok');
  }

  close(why) {
    if (this.closed) return;
    this.closed = true;
    clearTimeout(this.retryTimer);
    if (this.id) sendSignal({ type: 'end', session: this.id, reason: why === 'user' ? 'viewer disconnected' : why });
    this.teardownPC();
    this.unbindUI();
    if (document.pointerLockElement) document.exitPointerLock();
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    state.session = null;
    show(KIOSK ? 'kiosk' : 'app');
    renderKiosk();
  }

  // ---- control channel (agent -> viewer)

  onControl(data) {
    let m;
    try { m = JSON.parse(data); } catch { return; }
    switch (m.t) {
      case 'info':
        this.info = m;
        this.cursorMode = m.cursor;
        this.applyCursor(m.shape || 'default');
        $('#btn-audio').classList.toggle('hidden', !m.audio);
        this.showDesktop(m.desktop);
        break;
      case 'cursor':
        this.applyCursor(m.c);
        break;
      case 'clip':
        this.lastClip = m.text;
        if (document.hasFocus() && navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(m.text).catch(() => {});
        }
        break;
      case 'desktop':
        this.showDesktop(m.name);
        break;
      case 'p':
        this.pingRTT = performance.now() - m.ts;
        break;
    }
  }

  showDesktop(name) {
    const b = $('#tb-desktop');
    const label = { Winlogon: 'Sperr-/Anmeldebildschirm', 'Screen-saver': 'Bildschirmschoner' }[name];
    b.textContent = label || name || '';
    b.classList.toggle('hidden', !label);
  }

  applyCursor(shape) {
    const v = $('#video');
    if (this.pointerLocked) return;
    // With a locally drawn cursor the pointer moves without any latency;
    // otherwise the cursor is part of the video and the local one hidden.
    v.style.cursor = this.cursorMode === 'local' ? (shape || 'default') : 'none';
  }

  // ---- audio

  tryUnmute() {
    const v = $('#video');
    v.muted = false;
    v.play().then(() => $('#btn-unmute').classList.add('hidden')).catch(() => {
      v.muted = true;
      v.play().catch(() => {});
      $('#btn-unmute').classList.remove('hidden');
    });
    this.updateAudioBtn();
  }

  updateAudioBtn() {
    $('#btn-audio').textContent = $('#video').muted ? '🔇' : '🔊';
  }

  // ---- input (viewer -> agent)

  send(obj, reliable = true) {
    const dc = reliable ? this.control : (this.motion || this.control);
    if (dc && dc.readyState === 'open') dc.send(JSON.stringify(obj));
  }

  videoPoint(ev) {
    const v = $('#video');
    const r = v.getBoundingClientRect();
    const vw = v.videoWidth || r.width, vh = v.videoHeight || r.height;
    const scale = Math.min(r.width / vw, r.height / vh);
    const w = vw * scale, h = vh * scale;
    const ox = r.left + (r.width - w) / 2, oy = r.top + (r.height - h) / 2;
    return { x: (ev.clientX - ox) / w, y: (ev.clientY - oy) / h };
  }

  mapCode(code) {
    if (!this.swapMeta) return code;
    return { MetaLeft: 'ControlLeft', ControlLeft: 'MetaLeft', MetaRight: 'ControlRight', ControlRight: 'MetaRight' }[code] || code;
  }

  async syncClipboard() {
    if (!this.info.clipboard && this.info.t) return;
    if (!navigator.clipboard || !navigator.clipboard.readText || !document.hasFocus()) return;
    try {
      const text = await navigator.clipboard.readText();
      if (text && text !== this.lastClip && text.length < 1 << 20) {
        this.lastClip = text;
        this.send({ t: 'clip', text });
      }
    } catch { /* permission denied */ }
  }

  bindUI() {
    const v = $('#video');
    const h = this.handlers = {};

    h.pointermove = (ev) => {
      if (this.pointerLocked) {
        this.send({ t: 'r', dx: ev.movementX, dy: ev.movementY }, false);
        return;
      }
      const events = ev.getCoalescedEvents ? ev.getCoalescedEvents() : [];
      const last = events.length ? events[events.length - 1] : ev;
      const p = this.videoPoint(last);
      if (p.x < 0 || p.x > 1 || p.y < 0 || p.y > 1) return;
      this.send({ t: 'm', x: +p.x.toFixed(5), y: +p.y.toFixed(5) }, false);
    };
    h.pointerdown = (ev) => {
      ev.preventDefault();
      v.focus();
      if (ev.pointerType === 'mouse' || ev.pointerType === 'pen' || ev.pointerType === 'touch') {
        if (!this.pointerLocked) {
          const p = this.videoPoint(ev);
          // Position travels on the reliable channel right before the click.
          this.send({ t: 'm', x: p.x, y: p.y });
        }
        v.setPointerCapture(ev.pointerId);
        this.send({ t: 'b', b: ev.button < 0 ? 0 : ev.button, d: true });
      }
    };
    h.pointerup = (ev) => {
      ev.preventDefault();
      if (!this.pointerLocked) {
        const p = this.videoPoint(ev);
        this.send({ t: 'm', x: p.x, y: p.y });
      }
      this.send({ t: 'b', b: ev.button < 0 ? 0 : ev.button, d: false });
    };
    h.wheel = (ev) => {
      ev.preventDefault();
      const f = ev.deltaMode === 1 ? 40 : ev.deltaMode === 2 ? 360 : 1.2;
      this.wheelAcc.x += ev.deltaX * f;
      this.wheelAcc.y += -ev.deltaY * f;
      const dx = Math.trunc(this.wheelAcc.x), dy = Math.trunc(this.wheelAcc.y);
      if (dx || dy) {
        this.wheelAcc.x -= dx; this.wheelAcc.y -= dy;
        this.send({ t: 'w', dx, dy });
      }
    };
    h.contextmenu = (ev) => ev.preventDefault();

    h.keydown = (ev) => {
      if (!this.capturingKeys(ev)) return;
      // Ctrl+Alt+Shift+M toggles the toolbar locally.
      if (ev.code === 'KeyM' && ev.ctrlKey && ev.altKey && ev.shiftKey) {
        ev.preventDefault();
        this.toggleToolbar();
        return;
      }
      ev.preventDefault();
      if (ev.repeat && /^(Shift|Control|Alt|Meta)/.test(ev.code)) return;
      const code = this.mapCode(ev.code);
      // Paste: push the local clipboard first, then the key.
      if (ev.code === 'KeyV' && (ev.ctrlKey || ev.metaKey) && this.info.clipboard) {
        this.keyChain = this.keyChain.then(() => this.syncClipboard()).then(() => this.send({ t: 'k', c: code, d: true }));
      } else {
        this.keyChain = this.keyChain.then(() => this.send({ t: 'k', c: code, d: true }));
      }
    };
    h.keyup = (ev) => {
      if (!this.capturingKeys(ev)) return;
      ev.preventDefault();
      const code = this.mapCode(ev.code);
      this.keyChain = this.keyChain.then(() => this.send({ t: 'k', c: code, d: false }));
    };
    h.blur = () => this.send({ t: 'x' });
    h.focus = () => this.syncClipboard();
    h.visibility = () => { if (document.hidden) this.send({ t: 'x' }); };
    h.pointerlockchange = () => {
      this.pointerLocked = document.pointerLockElement === v;
      $('#btn-pointer').classList.toggle('active', this.pointerLocked);
      v.style.cursor = this.pointerLocked ? 'none' : '';
      if (!this.pointerLocked) this.applyCursor('default');
    };
    h.fullscreenchange = () => {
      const fs = !!document.fullscreenElement;
      $('#btn-fullscreen').classList.toggle('active', fs);
      // In fullscreen the browser lets us capture system shortcuts
      // (Alt+Tab, Win key, Esc …) and pass them to the remote machine.
      if (fs && navigator.keyboard && navigator.keyboard.lock) navigator.keyboard.lock().catch(() => {});
      if (!fs && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
    };
    h.hot = () => this.showToolbar();
    h.toolbarLeave = () => this.hideToolbarSoon();
    h.toolbarEnter = () => clearTimeout(this.toolbarTimer);

    v.addEventListener('pointermove', h.pointermove);
    v.addEventListener('pointerdown', h.pointerdown);
    v.addEventListener('pointerup', h.pointerup);
    v.addEventListener('wheel', h.wheel, { passive: false });
    v.addEventListener('contextmenu', h.contextmenu);
    window.addEventListener('keydown', h.keydown, true);
    window.addEventListener('keyup', h.keyup, true);
    window.addEventListener('blur', h.blur);
    window.addEventListener('focus', h.focus);
    document.addEventListener('visibilitychange', h.visibility);
    document.addEventListener('pointerlockchange', h.pointerlockchange);
    document.addEventListener('fullscreenchange', h.fullscreenchange);
    $('#hotzone').addEventListener('pointerenter', h.hot);
    $('#toolbar').addEventListener('pointerleave', h.toolbarLeave);
    $('#toolbar').addEventListener('pointerenter', h.toolbarEnter);

    $('#btn-disconnect').onclick = () => { this.userClosed = true; this.close('user'); };
    $('#btn-fullscreen').onclick = () => {
      if (document.fullscreenElement) document.exitFullscreen();
      else $('#view-session').requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
    };
    $('#btn-pointer').onclick = () => v.requestPointerLock({ unadjustedMovement: true }).catch(() => v.requestPointerLock());
    $('#btn-cad').onclick = () => this.send({ t: 'sas' });
    $('#btn-stats').onclick = () => $('#stats-detail').classList.toggle('hidden');
    $('#btn-audio').onclick = () => { v.muted = !v.muted; this.updateAudioBtn(); };
    $('#btn-unmute').onclick = () => { v.muted = false; v.play(); $('#btn-unmute').classList.add('hidden'); this.updateAudioBtn(); };
    $('#swap-meta').onchange = (ev) => {
      this.send({ t: 'x' });
      this.swapMeta = ev.target.checked;
      settings.swapMeta = this.swapMeta;
      saveSettings();
    };
    $('#stats').classList.toggle('hidden', !settings.showStats);
  }

  unbindUI() {
    const v = $('#video');
    const h = this.handlers;
    v.removeEventListener('pointermove', h.pointermove);
    v.removeEventListener('pointerdown', h.pointerdown);
    v.removeEventListener('pointerup', h.pointerup);
    v.removeEventListener('wheel', h.wheel);
    v.removeEventListener('contextmenu', h.contextmenu);
    window.removeEventListener('keydown', h.keydown, true);
    window.removeEventListener('keyup', h.keyup, true);
    window.removeEventListener('blur', h.blur);
    window.removeEventListener('focus', h.focus);
    document.removeEventListener('visibilitychange', h.visibility);
    document.removeEventListener('pointerlockchange', h.pointerlockchange);
    document.removeEventListener('fullscreenchange', h.fullscreenchange);
    $('#hotzone').removeEventListener('pointerenter', h.hot);
    $('#toolbar').removeEventListener('pointerleave', h.toolbarLeave);
    $('#toolbar').removeEventListener('pointerenter', h.toolbarEnter);
    v.style.cursor = '';
    $('#toolbar').classList.add('collapsed');
    $('#stats-detail').classList.add('hidden');
    $('#btn-unmute').classList.add('hidden');
  }

  capturingKeys(ev) {
    if (this.closed || !this.control) return false;
    if ($('#dlg').open) return false;
    const t = ev.target;
    return !(t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA') && t.type !== 'checkbox');
  }

  // ---- toolbar

  showToolbar() {
    $('#toolbar').classList.remove('collapsed');
    this.hideToolbarSoon(4000);
  }

  hideToolbarSoon(ms = 1200) {
    clearTimeout(this.toolbarTimer);
    this.toolbarTimer = setTimeout(() => $('#toolbar').classList.add('collapsed'), ms);
  }

  toggleToolbar() {
    const t = $('#toolbar');
    if (t.classList.contains('collapsed')) { clearTimeout(this.toolbarTimer); t.classList.remove('collapsed'); } else t.classList.add('collapsed');
  }

  flashToolbar() {
    this.showToolbar();
    this.hideToolbarSoon(2500);
  }

  // ---- stats

  startStats() {
    clearInterval(this.statsTimer);
    clearInterval(this.pingTimer);
    this.pingTimer = setInterval(() => this.send({ t: 'p', ts: performance.now() }), 1000);
    this.statsTimer = setInterval(() => this.updateStats(), 1000);
  }

  async updateStats() {
    if (!this.pc) return;
    const report = await this.pc.getStats();
    let video, pair, local, remote;
    const byId = new Map();
    report.forEach((s) => byId.set(s.id, s));
    report.forEach((s) => {
      if (s.type === 'inbound-rtp' && s.kind === 'video') video = s;
      if (s.type === 'transport' && s.selectedCandidatePairId) pair = byId.get(s.selectedCandidatePairId);
    });
    if (!pair) report.forEach((s) => { if (s.type === 'candidate-pair' && s.nominated && s.state === 'succeeded') pair = s; });
    if (pair) { local = byId.get(pair.localCandidateId); remote = byId.get(pair.remoteCandidateId); }
    if (!video) return;

    const prev = this.prevStats;
    this.prevStats = { ...video, at: performance.now() };
    if (!prev) return;
    const dt = (this.prevStats.at - prev.at) / 1000;
    const mbit = ((video.bytesReceived - prev.bytesReceived) * 8) / dt / 1e6;
    const frames = video.framesDecoded - prev.framesDecoded;
    const fps = video.framesPerSecond ?? frames / dt;
    const decodeMs = frames > 0 ? ((video.totalDecodeTime - prev.totalDecodeTime) / frames) * 1000 : 0;
    const jbCount = video.jitterBufferEmittedCount - prev.jitterBufferEmittedCount;
    const bufferMs = jbCount > 0 ? ((video.jitterBufferDelay - prev.jitterBufferDelay) / jbCount) * 1000 : 0;
    const rttMs = pair && pair.currentRoundTripTime !== undefined ? pair.currentRoundTripTime * 1000 : null;
    const lost = video.packetsLost - prev.packetsLost;
    const relayed = (local && local.candidateType === 'relay') || (remote && remote.candidateType === 'relay');
    // Network one-way + jitter buffer + decode + ~1 frame capture/encode.
    const est = (rttMs ?? 1) / 2 + bufferMs + decodeMs + (fps > 0 ? 1000 / fps : 16);
    const cls = est < 25 ? 'good' : est < 50 ? 'mid' : 'bad';

    const s = $('#stats');
    s.replaceChildren(
      el('span', { class: cls }, `≈${est.toFixed(0)} ms`),
      ` · ${relayed ? 'Relay' : 'Direkt'} · ${fps.toFixed(0)} fps · ${mbit.toFixed(1)} Mbit/s`,
    );
    $('#stats-detail').textContent = [
      `Verbindung    ${relayed ? 'über Server-Relay (TURN)' : 'direkt (Peer-to-Peer)'}`,
      `Netzwerk-RTT  ${rttMs !== null ? rttMs.toFixed(1) + ' ms' : '–'}`,
      `Eingabe-RTT   ${this.pingRTT !== null ? this.pingRTT.toFixed(1) + ' ms' : '–'}`,
      `Jitter-Puffer ${bufferMs.toFixed(1)} ms`,
      `Dekodierung   ${decodeMs.toFixed(1)} ms (${video.decoderImplementation || '?'}${video.powerEfficientDecoder ? ', Hardware' : ''})`,
      `Bildrate      ${fps.toFixed(0)} fps`,
      `Datenrate     ${mbit.toFixed(1)} Mbit/s`,
      `Auflösung     ${video.frameWidth || '?'}×${video.frameHeight || '?'}`,
      `Paketverlust  ${lost}`,
      `Encoder       ${encoderLabel(this.info.encoder || '')} (${this.info.capture || '?'})`,
      `Geschätzte Latenz ≈ ${est.toFixed(0)} ms (ohne Bildschirm-Eingabeverzögerung)`,
    ].join('\n');
  }
}

boot();
