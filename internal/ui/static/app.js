/* AMail UI: one file, no framework. Talks to /api/* on the same origin. */
(() => {
  const $ = (s, el = document) => el.querySelector(s);
  const view = $('#view'), title = $('#title');
  const H = { 'X-AMail-UI': '1' };
  let overview = null, currentView = '', timer = null;

  // ---- helpers -----------------------------------------------------------
  const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const fmtSize = n => n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : n < 1073741824 ? (n / 1048576).toFixed(1) + ' MB' : (n / 1073741824).toFixed(2) + ' GB';
  const fmtTime = t => { const d = new Date(t); return isNaN(d) ? t : d.toLocaleString(); };
  const toast = (msg, cls = '') => { const d = document.createElement('div'); d.className = cls; d.textContent = msg; $('#toast').appendChild(d); setTimeout(() => d.remove(), 4000); };
  async function api(path, opts = {}) {
    const r = await fetch(path, { ...opts, headers: { ...H, ...(opts.headers || {}) } });
    const ct = r.headers.get('content-type') || '';
    const body = ct.includes('json') ? await r.json() : await r.text();
    if (!r.ok) throw new Error(body.error || body || r.statusText);
    return body;
  }
  const post = (path, data) => api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) });
  const fileURL = (m, inline) => `/api/file?folder=${encodeURIComponent(m.folder)}&peer=${encodeURIComponent(m.peer)}&name=${encodeURIComponent(m.name)}${inline ? '&inline=1' : ''}`;
  function modal(t, html) { $('#modal-title').textContent = t; $('#modal-body').innerHTML = html; $('#modal').hidden = false; }
  $('#modal-close').onclick = () => { $('#modal').hidden = true; $('#modal-body').innerHTML = ''; };
  $('#modal').addEventListener('click', e => { if (e.target === $('#modal')) $('#modal-close').click(); });

  // ---- theme -------------------------------------------------------------
  const applyTheme = t => { document.documentElement.dataset.theme = t; try { localStorage.setItem('amail-theme', t); } catch (e) { } };
  let theme = 'light'; try { theme = localStorage.getItem('amail-theme') || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'); } catch (e) { }
  applyTheme(theme);
  $('#btn-theme').onclick = () => applyTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark');
  $('#btn-refresh').onclick = () => { loadOverview(); render(); };

  // ---- overview data (also feeds sidebar counts and chips) ---------------
  async function loadOverview() {
    try {
      overview = await api('/api/overview');
      $('#chip-node').textContent = overview.node_id + (overview.network ? ' · ' + overview.network : '');
      const st = overview.local;
      const role = st ? st.role : (overview.client_only ? 'client-only' : 'offline');
      const chip = $('#chip-role');
      chip.textContent = st ? (st.role === 'server' ? 'SERVER' : st.role === 'client' ? 'client of ' + st.server : 'searching') : role;
      chip.className = 'chip ' + (st ? 'chip--' + st.role : 'chip--searching');
      $('#c-inbox').textContent = overview.counts.inbox || '';
      $('#c-outbox').textContent = overview.counts.outbox || '';
      $('#c-forward').textContent = overview.counts.forward || '';
      $('#foot').innerHTML = `amail ${esc(overview.version)}<br>${esc(overview.mailbox)}`;
      if (currentView === 'overview') renderOverview();
    } catch (e) { toast('overview: ' + e.message, 'err'); }
  }

  // ---- views -------------------------------------------------------------
  const titles = { overview: 'Overview', inbox: 'Inbox', outbox: 'Outbox', sent: 'Sent', forward: 'Held for other nodes', failed: 'Failed', compose: 'Compose', settings: 'Node settings', log: 'Log' };
  function render() {
    const v = (location.hash || '#overview').slice(1);
    currentView = titles[v] ? v : 'overview';
    title.textContent = titles[currentView];
    document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('active', a.dataset.view === currentView));
    const fn = { overview: renderOverview, compose: renderCompose, settings: renderSettings, log: renderLog }[currentView];
    if (fn) fn(); else renderFolder(currentView);
  }
  window.addEventListener('hashchange', render);

  function renderOverview() {
    if (!overview) { view.innerHTML = '<div class="empty">Loading…</div>'; return; }
    const o = overview, st = o.local;
    const stat = (label, value, cls, icon) => `<div class="stat"><div class="stat__icon ${cls}">${icon}</div><div><div class="stat__label">${label}</div><div class="stat__value">${value}</div></div></div>`;
    let html = `<div class="cards">
      ${stat('Role', st ? esc(st.role) : (o.client_only ? 'client-only' : 'not running'), st && st.role === 'server' ? 'stat__icon--success' : '', '⇄')}
      ${stat('Inbox', o.counts.inbox, 'stat__icon--info', '📥')}
      ${stat('Outbox', o.counts.outbox, 'stat__icon--warning', '📤')}
      ${stat('Held', o.counts.forward, '', '🗂')}
      ${stat('Mailbox used', o.usage_mb + ' / ' + o.max_mailbox_mb + ' MB', o.free_mb !== undefined && o.free_mb < 1024 ? 'stat__icon--danger' : 'stat__icon--success', '💾')}
    </div>`;
    html += `<div class="card"><h3>This node</h3><dl class="kv">
      <dt>Node id</dt><dd><b>${esc(o.node_id)}</b>${o.network ? ' on network <b>' + esc(o.network) + '</b>' : ''}</dd>
      <dt>Daemon</dt><dd>${st ? `${esc(st.role)}${st.server ? ', server is <b>' + esc(st.server) + '</b>' : ''}, up ${esc(st.uptime)}, v${esc(st.version)} (${esc(st.os)}/${esc(st.arch)})${st.listening ? ', listening' : ', no inbound port'}` : '<span class="muted">' + esc(o.local_error || 'not running') + '</span>'}</dd>
      <dt>Listen</dt><dd class="mono">${esc(o.listen)}</dd>
      <dt>Key</dt><dd>${o.key_error ? '<span class="badge badge--danger">not installed</span> ' + esc(o.key_error) : 'installed, expires ' + esc(o.key_expires)}</dd>
      <dt>Mailbox</dt><dd class="mono">${esc(o.mailbox)}${o.free_mb !== undefined ? ' <span class="muted">(' + o.free_mb + ' MB free on disk)</span>' : ''}</dd>
      <dt>Config</dt><dd class="mono">${esc(o.config)}</dd>
    </dl></div>`;
    html += `<div class="card"><h3>Whitelisted nodes</h3>`;
    if (!o.peers || !o.peers.length) html += `<div class="empty">Whitelist is empty. Add nodes under Settings.</div>`;
    else {
      html += `<table><tr><th></th><th>Node</th><th>Host</th><th>Role</th><th>Version</th><th>Up</th><th>In / Out / Held</th><th>Note</th></tr>`;
      for (const p of o.peers) {
        const s = p.status;
        const dot = s ? 'dot--success' : (p.error === 'this node' ? 'dot--info' : p.host ? 'dot--danger' : '');
        html += `<tr><td><span class="dot ${dot}"></span></td><td><b>${esc(p.id)}</b></td><td class="mono">${esc(p.host ? p.host + ':' + (p.port || 4444) : '')}</td>`;
        html += s ? `<td><span class="badge badge--${s.role === 'server' ? 'success' : 'info'}">${esc(s.role)}</span></td><td>${esc(s.version)} <span class="muted">${esc(s.os)}</span></td><td>${esc(s.uptime)}</td><td>${s.inbox} / ${s.outbox} / ${s.forward}</td>` : `<td colspan="4" class="muted">${esc(p.error)}</td>`;
        html += `<td class="muted">${esc(p.note)}</td></tr>`;
      }
      html += `</table>`;
    }
    html += `</div>`;
    view.innerHTML = html;
  }

  async function renderFolder(folder) {
    view.innerHTML = '<div class="empty">Loading…</div>';
    let items;
    try { items = await api('/api/mail?folder=' + folder); } catch (e) { view.innerHTML = `<div class="empty">${esc(e.message)}</div>`; return; }
    if (!items.length) {
      const hints = { inbox: 'Nothing has arrived yet.', outbox: 'Nothing waiting to go out. Use Compose, or drop files into outbox/&lt;node&gt;/.', sent: 'Nothing sent yet.', forward: 'This node is not holding anything for other nodes.', failed: 'No failures. Good.' };
      view.innerHTML = `<div class="card"><div class="empty">${hints[folder]}</div></div>`; return;
    }
    const who = folder === 'inbox' ? 'From' : folder === 'forward' ? 'For' : 'To';
    let html = `<div class="card"><table><tr><th>${who}</th><th>File</th><th>Size</th><th>When</th>${folder === 'failed' ? '<th>Reason</th>' : ''}<th></th></tr>`;
    items.forEach((m, i) => {
      html += `<tr class="row"><td><b>${esc(m.peer)}</b>${m.origin ? '<div class="small muted">from ' + esc(m.origin) + '</div>' : ''}</td>
        <td><a href="#" data-open="${i}">${esc(m.name.replace(/^([^/]+)\//, folder === 'forward' ? '' : '$1/'))}</a></td>
        <td class="muted">${fmtSize(m.size)}</td><td class="muted small">${fmtTime(m.time)}</td>
        ${folder === 'failed' ? `<td class="small">${esc(m.error)}</td>` : ''}
        <td class="actions">${m.kind !== 'other' ? `<button class="btn btn--ghost btn--sm" data-open="${i}">Open</button>` : ''}<a class="btn btn--ghost btn--sm" href="${fileURL(m)}" download>Download</a>${folder === 'inbox' ? `<button class="btn btn--ghost btn--sm" data-reply="${i}">Reply</button>` : ''}${folder === 'failed' ? `<button class="btn btn--ghost btn--sm" data-retry="${i}">Retry</button>` : ''}<button class="btn btn--danger btn--sm" data-del="${i}">Delete</button></td></tr>`;
    });
    html += `</table></div>`;
    view.innerHTML = html;
    view.querySelectorAll('[data-open]').forEach(el => el.onclick = e => { e.preventDefault(); openMail(items[el.dataset.open]); });
    view.querySelectorAll('[data-del]').forEach(el => el.onclick = () => delMail(items[el.dataset.del], folder));
    view.querySelectorAll('[data-reply]').forEach(el => el.onclick = () => { location.hash = '#compose'; setTimeout(() => { const s = $('#to'); if (s) s.value = items[el.dataset.reply].peer; }, 50); });
    view.querySelectorAll('[data-retry]').forEach(el => el.onclick = () => retryMail(items[el.dataset.retry]));
  }

  async function openMail(m) {
    if (m.kind === 'other') { location.href = fileURL(m); return; }
    const url = fileURL(m, true);
    let body;
    if (m.kind === 'image') body = `<div class="preview"><img src="${url}" alt=""></div>`;
    else if (m.kind === 'pdf') body = `<div class="preview"><iframe src="${url}"></iframe></div>`;
    else {
      try { const t = await (await fetch(url)).text(); body = `<div class="preview"><pre>${esc(t)}</pre></div>`; }
      catch (e) { body = `<div class="empty">${esc(e.message)}</div>`; }
    }
    modal(`${m.peer} · ${m.name} (${fmtSize(m.size)})`, body + `<div class="row-actions" style="margin-top:12px"><a class="btn btn--ghost" href="${fileURL(m)}" download>Download</a><button class="btn btn--danger" id="m-del">Delete</button></div>`);
    $('#m-del').onclick = () => { $('#modal-close').click(); delMail(m, m.folder); };
  }

  async function delMail(m, folder) {
    if (!confirm(`Delete "${m.name}" from ${folder}/${m.peer}? This removes the file from disk.`)) return;
    try { await post('/api/delete', { folder: m.folder, peer: m.peer, name: m.name }); toast('Deleted ' + m.name, 'ok'); renderFolder(folder); loadOverview(); }
    catch (e) { toast(e.message, 'err'); }
  }

  async function retryMail(m) {
    // Re-queue a failed file: download it into the outbox via the send API.
    try {
      const blob = await (await fetch(fileURL(m))).blob();
      const fd = new FormData(); fd.append('to', m.peer); fd.append('files', blob, m.name.split('/').pop());
      await api('/api/send', { method: 'POST', body: fd });
      await post('/api/delete', { folder: m.folder, peer: m.peer, name: m.name });
      toast('Re-queued ' + m.name + ' for ' + m.peer, 'ok'); renderFolder('failed'); loadOverview();
    } catch (e) { toast(e.message, 'err'); }
  }

  function renderCompose() {
    const peers = (overview?.peers || []).filter(p => p.id !== overview.node_id);
    const opts = peers.map(p => `<option value="${esc(p.id)}">${esc(p.id)}${p.host ? '' : ' (via server)'}</option>`).join('');
    view.innerHTML = `<div class="card"><form class="form" id="compose">
      <div class="grid2">
        <div class="field"><label>To (node id)</label><input list="peers" id="to" required placeholder="ausa-web" pattern="[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?"><datalist id="peers">${opts}</datalist><span class="hint">Must be on the whitelist. Unknown ids end up in Failed.</span></div>
        <div class="field"><label>File name for the note (optional)</label><input id="name" placeholder="note-2026-09-17.txt"></div>
      </div>
      <div class="field"><label>Note</label><textarea id="text" placeholder="Type a message. It is saved as a .txt file in the other node's inbox."></textarea></div>
      <div class="field"><label>Attach files</label>
        <div class="drop" id="drop">Drop files here or click to choose<input type="file" id="files" multiple hidden></div>
        <div class="filelist" id="filelist"></div>
        <span class="hint">Each file goes to outbox/&lt;to&gt;/ and is delivered by the running node. Folders: copy them into the outbox folder directly.</span>
      </div>
      <div class="row-actions"><button class="btn" type="submit">Send</button><span class="muted small" id="compose-status"></span></div>
    </form></div>`;
    const drop = $('#drop'), input = $('#files'), list = $('#filelist');
    let files = [];
    const show = () => list.innerHTML = files.map(f => `<span>${esc(f.name)} · ${fmtSize(f.size)}</span>`).join('');
    drop.onclick = () => input.click();
    input.onchange = () => { files = files.concat([...input.files]); show(); };
    drop.ondragover = e => { e.preventDefault(); drop.classList.add('over'); };
    drop.ondragleave = () => drop.classList.remove('over');
    drop.ondrop = e => { e.preventDefault(); drop.classList.remove('over'); files = files.concat([...e.dataTransfer.files]); show(); };
    $('#compose').onsubmit = async e => {
      e.preventDefault();
      const to = $('#to').value.trim(), text = $('#text').value, name = $('#name').value.trim();
      const btn = e.target.querySelector('button'); btn.disabled = true; $('#compose-status').textContent = 'Sending…';
      try {
        let n = 0;
        if (text.trim()) { await post('/api/send', { to, text, name }); n++; }
        if (files.length) {
          const fd = new FormData(); fd.append('to', to); files.forEach(f => fd.append('files', f, f.name));
          const r = await api('/api/send', { method: 'POST', body: fd }); n += r.queued.length;
        }
        if (!n) throw new Error('Nothing to send: write a note or attach a file.');
        toast(`Queued ${n} item(s) for ${to}`, 'ok'); $('#text').value = ''; files = []; show(); loadOverview();
        $('#compose-status').textContent = `Queued. Watch Outbox → Sent.`;
      } catch (err) { toast(err.message, 'err'); $('#compose-status').textContent = ''; }
      btn.disabled = false;
    };
  }

  async function renderSettings() {
    view.innerHTML = '<div class="empty">Loading…</div>';
    let c;
    try { c = await api('/api/settings'); } catch (e) { view.innerHTML = `<div class="empty">${esc(e.message)}</div>`; return; }
    const num = (id, label, hint) => `<div class="field"><label>${label}</label><input type="number" min="1" id="${id}" value="${c[id]}"><span class="hint">${hint}</span></div>`;
    view.innerHTML = `<form class="form" id="settings" style="max-width:none">
      <div class="card"><h3>Node</h3><div class="grid3">
        <div class="field"><label>Node id</label><input value="${esc(c.node_id)}" disabled><span class="hint">Comes from the network key.</span></div>
        <div class="field"><label>Listen (inbound port)</label><input id="listen" value="${esc(c.listen)}"><span class="hint">":4444", "127.0.0.1:4444" or "off" for a client-only node with no open port.</span></div>
        <div class="field"><label>Web UI address</label><input id="ui_listen" value="${esc(c.ui_listen)}"><span class="hint">Loopback only. "off" disables. Needs a full restart of amail run.</span></div>
        <div class="field"><label>Mailbox folder</label><input id="mailbox" value="${esc(c.mailbox)}"></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="server_eligible" ${c.server_eligible ? 'checked' : ''}> May become the server</label></div>
      </div></div>
      <div class="card"><h3>Timing and limits</h3><div class="grid3">
        ${num('poll_seconds', 'Poll every (s)', 'Outbox scan and pull interval.')}
        ${num('discovery_seconds', 'Re-check server every (s)', 'How often the whitelist is walked for a server.')}
        ${num('max_file_mb', 'Max file (MB)', 'Larger files are rejected.')}
        ${num('max_mailbox_mb', 'Max mailbox (MB)', 'Cap on inbox + held + failed.')}
        ${num('min_free_mb', 'Min free disk (MB)', 'Refuse files below this.')}
        ${num('max_connections', 'Max connections', 'Concurrent inbound; a quarter per IP.')}
        ${num('max_per_ip_per_min', 'Failed handshakes / IP / min', 'Strangers are dropped after this many.')}
      </div></div>
      <div class="card"><h3>Whitelist <span class="muted small">(order matters: the first reachable entry is the server)</span></h3>
        <table class="list-edit" id="wl"><tr><th>#</th><th>Id</th><th>Host</th><th>Port</th><th>Note</th><th></th></tr></table>
        <div class="row-actions" style="margin-top:10px"><button type="button" class="btn btn--ghost btn--sm" id="wl-add">+ Add node</button></div>
      </div>
      <div class="card"><h3>Blacklist</h3>
        <table class="list-edit" id="bl"><tr><th>Node id</th><th>Host / IP / CIDR</th><th>Reason</th><th></th></tr></table>
        <div class="row-actions" style="margin-top:10px"><button type="button" class="btn btn--ghost btn--sm" id="bl-add">+ Add entry</button></div>
      </div>
      <div class="row-actions"><button class="btn" type="submit">Save settings</button>
        ${overview?.in_process ? '<span class="muted small">Saving restarts the node in place (the UI stays up).</span><button type="button" class="btn btn--ghost" id="restart">Restart node now</button>' : '<span class="muted small">The running service picks changes up when it restarts (systemctl restart / scheduled task).</span>'}
      </div>
    </form>`;
    const wl = $('#wl'), bl = $('#bl');
    const wlRow = (p = {}) => { const tr = document.createElement('tr'); tr.innerHTML = `<td class="muted">↕</td><td><input class="id" value="${esc(p.id || '')}" placeholder="node-id"></td><td><input class="host" value="${esc(p.host || '')}" placeholder="ip or hostname (blank = via server)"></td><td><input class="port" type="number" value="${p.port || ''}" placeholder="4444" style="width:90px"></td><td><input class="note" value="${esc(p.note || '')}"></td><td class="actions"><button type="button" class="btn btn--ghost btn--sm up">↑</button><button type="button" class="btn btn--ghost btn--sm down">↓</button><button type="button" class="btn btn--danger btn--sm rm">✕</button></td>`; wl.appendChild(tr); wire(tr); };
    const blRow = (b = {}) => { const tr = document.createElement('tr'); tr.innerHTML = `<td><input class="id" value="${esc(b.id || '')}" placeholder="node-id"></td><td><input class="host" value="${esc(b.host || '')}" placeholder="10.0.0.0/8"></td><td><input class="reason" value="${esc(b.reason || '')}"></td><td class="actions"><button type="button" class="btn btn--danger btn--sm rm">✕</button></td>`; bl.appendChild(tr); wire(tr); };
    function wire(tr) {
      tr.querySelector('.rm').onclick = () => tr.remove();
      tr.querySelector('.up')?.addEventListener('click', () => { const p = tr.previousElementSibling; if (p && p.querySelector('input')) tr.parentNode.insertBefore(tr, p); });
      tr.querySelector('.down')?.addEventListener('click', () => { const n = tr.nextElementSibling; if (n) tr.parentNode.insertBefore(n, tr); });
    }
    (c.whitelist || []).forEach(wlRow); (c.blacklist || []).forEach(blRow);
    $('#wl-add').onclick = () => wlRow(); $('#bl-add').onclick = () => blRow();
    $('#restart')?.addEventListener('click', async () => { try { await post('/api/restart', {}); toast('Node restarting', 'ok'); setTimeout(loadOverview, 1500); } catch (e) { toast(e.message, 'err'); } });
    $('#settings').onsubmit = async e => {
      e.preventDefault();
      const n = id => parseInt($('#' + id).value, 10) || 0;
      const out = { ...c, listen: $('#listen').value.trim(), ui_listen: $('#ui_listen').value.trim(), mailbox: $('#mailbox').value.trim(), server_eligible: $('#server_eligible').checked,
        poll_seconds: n('poll_seconds'), discovery_seconds: n('discovery_seconds'), max_file_mb: n('max_file_mb'), max_mailbox_mb: n('max_mailbox_mb'), min_free_mb: n('min_free_mb'), max_connections: n('max_connections'), max_per_ip_per_min: n('max_per_ip_per_min'),
        whitelist: [...wl.querySelectorAll('tr')].filter(tr => tr.querySelector('input')).map(tr => ({ id: tr.querySelector('.id').value.trim(), host: tr.querySelector('.host').value.trim(), port: parseInt(tr.querySelector('.port').value, 10) || 0, note: tr.querySelector('.note').value.trim() })).filter(p => p.id),
        blacklist: [...bl.querySelectorAll('tr')].filter(tr => tr.querySelector('input')).map(tr => ({ id: tr.querySelector('.id').value.trim(), host: tr.querySelector('.host').value.trim(), reason: tr.querySelector('.reason').value.trim() })).filter(b => b.id || b.host) };
      try { const r = await post('/api/settings', out); toast(r.restarted ? 'Saved; node restarting' : 'Saved', 'ok'); setTimeout(loadOverview, 1500); }
      catch (err) { toast(err.message, 'err'); }
    };
  }

  async function renderLog() {
    view.innerHTML = `<div class="tabs"><button class="active" data-tab="log">amail.log</button><button data-tab="history">History</button></div><div id="logbody" class="log">Loading…</div>`;
    const load = async tab => {
      view.querySelectorAll('.tabs button').forEach(b => b.classList.toggle('active', b.dataset.tab === tab));
      const el = $('#logbody');
      try {
        if (tab === 'log') { const lines = await api('/api/log?lines=300'); el.textContent = lines.length ? lines.join('\n') : '(empty)'; el.scrollTop = el.scrollHeight; }
        else { const h = await api('/api/history?lines=300'); el.innerHTML = h.length ? h.map(e => `${esc(e.time)}  ${esc(String(e.event).padEnd(9))} ${esc(e.name)}  ${esc(e.from)} → ${esc(e.to)}${e.via ? ' via ' + esc(e.via) : ''}  ${fmtSize(e.size || 0)}${e.note && e.event === 'failed' ? '  ' + esc(e.note) : ''}`).join('\n') : '(no events yet)'; }
      } catch (e) { el.textContent = e.message; }
    };
    view.querySelectorAll('.tabs button').forEach(b => b.onclick = () => load(b.dataset.tab));
    load('log');
  }

  // ---- boot --------------------------------------------------------------
  loadOverview().then(render);
  timer = setInterval(() => { loadOverview(); if (['inbox', 'outbox', 'sent', 'forward', 'failed'].includes(currentView) && $('#modal').hidden) renderFolder(currentView); }, 15000);
})();
