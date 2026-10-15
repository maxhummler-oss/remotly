'use strict';
/* Remotly web viewer & admin UI. No dependencies, no build step. */

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));
const params = new URLSearchParams(location.search);
const KIOSK = params.has('kiosk');
const KIOSK_IDLE_MS = 2 * 60 * 1000; // shared thin clients: log out when idle

const state = {
  me: null,
  devices: [],
  ws: null,
  wsRetry: 0,
  admin: null,
  session: null,
};

// ---------------------------------------------------------------------------
// Icons (stroke, 24×24) and OS logos

const ICONS = {
  display: '<rect x="3" y="4" width="18" height="12" rx="2.5"/><path d="M9 20h6M12 16v4"/>',
  lock: '<rect x="5" y="11" width="14" height="10" rx="2.5"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  shield: '<path d="M12 3l7 3v6c0 4.5-3 7.6-7 9-4-1.4-7-4.5-7-9V6z"/><path d="M9 12l2 2 4-4"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3M15 8l2 2"/>',
  sliders: '<path d="M4 7h10M18 7h2M4 17h4M12 17h8"/><circle cx="16" cy="7" r="2"/><circle cx="10" cy="17" r="2"/>',
  logout: '<path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 17l-5-5 5-5M5 12h11"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  refresh: '<path d="M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7"/>',
  speaker: '<path d="M4 9h4l5-4v14l-5-4H4z"/><path d="M17 9a4 4 0 0 1 0 6M19.5 6.5a8 8 0 0 1 0 11"/>',
  mute: '<path d="M4 9h4l5-4v14l-5-4H4z"/><path d="M17 9l5 6M22 9l-5 6"/>',
  expand: '<path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"/>',
  target: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="2.5"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3"/>',
  keyboard: '<rect x="2.5" y="6" width="19" height="12" rx="2.5"/><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M7 14h10"/>',
  gauge: '<path d="M4 18a8 8 0 1 1 16 0"/><path d="M12 18l4-6"/>',
  close: '<path d="M6 6l12 12M18 6L6 18"/>',
  power: '<path d="M12 3v8"/><path d="M6.3 6.8a8 8 0 1 0 11.4 0"/>',
  arrow: '<path d="M5 12h14M13 6l6 6-6 6"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7.5h.01"/>',
  alert: '<path d="M12 3l10 18H2z"/><path d="M12 10v5M12 18h.01"/>',
  check: '<path d="M5 12l5 5 9-10"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
};
function icon(name) {
  return `<svg class="i" viewBox="0 0 24 24" aria-hidden="true">${ICONS[name] || ''}</svg>`;
}
function hydrateIcons(root = document) {
  root.querySelectorAll('[data-icon]').forEach((e) => {
    if (!e.querySelector('svg')) e.insertAdjacentHTML('afterbegin', icon(e.dataset.icon));
  });
}

const OS_ICONS = {
  windows: '<svg viewBox="0 0 24 24"><path d="M3 5.5 10.5 4.5v7H3zM11.5 4.4 21 3v8.5h-9.5zM3 12.5h7.5v7L3 18.5zM11.5 12.5H21V21l-9.5-1.4z"/></svg>',
  darwin: '<svg viewBox="0 0 24 24"><path d="M16.4 12.6c0-2.5 2-3.6 2.1-3.7-1.2-1.7-3-1.9-3.6-2-1.5-.2-3 .9-3.8.9-.8 0-2-.9-3.3-.9-1.7 0-3.3 1-4.1 2.5-1.8 3.1-.5 7.6 1.3 10.1.8 1.2 1.8 2.6 3.1 2.5 1.3-.1 1.7-.8 3.2-.8s1.9.8 3.2.8c1.4 0 2.2-1.2 3-2.4.9-1.4 1.3-2.7 1.3-2.8-.1 0-2.4-.9-2.4-3.2zM14 5.3c.7-.8 1.1-1.9 1-3-1 0-2.2.7-2.9 1.5-.6.7-1.2 1.8-1 2.9 1.1.1 2.2-.6 2.9-1.4z"/></svg>',
  linux: '<svg viewBox="0 0 24 24"><path d="M12 2c-2.2 0-3.5 1.9-3.5 4.4 0 1.4.4 2.4-.4 3.9-.9 1.6-2.6 3.5-2.6 6.3 0 .6.1 1.1.3 1.6-.6.4-1.3.9-1.3 1.6 0 1.2 1.9 1.4 3.6 1.9 1 .3 1.6.3 2.2-.2.5.2 1.1.3 1.7.3s1.2-.1 1.7-.3c.6.5 1.2.5 2.2.2 1.7-.5 3.6-.7 3.6-1.9 0-.7-.7-1.2-1.3-1.6.2-.5.3-1 .3-1.6 0-2.8-1.7-4.7-2.6-6.3-.8-1.5-.4-2.5-.4-3.9C15.5 3.9 14.2 2 12 2zm-1.5 4.2c.4 0 .7.5.7 1.1s-.3 1.1-.7 1.1-.7-.5-.7-1.1.3-1.1.7-1.1zm3 0c.4 0 .7.5.7 1.1s-.3 1.1-.7 1.1-.7-.5-.7-1.1.3-1.1.7-1.1zM12 9.4c.9 0 2 .6 2 1s-1.1 1-2 1-2-.6-2-1 1.1-1 2-1z"/></svg>',
};
const OS_NAMES = { windows: 'Windows', darwin: 'macOS', linux: 'Linux' };
const osIcon = (os) => el('div', { class: 'os-icon', html: OS_ICONS[os] || OS_ICONS.linux });

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

function btn(label, opts = {}) {
  const b = el('button', { type: 'button', class: 'btn ' + (opts.cls || ''), title: opts.title, disabled: opts.disabled, onclick: opts.onclick });
  if (opts.icon) b.insertAdjacentHTML('afterbegin', icon(opts.icon));
  if (label) b.append(label);
  return b;
}

let toastTimer;
function toast(msg, isError = false, ms) {
  const t = $('#toast');
  $('#toast-text').textContent = msg;
  $('#toast-icon').innerHTML = icon(isError ? 'alert' : 'info');
  t.classList.toggle('err', isError);
  t.classList.add('hidden');
  void t.offsetWidth; // restart the entry animation
  t.classList.remove('hidden');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add('hidden'), ms || (isError ? 6000 : 3500));
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
  for (const v of ['login', 'setup2fa', 'app', 'kiosk', 'session']) {
    $('#view-' + v).classList.toggle('hidden', v !== view);
  }
  document.body.classList.toggle('in-session', view === 'session');
  if (view === 'app') requestAnimationFrame(moveSegThumb);
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

// Sheet dialog. body: Node or Node[]; onOk may throw to show an error.
function dialog({ title, body, okText = 'OK', danger = false, cancel = true, onOk }) {
  const dlg = $('#dlg');
  $('#dlg-title').textContent = title;
  const b = $('#dlg-body');
  b.replaceChildren(...(Array.isArray(body) ? body : [body]).filter(Boolean));
  hydrateIcons(b);
  $('#dlg-error').textContent = '';
  const ok = $('#dlg-ok');
  ok.textContent = okText;
  ok.className = 'btn ' + (danger ? 'danger solid' : 'primary');
  $('#dlg-cancel').classList.toggle('hidden', !cancel);
  return new Promise((resolve) => {
    $('#dlg-form').onsubmit = async (ev) => {
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
    dlg.onclose = () => resolve(undefined);
    dlg.showModal();
    const first = b.querySelector('input:not([type=checkbox])');
    if (first) first.focus();
  });
}

const field = (label, input) => el('label', { class: 'field' }, el('span', {}, label), input);
const check = (label, input) => el('label', { class: 'check' }, el('span', {}, label), input);

// ---------------------------------------------------------------------------
// Login (password, then optional authenticator code)

let loginNeedsCode = false;

async function boot() {
  hydrateIcons();
  try {
    state.me = await api('GET', '/api/me');
    enterApp();
  } catch (e) {
    const info = e.data || {};
    if (info.serverName && info.serverName !== 'Remotly') $('#login-server').textContent = info.serverName;
    $('#login-setup').classList.toggle('hidden', !info.needsSetup);
    showLogin();
  }
}

function showLogin() {
  setLoginStep(false);
  show('login');
  $('#login-user').focus();
}

function setLoginStep(code) {
  loginNeedsCode = code;
  $('#login-step-pass').classList.toggle('hidden', code);
  $('#login-step-code').classList.toggle('hidden', !code);
  $('#login-back').classList.toggle('hidden', !code);
  $('#login-submit').textContent = code ? 'Bestätigen' : 'Anmelden';
  $('#login-user').required = !code;
  $('#login-pass').required = !code;
  if (code) { $('#login-code').value = ''; $('#login-code').focus(); }
}

$('#login-back').addEventListener('click', () => { $('#login-error').textContent = ''; setLoginStep(false); });
// Submit automatically once six digits were entered (or pasted).
$('#login-code').addEventListener('input', (ev) => {
  if (ev.target.value.replace(/\D/g, '').length === 6) $('#login-form').requestSubmit();
});

$('#login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const submit = $('#login-submit');
  submit.disabled = true;
  $('#login-error').textContent = '';
  try {
    const body = { username: $('#login-user').value, password: $('#login-pass').value };
    if (loginNeedsCode) body.code = $('#login-code').value.replace(/\D/g, '');
    state.me = await api('POST', '/api/login', body);
    $('#login-pass').value = '';
    $('#login-code').value = '';
    enterApp();
  } catch (e) {
    if (e.data && e.data.needsCode && !loginNeedsCode) {
      setLoginStep(true);
    } else {
      $('#login-error').textContent = e.message;
      if (loginNeedsCode) { $('#login-code').value = ''; $('#login-code').focus(); }
    }
  } finally {
    submit.disabled = false;
  }
});

async function logout() {
  try { await api('POST', '/api/logout'); } catch { /* ignore */ }
  if (state.session) state.session.close('logout');
  if (state.ws) { state.ws.onclose = null; state.ws.close(); }
  state.ws = null;
  state.me = null;
  clearInterval(adminTimer);
  $('#login-pass').value = '';
  $('#toast').classList.add('hidden');
  showLogin();
}

function enterApp() {
  const me = state.me;
  if (me.mustSetup2FA) {
    show('setup2fa');
    return;
  }
  document.title = me.serverName && me.serverName !== 'Remotly' ? `Remotly · ${me.serverName}` : 'Remotly';
  $('#server-name').textContent = me.serverName && me.serverName !== 'Remotly' ? me.serverName : '';
  $('#user-name').textContent = me.displayName;
  $('#user-avatar').textContent = initials(me.displayName);
  $('#menu-name').textContent = me.displayName;
  $('#menu-login').textContent = me.name + (me.admin ? ' · Administrator' : '');
  $('#menu-2fa').textContent = me.totp ? 'Zwei-Faktor-Anmeldung: aktiv' : 'Zwei-Faktor-Anmeldung einrichten';
  $('#tab-admin').classList.toggle('hidden', !me.admin || KIOSK);
  const hour = new Date().getHours();
  const hello = hour < 11 ? 'Guten Morgen' : hour < 18 ? 'Hallo' : 'Guten Abend';
  $('#greeting').textContent = `${hello}, ${me.displayName.split(' ')[0]}`;
  if (KIOSK) {
    $('#kiosk-hello').textContent = `${hello}, ${me.displayName.split(' ')[0]}`;
    show('kiosk');
    kioskActivity();
  } else {
    show('app');
  }
  connectWS();
}

// Forced setup screen
$('#setup2fa-start').addEventListener('click', () => setup2FA(true));
$('#setup2fa-logout').addEventListener('click', logout);

async function setup2FA(forced = false) {
  let data;
  try { data = await api('POST', '/api/totp/setup'); } catch (e) { toast(e.message, true); return; }
  const code = el('input', { class: 'code', inputmode: 'numeric', maxlength: 7, autocomplete: 'one-time-code', placeholder: '000000', required: true });
  const res = await dialog({
    title: 'Zwei-Faktor-Anmeldung',
    body: [
      el('ol', { class: 'steps' },
        el('li', {}, 'Authenticator-App öffnen (z. B. Microsoft Authenticator, Google Authenticator, Apple Passwörter).'),
        el('li', {}, 'QR-Code scannen oder den Schlüssel eingeben.'),
        el('li', {}, 'Den angezeigten 6-stelligen Code hier eintragen.')),
      el('div', { class: 'qr' }, el('img', { src: data.qr, alt: 'QR-Code für die Authenticator-App', width: 196, height: 196 })),
      el('p', { class: 'muted small' }, 'Schlüssel: ', el('code', { class: 'wrap' }, data.secret)),
      field('Bestätigungscode', code),
    ],
    okText: 'Aktivieren',
    cancel: true,
    onOk: async () => api('POST', '/api/totp/confirm', { code: code.value.replace(/\D/g, '') }),
  });
  if (res) {
    state.me = res;
    toast('Zwei-Faktor-Anmeldung ist aktiv');
    if (forced) enterApp();
    else $('#menu-2fa').textContent = 'Zwei-Faktor-Anmeldung: aktiv';
  }
}

function manage2FA() {
  if (!state.me.totp) { setup2FA(); return; }
  if (state.me.require2FA) {
    dialog({ title: 'Zwei-Faktor-Anmeldung', body: el('p', {}, 'Die Zwei-Faktor-Anmeldung ist aktiv und auf diesem Server Pflicht. Bei Verlust des Telefons setzt ein Administrator sie zurück.'), cancel: false });
    return;
  }
  const pw = el('input', { type: 'password', autocomplete: 'current-password', required: true });
  const code = el('input', { class: 'code', inputmode: 'numeric', maxlength: 7, autocomplete: 'one-time-code', required: true });
  dialog({
    title: 'Zwei-Faktor-Anmeldung deaktivieren?',
    body: [el('p', { class: 'muted' }, 'Dein Konto ist dann nur noch durch das Passwort geschützt.'), field('Passwort', pw), field('Aktueller Code', code)],
    okText: 'Deaktivieren', danger: true,
    onOk: async () => {
      await api('POST', '/api/totp/disable', { password: pw.value, code: code.value.replace(/\D/g, '') });
      state.me.totp = false;
      $('#menu-2fa').textContent = 'Zwei-Faktor-Anmeldung einrichten';
      toast('Zwei-Faktor-Anmeldung deaktiviert');
    },
  });
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
    try { await api('GET', '/api/me'); } catch (e) {
      if (e.status === 401) {
        if (state.session) state.session.close('logout');
        toast('Abgemeldet – bitte erneut anmelden', true);
        state.me = null;
        showLogin();
        return;
      }
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
    case 'notice':
      toast(msg.reason, false, 8000);
      break;
    case 'error':
      if (state.session && !state.session.id) state.session.fail(msg.reason);
      else toast(msg.reason, true);
      break;
    default:
      if (state.session) state.session.onSignal(msg);
  }
}

// ---------------------------------------------------------------------------
// Device list

function busyByOthers(d) {
  return (d.inUseBy || []).filter((n) => n !== state.me.displayName);
}

function deviceStatus(d) {
  if (d.pending) return el('span', { class: 'chip pending' }, 'Wartet auf Freigabe');
  if (!d.online) return el('span', { class: 'chip' }, 'Offline');
  if (d.inUseBy && d.inUseBy.length) return el('span', { class: 'chip busy', title: d.inUseBy.join(', ') }, 'Aktiv: ' + d.inUseBy.join(', '));
  return el('span', { class: 'chip on' }, 'Bereit');
}

function encoderLabel(enc) {
  return {
    h264_nvenc: 'NVIDIA NVENC', h264_amf: 'AMD AMF', h264_qsv: 'Intel Quick Sync',
    h264_videotoolbox: 'Apple VideoToolbox', libx264: 'Software', h264_mf: 'Media Foundation', libvpx: 'Software (VP8)',
  }[enc] || enc || '';
}

function renderDevices() {
  const list = $('#device-list');
  const q = ($('#device-search').value || '').toLowerCase();
  const devices = state.devices.filter((d) => !q || d.name.toLowerCase().includes(q));
  $('#no-devices').classList.toggle('hidden', state.devices.length > 0);
  const online = state.devices.filter((d) => d.online && !d.pending).length;
  $('#devices-sub').textContent = state.devices.length
    ? `${online} von ${state.devices.length} ${state.devices.length === 1 ? 'Rechner' : 'Rechnern'} online`
    : '';
  list.replaceChildren(...devices.map((d) => {
    const others = busyByOthers(d);
    const actions = el('div', { class: 'device-actions' });
    if (d.pending) {
      if (state.me.admin) actions.append(btn('Freigeben', { cls: 'primary small', icon: 'check', onclick: () => approveDevice(d) }));
    } else if (d.online) {
      if (others.length && state.me.admin) {
        actions.append(btn('Übernehmen', { cls: 'small', title: 'Andere Sitzung beenden und übernehmen', onclick: () => startSession(d, { takeover: true }) }));
        actions.append(btn('Zuschauen', { cls: 'primary small', icon: 'eye', title: 'Zusätzlich verbinden (Support) – der Benutzer wird informiert', onclick: () => startSession(d) }));
      } else {
        actions.append(btn('Verbinden', { cls: 'primary small', disabled: others.length > 0, onclick: () => startSession(d) }));
      }
    } else if (d.canWake) {
      actions.append(btn('Einschalten', { cls: 'small', icon: 'power', onclick: () => wakeDevice(d) }));
    }
    return el('div', { class: 'device glass ' + (d.online ? 'online' : 'offline') },
      el('div', { class: 'device-head' },
        osIcon(d.os),
        el('div', {},
          el('div', { class: 'device-name' }, d.name),
          el('div', { class: 'device-meta' }, [OS_NAMES[d.os] || d.os, d.encoder && encoderLabel(d.encoder)].filter(Boolean).join(' · ')))),
      el('div', { class: 'device-foot' }, deviceStatus(d), actions));
  }));
}

$('#device-search').addEventListener('input', renderDevices);

async function wakeDevice(d) {
  try {
    await api('POST', `/api/devices/${encodeURIComponent(d.id)}/wake`);
    toast(`Einschaltsignal an „${d.name}“ gesendet – das dauert bis zu einer Minute.`);
  } catch (e) {
    toast(e.message, true);
  }
}

async function approveDevice(d) {
  try {
    await api('PATCH', `/api/admin/devices/${encodeURIComponent(d.id)}`, { approve: true });
    toast(`„${d.name}“ freigegeben`);
    if (state.admin) loadAdmin();
  } catch (e) {
    toast(e.message, true);
  }
}

// ---------------------------------------------------------------------------
// Kiosk: lock-screen clock, big buttons, idle logout

let kioskAutoDone = false;
let kioskIdleTimer;

function kioskActivity() {
  if (!KIOSK) return;
  clearTimeout(kioskIdleTimer);
  kioskIdleTimer = setTimeout(() => {
    if (state.me && !state.session) { toast('Wegen Inaktivität abgemeldet'); logout(); }
  }, KIOSK_IDLE_MS);
}
if (KIOSK) {
  ['pointerdown', 'keydown', 'pointermove'].forEach((t) => document.addEventListener(t, () => { if (!state.session) kioskActivity(); }, { passive: true }));
  const tick = () => {
    const now = new Date();
    $('#kiosk-clock').textContent = now.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });
    $('#kiosk-date').textContent = now.toLocaleDateString('de-DE', { weekday: 'long', day: 'numeric', month: 'long' });
  };
  tick();
  setInterval(tick, 1000);
}

function renderKiosk() {
  if (!KIOSK) return;
  const box = $('#kiosk-devices');
  const wanted = params.get('device');
  const devices = (wanted ? state.devices.filter((d) => d.id === wanted || d.name === wanted) : state.devices).filter((d) => !d.pending);
  if (!devices.length) {
    box.replaceChildren(el('p', { class: 'muted' }, 'Dir ist noch kein Rechner zugewiesen.'));
    return;
  }
  box.replaceChildren(...devices.map((d) => {
    const others = busyByOthers(d);
    let label = 'Verbinden';
    if (!d.online) label = d.canWake ? 'Einschalten' : 'Offline';
    else if (others.length) label = 'Verwendet von ' + others.join(', ');
    const go = el('span', { class: 'go' }, label);
    if (d.online && !others.length) go.insertAdjacentHTML('beforeend', icon('arrow'));
    return el('button', {
      class: 'kiosk-device glass refract',
      disabled: (!d.online && !d.canWake) || others.length > 0,
      onclick: () => {
        if (!d.online) { wakeDevice(d); return; }
        enterFullscreen();
        startSession(d);
      },
    }, osIcon(d.os), el('div', { class: 'grow' }, el('div', { class: 'device-name' }, d.name),
      el('div', { class: 'device-meta' }, OS_NAMES[d.os] || d.os)), go);
  }));
  // Connect automatically when the user owns exactly one machine.
  if (!kioskAutoDone && devices.length === 1 && devices[0].online && !busyByOthers(devices[0]).length && params.has('auto')) {
    kioskAutoDone = true;
    startSession(devices[0]);
  }
}

$('#kiosk-logout').addEventListener('click', logout);

// ---------------------------------------------------------------------------
// Menu, settings, tabs

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
  const b = ev.target.closest('button');
  const action = b && b.dataset.action;
  if (action === 'logout') logout();
  if (action === 'password') changePassword();
  if (action === 'settings') openSettings();
  if (action === '2fa') manage2FA();
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
    okText: 'Fertig',
    cancel: false,
    onOk: () => {
      settings.forceRelay = relay.checked;
      settings.showStats = stats.checked;
      saveSettings();
    },
  });
}

function moveSegThumb() {
  const active = $('#tabs button.active');
  const thumb = $('#seg-thumb');
  if (!active || !thumb || !active.offsetWidth) return;
  thumb.style.width = active.offsetWidth + 'px';
  thumb.style.transform = `translateX(${active.offsetLeft}px)`;
}
window.addEventListener('resize', moveSegThumb);

$('#tabs').addEventListener('click', (ev) => {
  const b = ev.target.closest('button');
  const tab = b && b.dataset.tab;
  if (!tab) return;
  $$('#tabs button').forEach((t) => t.classList.toggle('active', t.dataset.tab === tab));
  moveSegThumb();
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
    if ($('#tab-admin-panel').classList.contains('hidden') || $('#dlg').open || !state.me) return;
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
  const pending = a.devices.filter((d) => d.pending).length;
  const with2fa = a.users.filter((u) => u.totp).length;

  const kpi = (n, label) => el('div', { class: 'kpi glass' }, el('b', {}, n), el('span', {}, label));
  $('#admin-kpis').replaceChildren(
    kpi(a.sessions.length, 'aktive Sitzungen'),
    kpi(`${online}/${a.devices.length}`, 'Geräte online'),
    kpi(`${with2fa}/${a.users.length}`, 'Benutzer mit 2FA'),
    kpi(pending, 'Geräte warten auf Freigabe'),
  );

  table('#tbl-sessions', ['Benutzer', 'Gerät', 'Von', 'Dauer', ''], a.sessions.map((s) => el('tr', {},
    el('td', {}, s.user), el('td', {}, s.device), el('td', { class: 'mono' }, s.ip), el('td', {}, fmtDuration(s.started)),
    el('td', { class: 'actions' }, btn('Beenden', {
      cls: 'danger small',
      onclick: async () => {
        try { await api('DELETE', `/api/admin/sessions/${s.id}`); toast('Sitzung beendet'); loadAdmin(); } catch (e) { toast(e.message, true); }
      },
    })))), 'Keine aktiven Sitzungen');

  table('#tbl-users', ['Name', 'Benutzer', 'Rolle', '2FA', 'Geräte', 'Letzte Anmeldung', ''], a.users.map((u) => el('tr', {},
    el('td', {}, u.displayName, ' ', u.disabled ? el('span', { class: 'tag red' }, 'deaktiviert') : null),
    el('td', { class: 'mono' }, u.name),
    el('td', {}, u.admin ? el('span', { class: 'tag accent' }, 'Admin') : el('span', { class: 'tag' }, 'Benutzer')),
    el('td', {}, u.totp ? el('span', { class: 'tag ok' }, 'aktiv') : el('span', { class: a.require2FA ? 'tag warn' : 'tag' }, 'aus')),
    el('td', {}, u.admin ? 'alle' : (u.devices.map((id) => devName[id]).filter(Boolean).join(', ') || '–')),
    el('td', {}, fmtTime(u.lastLogin)),
    el('td', { class: 'actions' },
      btn('Bearbeiten', { cls: 'small', onclick: () => editUser(u) }),
      btn('Löschen', { cls: 'danger small', onclick: () => deleteUser(u) })))), 'Keine Benutzer');

  table('#tbl-devices', ['Gerät', 'System', 'Aufnahme', 'IP', 'Zuletzt', ''], a.devices.map((d) => el('tr', {},
    el('td', {}, el('span', { class: 'chip ' + (d.pending ? 'pending' : d.online ? 'on' : '') }, d.name)),
    el('td', {}, `${OS_NAMES[d.os] || d.os} ${d.arch || ''}`, d.info && d.info.version ? el('div', { class: 'muted small' }, 'Agent ' + d.info.version) : null),
    el('td', {}, d.info ? `${encoderLabel(d.info.encoder)}${d.info.capture ? ' · ' + d.info.capture : ''}${d.info.audio ? ' · Ton' : ''}` : '–',
      d.info && d.info.desktop && d.info.desktop !== 'Default' ? el('div', { class: 'muted small' }, 'Desktop: ' + d.info.desktop) : null),
    el('td', { class: 'mono' }, d.lastIp || '–'),
    el('td', {}, d.online ? 'jetzt' : fmtTime(d.lastSeen)),
    el('td', { class: 'actions' },
      d.pending ? btn('Freigeben', { cls: 'primary small', icon: 'check', onclick: () => approveDevice(d) }) : null,
      btn('Umbenennen', { cls: 'small', onclick: () => renameDevice(d) }),
      btn('Entfernen', { cls: 'danger small', onclick: () => deleteDevice(d) })))), 'Noch keine Geräte registriert');

  $('#kiosk-cmd').textContent = `chromium --kiosk --noerrdialogs --disable-session-crashed-bubble "${location.origin}/?kiosk&auto"`;
  $('#ca-fp').textContent = a.caFingerprint || '(eigenes Zertifikat)';
  $('#ca-link').classList.toggle('hidden', !a.caFingerprint);
}
$('#copy-kiosk').addEventListener('click', () => copyText($('#kiosk-cmd').textContent));

async function loadAudit() {
  try {
    const entries = await api('GET', '/api/admin/audit');
    const labels = {
      'login': 'Anmeldung', 'login.failed': 'Fehlgeschlagene Anmeldung', 'session.start': 'Sitzung gestartet',
      'session.end': 'Sitzung beendet', 'session.denied': 'Zugriff verweigert', 'agent.enrolled': 'Gerät registriert',
      'agent.enroll_failed': 'Registrierung abgelehnt', 'agent.auth_failed': 'Gerät abgewiesen', 'device.wake': 'Wake-on-LAN',
      'device.renamed': 'Gerät umbenannt', 'device.revoked': 'Gerät entfernt', 'device.approved': 'Gerät freigegeben',
      'user.created': 'Benutzer angelegt', 'user.updated': 'Benutzer geändert', 'user.deleted': 'Benutzer gelöscht',
      'user.password_changed': 'Passwort geändert', 'user.2fa_enabled': '2FA aktiviert', 'user.2fa_disabled': '2FA deaktiviert',
      'enroll_key.rotated': 'Registrierungsschlüssel erneuert',
    };
    const bad = (ev) => /fail|denied/.test(ev);
    table('#tbl-audit', ['Zeit', 'Ereignis', 'Benutzer', 'Gerät', 'IP', 'Details'], (entries || []).map((e) => el('tr', {},
      el('td', { class: 'small nowrap' }, new Date(e.time).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'medium' })),
      el('td', {}, bad(e.event) ? el('span', { class: 'tag red' }, labels[e.event] || e.event) : (labels[e.event] || e.event)),
      el('td', {}, e.user || ''), el('td', {}, e.device || ''), el('td', { class: 'mono' }, e.ip || ''),
      el('td', { class: 'muted small' }, e.detail || ''))), 'Keine Einträge');
  } catch (e) {
    toast(e.message, true);
  }
}
$('#btn-reload-audit').addEventListener('click', loadAudit);

function deviceChecks(selected) {
  const box = el('div', { class: 'device-checks' });
  const inputs = state.admin.devices.filter((d) => !d.pending).map((d) => {
    const cb = el('input', { type: 'checkbox', value: d.id, checked: selected.includes(d.id) });
    box.append(check(`${d.name} (${OS_NAMES[d.os] || d.os})`, cb));
    return cb;
  });
  if (!inputs.length) box.append(el('p', { class: 'muted small' }, 'Noch keine freigegebenen Geräte.'));
  return { box, values: () => inputs.filter((i) => i.checked).map((i) => i.value) };
}

function genPassword() {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789';
  const a = crypto.getRandomValues(new Uint32Array(16));
  return Array.from(a, (n) => chars[n % chars.length]).join('');
}

$('#btn-add-user').addEventListener('click', () => {
  const name = el('input', { required: true, pattern: '[a-zA-Z0-9][a-zA-Z0-9._\\-]*', placeholder: 'max.mustermann', autocomplete: 'off', autocapitalize: 'none' });
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
  const reset = el('input', { type: 'checkbox' });
  const devs = deviceChecks(u.devices);
  dialog({
    title: u.displayName,
    body: [
      field('Anzeigename', display), field('Neues Passwort', pw),
      check('Administrator', admin), check('Deaktiviert', disabled),
      u.totp ? check('Zwei-Faktor-Anmeldung zurücksetzen (Telefon verloren)', reset) : null,
      field('Erlaubte Geräte', devs.box),
    ],
    okText: 'Sichern',
    onOk: async () => {
      const patch = { displayName: display.value, devices: devs.values() };
      if (pw.value) patch.password = pw.value;
      if (admin.checked !== u.admin) patch.admin = admin.checked;
      if (disabled.checked !== !!u.disabled) patch.disabled = disabled.checked;
      if (reset.checked) patch.resetTotp = true;
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
    title: 'Gerät umbenennen', body: field('Name', name), okText: 'Sichern',
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
  const cmd = (text) => el('div', { class: 'cmd' }, el('code', {}, text), btn('Kopieren', { cls: 'small', onclick: () => copyText(text) }));
  const missing = ['windows', 'darwin', 'linux'].filter((os) => !a.agents.some((b) => b.os === os));
  dialog({
    title: 'Gerät hinzufügen',
    body: [
      el('p', { class: 'muted small' }, 'Auf dem Rechner, der ferngesteuert werden soll, einmal ausführen. Das Skript prüft die Prüfsumme des Agents und pinnt das Server-Zertifikat. Neue Geräte erscheinen danach hier und müssen freigegeben werden.'),
      el('h3', {}, 'Windows'),
      el('p', { class: 'muted small' }, 'PowerShell „Als Administrator ausführen“ – installiert den Dienst (funktioniert auch am Sperr- und Anmeldebildschirm).'),
      cmd(win),
      el('h3', {}, 'macOS / Linux'), cmd(unix),
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
    // Must run synchronously inside the click handler.
    s.classList.remove('hidden');
    s.requestFullscreen({ navigationUI: 'hide' }).catch(() => {});
  }
}

function startSession(device, opts = {}) {
  if (state.session) state.session.close('switch');
  clearTimeout(kioskIdleTimer);
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
    this.frameLatency = [];
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
    const caps = (RTCRtpReceiver.getCapabilities && RTCRtpReceiver.getCapabilities('video')) || { codecs: [] };
    const codecs = [...new Set(caps.codecs.map((c) => c.mimeType))];
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
    // Media flows peer-to-peer; keep a running session even if the server
    // connection blips, otherwise retry once it is back.
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
        if (msg.session === this.id) this.ended(msg.reason);
        break;
    }
  }

  createPC(iceServers) {
    const pc = new RTCPeerConnection({ iceServers, iceTransportPolicy: settings.forceRelay ? 'relay' : 'all' });
    this.pc = pc;
    pc.onicecandidate = (ev) => {
      if (ev.candidate) sendSignal({ type: 'candidate', session: this.id, candidate: ev.candidate.toJSON() });
    };
    pc.ontrack = (ev) => {
      // Render every frame immediately: no jitter buffering on a LAN.
      try { ev.receiver.jitterBufferTarget = 0; } catch { /* unsupported */ }
      try { ev.receiver.playoutDelayHint = 0; } catch { /* unsupported */ }
      const v = $('#video');
      if (!v.srcObject) v.srcObject = new MediaStream();
      v.srcObject.addTrack(ev.track);
      if (ev.track.kind === 'audio') this.tryUnmute();
      if (ev.track.kind === 'video') this.watchFrames();
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
    const permanent = /übernommen|Administrator|anderen Arbeitsplatz|Berechtigung|Freigabe|revoked/i.test(reason || '');
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
    $('#video').srcObject = null;
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
    if (why === 'logout') return;
    show(KIOSK ? 'kiosk' : 'app');
    renderKiosk();
    kioskActivity();
  }

  // ---- measured display latency (per frame, from the browser itself)

  watchFrames() {
    const v = $('#video');
    if (!v.requestVideoFrameCallback) return;
    const onFrame = (now, meta) => {
      if (this.closed || !this.pc) return;
      // receiveTime: last packet of the frame arrived; expectedDisplayTime:
      // when the compositor shows it. The difference is decode + render.
      if (meta.receiveTime && meta.expectedDisplayTime) {
        const d = meta.expectedDisplayTime - meta.receiveTime;
        // Some engines report receiveTime in another timebase: keep only
        // physically plausible samples.
        if (d >= 0 && d < 500) {
          this.frameLatency.push(d);
          if (this.frameLatency.length > 120) this.frameLatency.shift();
        }
      }
      v.requestVideoFrameCallback(onFrame);
    };
    v.requestVideoFrameCallback(onFrame);
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
    const label = { Winlogon: 'Sperrbildschirm', 'Screen-saver': 'Bildschirmschoner' }[name];
    b.textContent = label || '';
    b.classList.toggle('hidden', !label);
  }

  applyCursor(shape) {
    if (this.pointerLocked) return;
    // A locally drawn cursor moves with zero latency; otherwise the cursor
    // is part of the video and the local one is hidden.
    $('#video').style.cursor = this.cursorMode === 'local' ? (shape || 'default') : 'none';
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
    const b = $('#btn-audio');
    b.innerHTML = icon($('#video').muted ? 'mute' : 'speaker');
  }

  // ---- input (viewer -> agent)

  send(obj, reliable = true) {
    const dc = reliable ? this.control : (this.motion || this.control);
    if (dc && dc.readyState === 'open') dc.send(JSON.stringify(obj));
  }

  videoPoint(ev) {
    const v = $('#video');
    const r = this.rect || (this.rect = v.getBoundingClientRect());
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
    if (this.info.t && !this.info.clipboard) return;
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

    // pointerrawupdate fires for every hardware mouse report instead of
    // once per animation frame: the remote cursor follows up to one frame
    // (8–16 ms) sooner. Falls back to pointermove where unsupported.
    const moveEvent = 'onpointerrawupdate' in window ? 'pointerrawupdate' : 'pointermove';
    h.move = (ev) => {
      if (this.pointerLocked) {
        if (ev.movementX || ev.movementY) this.send({ t: 'r', dx: ev.movementX, dy: ev.movementY }, false);
        return;
      }
      const p = this.videoPoint(ev);
      if (p.x < 0 || p.x > 1 || p.y < 0 || p.y > 1) return;
      this.send({ t: 'm', x: +p.x.toFixed(5), y: +p.y.toFixed(5) }, false);
    };
    h.pointerdown = (ev) => {
      ev.preventDefault();
      v.focus();
      if (!this.pointerLocked) {
        const p = this.videoPoint(ev);
        // The position goes over the reliable channel right before the
        // click, so a lost motion packet can never misplace a click.
        this.send({ t: 'm', x: p.x, y: p.y });
      }
      v.setPointerCapture(ev.pointerId);
      this.send({ t: 'b', b: ev.button < 0 ? 0 : ev.button, d: true });
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
    h.resize = () => { this.rect = null; };

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
      if (ev.code === 'KeyV' && (ev.ctrlKey || ev.metaKey) && this.info.clipboard) {
        // Paste: push the local clipboard first, then the key.
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
      this.rect = null;
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
    h.moveEvent = moveEvent;

    v.addEventListener(moveEvent, h.move);
    v.addEventListener('pointerdown', h.pointerdown);
    v.addEventListener('pointerup', h.pointerup);
    v.addEventListener('wheel', h.wheel, { passive: false });
    v.addEventListener('contextmenu', h.contextmenu);
    window.addEventListener('resize', h.resize);
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
    $('#btn-pointer').onclick = () => {
      const r = v.requestPointerLock({ unadjustedMovement: true });
      if (r && r.catch) r.catch(() => v.requestPointerLock());
    };
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
    v.removeEventListener(h.moveEvent, h.move);
    v.removeEventListener('pointerdown', h.pointerdown);
    v.removeEventListener('pointerup', h.pointerup);
    v.removeEventListener('wheel', h.wheel);
    v.removeEventListener('contextmenu', h.contextmenu);
    window.removeEventListener('resize', h.resize);
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
    const fps = frames / dt;
    const decodeMs = frames > 0 ? ((video.totalDecodeTime - prev.totalDecodeTime) / frames) * 1000 : 0;
    const jbCount = video.jitterBufferEmittedCount - prev.jitterBufferEmittedCount;
    const bufferMs = jbCount > 0 ? ((video.jitterBufferDelay - prev.jitterBufferDelay) / jbCount) * 1000 : 0;
    const rttMs = pair && pair.currentRoundTripTime !== undefined ? pair.currentRoundTripTime * 1000 : null;
    const lost = video.packetsLost - prev.packetsLost;
    const relayed = (local && local.candidateType === 'relay') || (remote && remote.candidateType === 'relay');
    const fl = this.frameLatency.slice().sort((a, b) => a - b);
    const displayMs = fl.length >= 10 ? fl[Math.floor(fl.length / 2)] : null; // median receive→display
    // One-way network + (measured receive→display, else buffer+decode) +
    // ~1 capture interval on the remote machine.
    const capture = 1000 / (this.info.fps || 60) / 2;
    const est = (rttMs ?? 1) / 2 + (displayMs ?? bufferMs + decodeMs) + capture + 1;
    const cls = est < 20 ? 'good' : est < 40 ? 'mid' : 'bad';

    $('#stats').replaceChildren(
      el('span', { class: cls }, `${est.toFixed(0)} ms`),
      ` · ${relayed ? 'Relay' : 'Direkt'} · ${fps.toFixed(0)} fps`,
    );
    $('#stats-detail').textContent = [
      `Verbindung        ${relayed ? 'über Server-Relay (TURN)' : 'direkt (Peer-to-Peer)'}`,
      `Netzwerk-RTT      ${rttMs !== null ? rttMs.toFixed(1) + ' ms' : '–'}`,
      `Eingabe-RTT       ${this.pingRTT !== null ? this.pingRTT.toFixed(1) + ' ms' : '–'}`,
      `Jitter-Puffer     ${bufferMs.toFixed(1)} ms`,
      `Dekodierung       ${decodeMs.toFixed(1)} ms${video.powerEfficientDecoder ? ' (Hardware)' : ''}`,
      `Empfang→Anzeige   ${displayMs !== null ? displayMs.toFixed(1) + ' ms (gemessen)' : '–'}`,
      `Bildrate          ${fps.toFixed(0)} fps (Aufnahme ${this.info.fps || '?'} fps)`,
      `Datenrate         ${mbit.toFixed(1)} Mbit/s`,
      `Auflösung         ${video.frameWidth || '?'}×${video.frameHeight || '?'}`,
      `Paketverlust      ${lost}`,
      `Encoder           ${encoderLabel(this.info.encoder || '')} · ${this.info.capture || '?'}`,
      `Latenz (geschätzt) ≈ ${est.toFixed(0)} ms`,
    ].join('\n');
  }
}

boot();
